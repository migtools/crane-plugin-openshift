package openshift

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	appsv1 "github.com/openshift/api/apps/v1"
	k8sappsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	DeploymentConfigConversionStatusAnnotation   = "crane.konveyor.io/deploymentconfig-conversion-status"
	DeploymentConfigConversionReasonAnnotation   = "crane.konveyor.io/deploymentconfig-conversion-reason"
	DeploymentConfigConversionWarningsAnnotation = "crane.konveyor.io/deploymentconfig-conversion-warnings"
	conversionWarningsMaxLength                  = 4096
)

var deploymentConfigGVK = schema.GroupVersionKind{
	Group:   "apps.openshift.io",
	Version: "v1",
	Kind:    "DeploymentConfig",
}

func isDeploymentConfig(u unstructured.Unstructured) bool {
	return u.GroupVersionKind() == deploymentConfigGVK
}

// ConvertDeploymentConfig converts the portable subset of a DeploymentConfig.
// A non-empty skip reason means the source must remain active.
func ConvertDeploymentConfig(u unstructured.Unstructured, fields OpenshiftOptionalFields) (*unstructured.Unstructured, []string, string, error) {
	raw, err := u.MarshalJSON()
	if err != nil {
		return nil, nil, "", err
	}

	deploymentConfig := &appsv1.DeploymentConfig{}
	if err := json.Unmarshal(raw, deploymentConfig); err != nil {
		return nil, nil, "", err
	}

	skipReasons := unsupportedDeploymentConfigBehavior(deploymentConfig)
	if len(skipReasons) > 0 {
		return nil, nil, strings.Join(skipReasons, "; "), nil
	}

	warnings, triggerSkipReasons := deploymentConfigWarnings(deploymentConfig)
	if len(triggerSkipReasons) > 0 {
		return nil, nil, strings.Join(triggerSkipReasons, "; "), nil
	}

	template, found, err := unstructured.NestedMap(u.Object, "spec", "template")
	if err != nil {
		return nil, nil, "", err
	}
	if !found {
		return nil, nil, "spec.template is required", nil
	}
	removeServerManagedMetadata(template, "metadata")
	if err := sanitizePodTemplateImagePullSecrets(template, fields); err != nil {
		return nil, nil, "", err
	}
	volumes, found, err := unstructured.NestedSlice(template, "spec", "volumes")
	if err != nil {
		return nil, nil, "", err
	}
	if found {
		for i := range volumes {
			volume, ok := volumes[i].(map[string]interface{})
			if !ok {
				continue
			}
			claim, ok := volume["persistentVolumeClaim"].(map[string]interface{})
			if !ok {
				continue
			}
			claimName, _ := claim["claimName"].(string)
			if replacement, ok := fields.PVCRenameMap[claimName]; ok {
				claim["claimName"] = replacement
			}
		}
		if err := unstructured.SetNestedSlice(template, volumes, "spec", "volumes"); err != nil {
			return nil, nil, "", err
		}
	}

	replicas := deploymentConfig.Spec.Replicas
	annotations := cloneStringMap(deploymentConfig.Annotations)
	delete(annotations, DeploymentConfigConversionStatusAnnotation)
	delete(annotations, DeploymentConfigConversionReasonAnnotation)
	delete(annotations, DeploymentConfigConversionWarningsAnnotation)
	deployment := &k8sappsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        deploymentConfig.Name,
			Namespace:   deploymentConfig.Namespace,
			Labels:      cloneStringMap(deploymentConfig.Labels),
			Annotations: annotations,
		},
		Spec: k8sappsv1.DeploymentSpec{
			Replicas:             &replicas,
			Selector:             &metav1.LabelSelector{MatchLabels: cloneStringMap(deploymentConfig.Spec.Selector)},
			Template:             *deploymentConfig.Spec.Template,
			MinReadySeconds:      deploymentConfig.Spec.MinReadySeconds,
			RevisionHistoryLimit: deploymentConfig.Spec.RevisionHistoryLimit,
			Paused:               deploymentConfig.Spec.Paused,
		},
	}

	switch deploymentConfig.Spec.Strategy.Type {
	case "", appsv1.DeploymentStrategyTypeRolling:
		deployment.Spec.Strategy.Type = k8sappsv1.RollingUpdateDeploymentStrategyType
		if params := deploymentConfig.Spec.Strategy.RollingParams; params != nil {
			deployment.Spec.Strategy.RollingUpdate = &k8sappsv1.RollingUpdateDeployment{
				MaxUnavailable: params.MaxUnavailable,
				MaxSurge:       params.MaxSurge,
			}
		}
	case appsv1.DeploymentStrategyTypeRecreate:
		deployment.Spec.Strategy.Type = k8sappsv1.RecreateDeploymentStrategyType
	}

	if len(warnings) > 0 {
		if deployment.Annotations == nil {
			deployment.Annotations = map[string]string{}
		}
		deployment.Annotations[DeploymentConfigConversionWarningsAnnotation] = cappedWarnings(warnings)
	}

	convertedJSON, err := json.Marshal(deployment)
	if err != nil {
		return nil, nil, "", err
	}
	converted := &unstructured.Unstructured{}
	if err := converted.UnmarshalJSON(convertedJSON); err != nil {
		return nil, nil, "", err
	}
	if err := unstructured.SetNestedMap(converted.Object, template, "spec", "template"); err != nil {
		return nil, nil, "", err
	}
	delete(converted.Object, "status")
	removeServerManagedMetadata(converted.Object, "metadata")
	removeServerManagedMetadata(converted.Object, "spec", "template", "metadata")
	convertedJSON, err = converted.MarshalJSON()
	if err != nil {
		return nil, nil, "", err
	}

	securityPatch, err := StripSecurityContext(*converted)
	if err != nil {
		return nil, nil, "", err
	}
	if len(securityPatch) > 0 {
		convertedJSON, err = securityPatch.Apply(convertedJSON)
		if err != nil {
			return nil, nil, "", err
		}
		if err := converted.UnmarshalJSON(convertedJSON); err != nil {
			return nil, nil, "", err
		}
	}

	return converted, warnings, "", nil
}

