package openshift

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/konveyor/crane-lib/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeploymentConfigConversionDisabledByDefault(t *testing.T) {
	plugin := &OpenShiftTransformPlugin{}
	response, err := plugin.Run(transform.PluginRequest{Unstructured: deploymentConfigFixture()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.IsWhiteOut {
		t.Fatal("conversion must be disabled by default")
	}
	if len(response.NewResources) != 0 {
		t.Fatalf("expected no generated resources, got %d", len(response.NewResources))
	}
}

func TestDeploymentConfigConversionRequiresExactGVK(t *testing.T) {
	resource := deploymentConfigFixture()
	resource.SetAPIVersion("example.com/v1")

	plugin := &OpenShiftTransformPlugin{}
	response, err := plugin.Run(transform.PluginRequest{
		Unstructured: resource,
		Extras:       map[string]string{ConvertDeploymentConfigsFlag: "true"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.IsWhiteOut || len(response.NewResources) != 0 {
		t.Fatal("a DeploymentConfig from another API group must not be converted")
	}
}

func TestConvertRollingDeploymentConfig(t *testing.T) {
	resource := deploymentConfigFixture()
	resource.SetUID("source-uid")
	resource.SetResourceVersion("123")
	resource.SetFinalizers([]string{"example.com/finalizer"})
	resource.SetAnnotations(map[string]string{
		"example.com/preserved":                      "true",
		DeploymentConfigConversionStatusAnnotation:   "skipped",
		DeploymentConfigConversionReasonAnnotation:   "old reason",
		DeploymentConfigConversionWarningsAnnotation: "old warning",
	})
	resource.Object["metadata"].(map[string]interface{})["ownerReferences"] = []interface{}{
		map[string]interface{}{
			"apiVersion": "example.com/v1",
			"kind":       "Owner",
			"name":       "owner",
			"uid":        "owner-uid",
		},
	}
	resource.Object["status"] = map[string]interface{}{"latestVersion": int64(4)}

	spec := resource.Object["spec"].(map[string]interface{})
	spec["replicas"] = int64(0)
	spec["minReadySeconds"] = int64(5)
	spec["revisionHistoryLimit"] = int64(3)
	spec["paused"] = true
	spec["triggers"] = []interface{}{
		map[string]interface{}{
			"type": "ImageChange",
			"imageChangeParams": map[string]interface{}{
				"automatic":      true,
				"containerNames": []interface{}{"app"},
				"from": map[string]interface{}{
					"kind": "ImageStreamTag",
					"name": "app:latest",
				},
			},
		},
	}
	spec["strategy"] = map[string]interface{}{
		"type": "Rolling",
		"rollingParams": map[string]interface{}{
			"maxSurge":            "30%",
			"maxUnavailable":      int64(1),
			"timeoutSeconds":      int64(600),
			"intervalSeconds":     int64(1),
			"updatePeriodSeconds": int64(1),
		},
		"activeDeadlineSeconds": int64(21600),
	}
	templateSpec := spec["template"].(map[string]interface{})["spec"].(map[string]interface{})
	template := spec["template"].(map[string]interface{})
	template["metadata"].(map[string]interface{})["uid"] = "template-uid"
	template["metadata"].(map[string]interface{})["finalizers"] = []interface{}{"template.example.com/finalizer"}
	container := templateSpec["containers"].([]interface{})[0].(map[string]interface{})
	container["env"] = []interface{}{map[string]interface{}{"name": "MODE", "value": "production"}}
	container["readinessProbe"] = map[string]interface{}{
		"httpGet": map[string]interface{}{"path": "/ready", "port": int64(8080)},
	}
	container["resources"] = map[string]interface{}{
		"requests": map[string]interface{}{"cpu": "100m"},
	}
	templateSpec["securityContext"] = map[string]interface{}{
		"runAsUser": int64(1000560000),
		"fsGroup":   int64(1000560000),
	}
	templateSpec["volumes"] = []interface{}{
		map[string]interface{}{
			"name": "data",
			"persistentVolumeClaim": map[string]interface{}{
				"claimName": "old-pvc",
			},
		},
	}

	plugin := &OpenShiftTransformPlugin{}
	response, err := plugin.Run(transform.PluginRequest{
		Unstructured: resource,
		Extras: map[string]string{
			ConvertDeploymentConfigsFlag: "true",
			PVCRenameMapFlag:             "old-pvc:new-pvc",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !response.IsWhiteOut {
		t.Fatal("successful conversion must whiteout the source DeploymentConfig")
	}
	if len(response.NewResources) != 1 {
		t.Fatalf("expected one generated Deployment, got %d", len(response.NewResources))
	}

	deployment := response.NewResources[0]
	if deployment.GetAPIVersion() != "apps/v1" || deployment.GetKind() != "Deployment" {
		t.Fatalf("unexpected generated GVK: %s, %s", deployment.GetAPIVersion(), deployment.GetKind())
	}
	if deployment.GetName() != "app" || deployment.GetNamespace() != "example" {
		t.Fatalf("unexpected generated identity: %s/%s", deployment.GetNamespace(), deployment.GetName())
	}
	if deployment.GetUID() != "" || deployment.GetResourceVersion() != "" || len(deployment.GetFinalizers()) != 0 || len(deployment.GetOwnerReferences()) != 0 {
		t.Fatal("generated Deployment contains server-managed metadata")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "status"); found {
		t.Fatal("generated Deployment must not contain status")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "metadata", "creationTimestamp"); found {
		t.Fatal("generated Deployment contains metadata.creationTimestamp")
	}

	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	if err != nil || !found || replicas != 0 {
		t.Fatalf("explicit zero replicas were not preserved: value=%d found=%v err=%v", replicas, found, err)
	}
	strategyType, _, _ := unstructured.NestedString(deployment.Object, "spec", "strategy", "type")
	if strategyType != "RollingUpdate" {
		t.Fatalf("expected RollingUpdate strategy, got %q", strategyType)
	}
	maxSurge, _, _ := unstructured.NestedString(deployment.Object, "spec", "strategy", "rollingUpdate", "maxSurge")
	if maxSurge != "30%" {
		t.Fatalf("expected maxSurge 30%%, got %q", maxSurge)
	}
	volumes, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	claimName := volumes[0].(map[string]interface{})["persistentVolumeClaim"].(map[string]interface{})["claimName"].(string)
	if claimName != "new-pvc" {
		t.Fatalf("expected renamed PVC, got %q", claimName)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "template", "spec", "securityContext", "runAsUser"); found {
		t.Fatal("SCC-injected runAsUser was not removed")
	}
	warnings := deployment.GetAnnotations()[DeploymentConfigConversionWarningsAnnotation]
	if !strings.Contains(warnings, "ImageChange") || !strings.Contains(warnings, "manual rollout") {
		t.Fatalf("expected trigger warnings annotation, got %q", warnings)
	}
	if deployment.GetAnnotations()["example.com/preserved"] != "true" || deployment.GetLabels()["app"] != "demo" {
		t.Fatal("user metadata was not preserved")
	}
	if _, found := deployment.GetAnnotations()[DeploymentConfigConversionStatusAnnotation]; found {
		t.Fatal("stale conversion status annotation was preserved")
	}
	if _, found := deployment.GetAnnotations()[DeploymentConfigConversionReasonAnnotation]; found {
		t.Fatal("stale conversion reason annotation was preserved")
	}
	if serviceAccount, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "serviceAccountName"); serviceAccount != "app" {
		t.Fatalf("expected service account to be preserved, got %q", serviceAccount)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "template", "metadata", "uid"); found {
		t.Fatal("pod template contains server-managed metadata")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "template", "metadata", "creationTimestamp"); found {
		t.Fatal("pod template contains metadata.creationTimestamp")
	}
	if paused, _, _ := unstructured.NestedBool(deployment.Object, "spec", "paused"); !paused {
		t.Fatal("paused state was not preserved")
	}
	if minReady, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "minReadySeconds"); minReady != 5 {
		t.Fatalf("expected minReadySeconds 5, got %d", minReady)
	}
	if history, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "revisionHistoryLimit"); history != 3 {
		t.Fatalf("expected revisionHistoryLimit 3, got %d", history)
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	convertedContainer := containers[0].(map[string]interface{})
	if _, ok := convertedContainer["readinessProbe"]; !ok {
		t.Fatal("container readiness probe was not preserved")
	}
	if _, ok := convertedContainer["env"]; !ok {
		t.Fatal("container environment was not preserved")
	}
	if _, ok := convertedContainer["resources"]; !ok {
		t.Fatal("container resources were not preserved")
	}
}

func TestDeploymentConfigStrategyConversion(t *testing.T) {
	tests := []struct {
		name             string
		strategy         map[string]interface{}
		expectedStrategy string
	}{
		{name: "default rolling", strategy: map[string]interface{}{}, expectedStrategy: "RollingUpdate"},
		{name: "recreate", strategy: map[string]interface{}{"type": "Recreate"}, expectedStrategy: "Recreate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource := deploymentConfigFixture()
			resource.Object["spec"].(map[string]interface{})["strategy"] = tt.strategy

			response, err := (&OpenShiftTransformPlugin{}).Run(transform.PluginRequest{
				Unstructured: resource,
				Extras:       map[string]string{ConvertDeploymentConfigsFlag: "true"},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !response.IsWhiteOut || len(response.NewResources) != 1 {
				t.Fatal("expected successful conversion")
			}
			strategy, _, _ := unstructured.NestedString(response.NewResources[0].Object, "spec", "strategy", "type")
			if strategy != tt.expectedStrategy {
				t.Fatalf("expected strategy %q, got %q", tt.expectedStrategy, strategy)
			}
		})
	}
}

func TestDeploymentConfigConversionIsDeterministicAndRepeatable(t *testing.T) {
	plugin := &OpenShiftTransformPlugin{}
	request := transform.PluginRequest{
		Unstructured: deploymentConfigFixture(),
		Extras:       map[string]string{ConvertDeploymentConfigsFlag: "true"},
	}

	first, err := plugin.Run(request)
	if err != nil {
		t.Fatalf("first conversion failed: %v", err)
	}
	second, err := plugin.Run(request)
	if err != nil {
		t.Fatalf("second conversion failed: %v", err)
	}
	if !reflect.DeepEqual(first.NewResources, second.NewResources) {
		t.Fatal("repeated conversion produced different Deployments")
	}

	repeated, err := plugin.Run(transform.PluginRequest{
		Unstructured: first.NewResources[0],
		Extras:       request.Extras,
	})
	if err != nil {
		t.Fatalf("processing generated Deployment failed: %v", err)
	}
	if repeated.IsWhiteOut || len(repeated.NewResources) != 0 || len(repeated.Patches) != 0 {
		t.Fatal("processing the generated Deployment must not generate another resource")
	}
}

func TestConversionWarningsAreCapped(t *testing.T) {
	warning := cappedWarnings([]string{strings.Repeat("x", conversionWarningsMaxLength+100)})
	if len(warning) != conversionWarningsMaxLength {
		t.Fatalf("expected warning annotation length %d, got %d", conversionWarningsMaxLength, len(warning))
	}
	if !strings.HasSuffix(warning, "... (truncated)") {
		t.Fatalf("expected truncation suffix, got %q", warning[len(warning)-20:])
	}
}

func TestUnsupportedDeploymentConfigRemainsActive(t *testing.T) {
	tests := []struct {
		name           string
		modify         func(map[string]interface{})
		reasonContains string
	}{
		{
			name: "custom strategy",
			modify: func(spec map[string]interface{}) {
				spec["strategy"] = map[string]interface{}{"type": "Custom"}
			},
			reasonContains: "Custom deployment strategy",
		},
		{
			name: "lifecycle hook",
			modify: func(spec map[string]interface{}) {
				spec["strategy"] = map[string]interface{}{
					"type": "Rolling",
					"rollingParams": map[string]interface{}{
						"pre": map[string]interface{}{"failurePolicy": "Abort"},
					},
				}
			},
			reasonContains: "lifecycle hooks",
		},
		{
			name: "test deployment",
			modify: func(spec map[string]interface{}) {
				spec["test"] = true
			},
			reasonContains: "spec.test",
		},
		{
			name: "missing template",
			modify: func(spec map[string]interface{}) {
				delete(spec, "template")
			},
			reasonContains: "spec.template",
		},
		{
			name: "invalid selector",
			modify: func(spec map[string]interface{}) {
				spec["selector"] = map[string]interface{}{"app": "other"}
			},
			reasonContains: "does not match",
		},
		{
			name: "negative replicas",
			modify: func(spec map[string]interface{}) {
				spec["replicas"] = int64(-1)
			},
			reasonContains: "must not be negative",
		},
		{
			name: "missing containers",
			modify: func(spec map[string]interface{}) {
				spec["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"] = []interface{}{}
			},
			reasonContains: "containers must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource := deploymentConfigFixture()
			tt.modify(resource.Object["spec"].(map[string]interface{}))

			response, err := (&OpenShiftTransformPlugin{}).Run(transform.PluginRequest{
				Unstructured: resource,
				Extras:       map[string]string{ConvertDeploymentConfigsFlag: "true"},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if response.IsWhiteOut {
				t.Fatal("unsupported DeploymentConfig must remain active")
			}
			if len(response.NewResources) != 0 {
				t.Fatal("unsupported DeploymentConfig must not generate a Deployment")
			}

			original, err := resource.MarshalJSON()
			if err != nil {
				t.Fatalf("marshal source: %v", err)
			}
			patched, err := response.Patches.Apply(original)
			if err != nil {
				t.Fatalf("apply status patch: %v", err)
			}
			var result map[string]interface{}
			if err := json.Unmarshal(patched, &result); err != nil {
				t.Fatalf("unmarshal patched source: %v", err)
			}
			annotations := result["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
			if annotations[DeploymentConfigConversionStatusAnnotation] != "skipped" {
				t.Fatalf("expected skipped status, got %#v", annotations)
			}
			reason, _ := annotations[DeploymentConfigConversionReasonAnnotation].(string)
			if !strings.Contains(reason, tt.reasonContains) {
				t.Fatalf("expected reason containing %q, got %q", tt.reasonContains, reason)
			}
		})
	}
}

func deploymentConfigFixture() unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openshift.io/v1",
		"kind":       "DeploymentConfig",
		"metadata": map[string]interface{}{
			"name":      "app",
			"namespace": "example",
			"labels": map[string]interface{}{
				"app": "demo",
			},
			"annotations": map[string]interface{}{
				"example.com/preserved": "true",
			},
		},
		"spec": map[string]interface{}{
			"replicas": int64(2),
			"selector": map[string]interface{}{
				"app": "demo",
			},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "demo",
					},
				},
				"spec": map[string]interface{}{
					"serviceAccountName": "app",
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "app",
							"image": "quay.io/example/app:latest",
						},
					},
				},
			},
			"strategy": map[string]interface{}{"type": "Rolling"},
			"triggers": []interface{}{map[string]interface{}{"type": "ConfigChange"}},
		},
	}}
}