func sanitizePodTemplateImagePullSecrets(template map[string]interface{}, fields OpenshiftOptionalFields) error {
	pullSecrets, found, err := unstructured.NestedSlice(template, "spec", "imagePullSecrets")
	if err != nil || !found {
		return err
	}

	sanitized := make([]interface{}, 0, len(pullSecrets))
	for _, item := range pullSecrets {
		pullSecret, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("spec.template.spec.imagePullSecrets contains an invalid entry")
		}
		name, _ := pullSecret["name"].(string)
		newName, keep := transformPullSecretName(name, fields)
		if !keep {
			continue
		}
		pullSecret["name"] = newName
		sanitized = append(sanitized, pullSecret)
	}

	return unstructured.SetNestedSlice(template, sanitized, "spec", "imagePullSecrets")
}

func unsupportedDeploymentConfigBehavior(deploymentConfig *appsv1.DeploymentConfig) []string {
	var reasons []string
	strategy := deploymentConfig.Spec.Strategy

	if deploymentConfig.Name == "" {
		reasons = append(reasons, "metadata.name is required")
	}
	if deploymentConfig.Spec.Replicas < 0 {
		reasons = append(reasons, "spec.replicas must not be negative")
	}
	if deploymentConfig.Spec.Template == nil {
		reasons = append(reasons, "spec.template is required")
	} else if len(deploymentConfig.Spec.Template.Spec.Containers) == 0 {
		reasons = append(reasons, "spec.template.spec.containers must not be empty")
	}
	if deploymentConfig.Spec.Test {
		reasons = append(reasons, "spec.test is not supported by Deployment")
	}
	if strategy.Type == appsv1.DeploymentStrategyTypeCustom {
		reasons = append(reasons, "Custom deployment strategy is not supported by Deployment")
	} else if strategy.Type != "" && strategy.Type != appsv1.DeploymentStrategyTypeRolling && strategy.Type != appsv1.DeploymentStrategyTypeRecreate {
		reasons = append(reasons, fmt.Sprintf("deployment strategy %q is not supported", strategy.Type))
	}
	if strategy.CustomParams != nil {
		reasons = append(reasons, "customParams are not supported by Deployment")
	}
	if hasDeploymentConfigLifecycleHooks(strategy) {
		reasons = append(reasons, "deployment lifecycle hooks are not supported by Deployment")
	}

	if deploymentConfig.Spec.Template != nil {
		if len(deploymentConfig.Spec.Selector) == 0 {
			reasons = append(reasons, "spec.selector must not be empty")
		} else if !selectorMatchesLabels(deploymentConfig.Spec.Selector, deploymentConfig.Spec.Template.Labels) {
			reasons = append(reasons, "spec.selector does not match spec.template.metadata.labels")
		}
	}

	return reasons
}

func hasDeploymentConfigLifecycleHooks(strategy appsv1.DeploymentStrategy) bool {
	if params := strategy.RollingParams; params != nil && (params.Pre != nil || params.Post != nil) {
		return true
	}
	if params := strategy.RecreateParams; params != nil && (params.Pre != nil || params.Mid != nil || params.Post != nil) {
		return true
	}
	return false
}

func selectorMatchesLabels(selector, labels map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func deploymentConfigWarnings(deploymentConfig *appsv1.DeploymentConfig) ([]string, []string) {
	var warnings []string
	var skipReasons []string
	hasConfigChange := deploymentConfig.Spec.Triggers == nil

	for _, trigger := range deploymentConfig.Spec.Triggers {
		switch trigger.Type {
		case appsv1.DeploymentTriggerOnConfigChange:
			hasConfigChange = true
		case appsv1.DeploymentTriggerOnImageChange:
			warnings = append(warnings, "ImageChange trigger was removed; configure image automation on the target cluster")
		default:
			skipReasons = append(skipReasons, fmt.Sprintf("deployment trigger %q is not supported", trigger.Type))
		}
	}
	if !hasConfigChange {
		warnings = append(warnings, "DeploymentConfig without a ConfigChange trigger had manual rollout semantics; Deployment rolls out automatically when its pod template changes")
	}

	strategy := deploymentConfig.Spec.Strategy
	if params := strategy.RollingParams; params != nil {
		if params.UpdatePeriodSeconds != nil {
			warnings = append(warnings, "rollingParams.updatePeriodSeconds was removed")
		}
		if params.IntervalSeconds != nil {
			warnings = append(warnings, "rollingParams.intervalSeconds was removed")
		}
		if params.TimeoutSeconds != nil {
			warnings = append(warnings, "rollingParams.timeoutSeconds was removed")
		}
	}
	if params := strategy.RecreateParams; params != nil && params.TimeoutSeconds != nil {
		warnings = append(warnings, "recreateParams.timeoutSeconds was removed")
	}
	if !reflect.DeepEqual(strategy.Resources, corev1.ResourceRequirements{}) {
		warnings = append(warnings, "strategy.resources for deployer pods were removed")
	}
	if len(strategy.Labels) > 0 {
		warnings = append(warnings, "strategy.labels for deployer pods were removed")
	}
	if len(strategy.Annotations) > 0 {
		warnings = append(warnings, "strategy.annotations for deployer pods were removed")
	}
	if strategy.ActiveDeadlineSeconds != nil {
		warnings = append(warnings, "strategy.activeDeadlineSeconds for deployer pods was removed")
	}

	return warnings, skipReasons
}

func removeServerManagedMetadata(object map[string]interface{}, path ...string) {
	for _, field := range []string{
		"uid",
		"resourceVersion",
		"generation",
		"creationTimestamp",
		"deletionTimestamp",
		"deletionGracePeriodSeconds",
		"managedFields",
		"ownerReferences",
		"finalizers",
		"selfLink",
	} {
		fieldPath := append([]string(nil), path...)
		unstructured.RemoveNestedField(object, append(fieldPath, field)...)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cappedWarnings(warnings []string) string {
	warnings = append([]string(nil), warnings...)
	sort.Strings(warnings)
	value := strings.Join(warnings, "; ")
	if len(value) <= conversionWarningsMaxLength {
		return value
	}
	const suffix = "... (truncated)"
	return value[:conversionWarningsMaxLength-len(suffix)] + suffix
}

// DeploymentConfigConversionStatusPatch records why a requested conversion was skipped.
func DeploymentConfigConversionStatusPatch(u unstructured.Unstructured, reason string) (jsonpatch.Patch, error) {
	annotations := u.GetAnnotations()
	operations := make([]map[string]interface{}, 0, 2)
	if len(annotations) == 0 {
		operations = append(operations, map[string]interface{}{
			"op":   "add",
			"path": "/metadata/annotations",
			"value": map[string]string{
				DeploymentConfigConversionStatusAnnotation: "skipped",
				DeploymentConfigConversionReasonAnnotation: reason,
			},
		})
	} else {
		operations = append(operations,
			map[string]interface{}{
				"op":    "add",
				"path":  "/metadata/annotations/" + escapeJSONPointer(DeploymentConfigConversionStatusAnnotation),
				"value": "skipped",
			},
			map[string]interface{}{
				"op":    "add",
				"path":  "/metadata/annotations/" + escapeJSONPointer(DeploymentConfigConversionReasonAnnotation),
				"value": reason,
			},
		)
	}

	data, err := json.Marshal(operations)
	if err != nil {
		return nil, err
	}
	return jsonpatch.DecodePatch(data)
}

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}
