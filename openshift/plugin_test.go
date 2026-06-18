package openshift

import (
	"bytes"
	"testing"

	"github.com/konveyor/crane-lib/transform"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestImageStreamDetection(t *testing.T) {
	tests := []struct {
		name              string
		resource          *unstructured.Unstructured
		expectWhiteOut    bool
		expectWarning     bool
		warningContains   string
	}{
		{
			name: "ImageStream should be marked as whiteout and warn",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "image.openshift.io/v1",
					"kind":       "ImageStream",
					"metadata": map[string]interface{}{
						"name":      "my-app",
						"namespace": "my-namespace",
					},
					"spec": map[string]interface{}{
						"lookupPolicy": map[string]interface{}{
							"local": false,
						},
					},
				},
			},
			expectWhiteOut:  true,
			expectWarning:   true,
			warningContains: "my-namespace/my-app",
		},
		{
			name: "ImageStreamTag should be marked as whiteout without ImageStream warning",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "image.openshift.io/v1",
					"kind":       "ImageStreamTag",
					"metadata": map[string]interface{}{
						"name":      "my-app:latest",
						"namespace": "my-namespace",
					},
				},
			},
			expectWhiteOut:  true,
			expectWarning:   false,
		},
		{
			name: "ImageTag should be marked as whiteout without ImageStream warning",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "image.openshift.io/v1",
					"kind":       "ImageTag",
					"metadata": map[string]interface{}{
						"name":      "sha256:abc123",
						"namespace": "my-namespace",
					},
				},
			},
			expectWhiteOut:  true,
			expectWarning:   false,
		},
		{
			name: "Regular Pod should not be marked as whiteout",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "my-namespace",
					},
				},
			},
			expectWhiteOut:  false,
			expectWarning:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer to capture log output
			var logBuffer bytes.Buffer
			logger := logrus.New()
			logger.SetOutput(&logBuffer)

			plugin := &OpenShiftTransformPlugin{
				Log: logger,
			}

			request := transform.PluginRequest{
				Unstructured: *tt.resource,
				Extras:       map[string]string{},
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if response.IsWhiteOut != tt.expectWhiteOut {
				t.Errorf("expected IsWhiteOut=%v, got %v", tt.expectWhiteOut, response.IsWhiteOut)
			}

			logOutput := logBuffer.String()
			if tt.expectWarning {
				if logOutput == "" {
					t.Error("expected warning log but got no output")
				}
				if tt.warningContains != "" && !contains(logOutput, tt.warningContains) {
					t.Errorf("expected log to contain '%s', got: %s", tt.warningContains, logOutput)
				}
				if !contains(logOutput, "NOT migrated automatically") {
					t.Errorf("expected warning about migration, got: %s", logOutput)
				}
				if !contains(logOutput, "skopeo") {
					t.Errorf("expected log to mention skopeo tool, got: %s", logOutput)
				}
			} else {
				if contains(logOutput, "NOT migrated automatically") {
					t.Errorf("unexpected ImageStream warning for %s: %s", tt.resource.GetKind(), logOutput)
				}
			}
		})
	}
}

func TestImageStreamWithEmptyNamespace(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logBuffer)

	plugin := &OpenShiftTransformPlugin{
		Log: logger,
	}

	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "image.openshift.io/v1",
			"kind":       "ImageStream",
			"metadata": map[string]interface{}{
				"name": "my-app",
				// namespace intentionally omitted
			},
		},
	}

	request := transform.PluginRequest{
		Unstructured: *resource,
		Extras:       map[string]string{},
	}

	response, err := plugin.Run(request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !response.IsWhiteOut {
		t.Error("expected ImageStream to be marked as whiteout")
	}

	logOutput := logBuffer.String()
	if !contains(logOutput, "ImageStream") {
		t.Errorf("expected ImageStream warning, got: %s", logOutput)
	}
}

func TestConfigMapFiltering(t *testing.T) {
	tests := []struct {
		name            string
		configMapName   string
		extras          map[string]string
		expectWhiteOut  bool
		description     string
	}{
		{
			name:           "openshift-service-ca.crt should be filtered by default",
			configMapName:  "openshift-service-ca.crt",
			extras:         map[string]string{},
			expectWhiteOut: true,
			description:    "Default behavior strips openshift-service-ca.crt ConfigMap",
		},
		{
			name:           "openshift-service-ca.crt with explicit strip-default-cabundle=true",
			configMapName:  "openshift-service-ca.crt",
			extras:         map[string]string{StripDefaultCABundleFlag: "true"},
			expectWhiteOut: true,
			description:    "Explicitly enabled flag strips the ConfigMap",
		},
		{
			name:           "openshift-service-ca.crt with strip-default-cabundle=false",
			configMapName:  "openshift-service-ca.crt",
			extras:         map[string]string{StripDefaultCABundleFlag: "false"},
			expectWhiteOut: false,
			description:    "Disabled flag allows the ConfigMap through",
		},
		{
			name:           "other ConfigMap should not be filtered",
			configMapName:  "my-custom-config",
			extras:         map[string]string{},
			expectWhiteOut: false,
			description:    "User ConfigMaps are not affected by the filter",
		},
		{
			name:           "kube-root-ca.crt should not be filtered by this plugin",
			configMapName:  "kube-root-ca.crt",
			extras:         map[string]string{},
			expectWhiteOut: false,
			description:    "Kubernetes CA bundle is handled by KubernetesPlugin, not OpenShiftPlugin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &OpenShiftTransformPlugin{
				Log: logrus.New(),
			}

			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      tt.configMapName,
						"namespace": "test-namespace",
					},
					"data": map[string]interface{}{
						"service-ca.crt": "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
					},
				},
			}

			request := transform.PluginRequest{
				Unstructured: *resource,
				Extras:       tt.extras,
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if response.IsWhiteOut != tt.expectWhiteOut {
				t.Errorf("%s: expected IsWhiteOut=%v, got %v", tt.description, tt.expectWhiteOut, response.IsWhiteOut)
			}
		})
	}
}

func TestRoleBindingFiltering(t *testing.T) {
	tests := []struct {
		name           string
		roleBindingName string
		extras         map[string]string
		expectWhiteOut bool
		description    string
	}{
		{
			name:            "system:deployers should be filtered by default",
			roleBindingName: "system:deployers",
			extras:          map[string]string{},
			expectWhiteOut:  true,
			description:     "Default behavior strips system:deployers RoleBinding",
		},
		{
			name:            "system:image-builders should be filtered by default",
			roleBindingName: "system:image-builders",
			extras:          map[string]string{},
			expectWhiteOut:  true,
			description:     "Default behavior strips system:image-builders RoleBinding",
		},
		{
			name:            "system:image-pullers should be filtered by default",
			roleBindingName: "system:image-pullers",
			extras:          map[string]string{},
			expectWhiteOut:  true,
			description:     "Default behavior strips system:image-pullers RoleBinding",
		},
		{
			name:            "admin should be filtered by default",
			roleBindingName: "admin",
			extras:          map[string]string{},
			expectWhiteOut:  true,
			description:     "Default behavior strips admin RoleBinding",
		},
		{
			name:            "system:deployers with explicit strip-default-rbac=true",
			roleBindingName: "system:deployers",
			extras:          map[string]string{StripDefaultRBACFlag: "true"},
			expectWhiteOut:  true,
			description:     "Explicitly enabled flag strips the RoleBinding",
		},
		{
			name:            "system:deployers with strip-default-rbac=false",
			roleBindingName: "system:deployers",
			extras:          map[string]string{StripDefaultRBACFlag: "false"},
			expectWhiteOut:  false,
			description:     "Disabled flag allows the RoleBinding through",
		},
		{
			name:            "custom RoleBinding should not be filtered",
			roleBindingName: "my-custom-role",
			extras:          map[string]string{},
			expectWhiteOut:  false,
			description:     "User RoleBindings are not affected by the filter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &OpenShiftTransformPlugin{
				Log: logrus.New(),
			}

			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "rbac.authorization.k8s.io/v1",
					"kind":       "RoleBinding",
					"metadata": map[string]interface{}{
						"name":      tt.roleBindingName,
						"namespace": "test-namespace",
					},
					"roleRef": map[string]interface{}{
						"apiGroup": "rbac.authorization.k8s.io",
						"kind":     "ClusterRole",
						"name":     "edit",
					},
					"subjects": []interface{}{
						map[string]interface{}{
							"kind":      "ServiceAccount",
							"name":      "default",
							"namespace": "test-namespace",
						},
					},
				},
			}

			request := transform.PluginRequest{
				Unstructured: *resource,
				Extras:       tt.extras,
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if response.IsWhiteOut != tt.expectWhiteOut {
				t.Errorf("%s: expected IsWhiteOut=%v, got %v", tt.description, tt.expectWhiteOut, response.IsWhiteOut)
			}
		})
	}
}

func TestServiceAccountFiltering(t *testing.T) {
	tests := []struct {
		name               string
		serviceAccountName string
		extras             map[string]string
		expectWhiteOut     bool
		description        string
	}{
		{
			name:               "builder should be filtered by default",
			serviceAccountName: "builder",
			extras:             map[string]string{},
			expectWhiteOut:     true,
			description:        "Default behavior strips builder ServiceAccount",
		},
		{
			name:               "deployer should be filtered by default",
			serviceAccountName: "deployer",
			extras:             map[string]string{},
			expectWhiteOut:     true,
			description:        "Default behavior strips deployer ServiceAccount",
		},
		{
			name:               "builder with explicit strip-default-rbac=true",
			serviceAccountName: "builder",
			extras:             map[string]string{StripDefaultRBACFlag: "true"},
			expectWhiteOut:     true,
			description:        "Explicitly enabled flag strips the ServiceAccount",
		},
		{
			name:               "builder with strip-default-rbac=false",
			serviceAccountName: "builder",
			extras:             map[string]string{StripDefaultRBACFlag: "false"},
			expectWhiteOut:     false,
			description:        "Disabled flag allows the ServiceAccount through",
		},
		{
			name:               "default ServiceAccount should not be filtered",
			serviceAccountName: "default",
			extras:             map[string]string{},
			expectWhiteOut:     false,
			description:        "Default ServiceAccount is not filtered",
		},
		{
			name:               "custom ServiceAccount should not be filtered",
			serviceAccountName: "my-app-sa",
			extras:             map[string]string{},
			expectWhiteOut:     false,
			description:        "User ServiceAccounts are not affected by the filter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &OpenShiftTransformPlugin{
				Log: logrus.New(),
			}

			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ServiceAccount",
					"metadata": map[string]interface{}{
						"name":      tt.serviceAccountName,
						"namespace": "test-namespace",
					},
				},
			}

			request := transform.PluginRequest{
				Unstructured: *resource,
				Extras:       tt.extras,
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if response.IsWhiteOut != tt.expectWhiteOut {
				t.Errorf("%s: expected IsWhiteOut=%v, got %v", tt.description, tt.expectWhiteOut, response.IsWhiteOut)
			}
		})
	}
}

func TestSecretFiltering(t *testing.T) {
	tests := []struct {
		name           string
		secretName     string
		annotations    map[string]interface{}
		extras         map[string]string
		expectWhiteOut bool
		description    string
	}{
		{
			name:       "builder secret should be filtered by default",
			secretName: "builder-token-abcde",
			annotations: map[string]interface{}{
				"kubernetes.io/service-account.name": "builder",
			},
			extras:         map[string]string{},
			expectWhiteOut: true,
			description:    "Default behavior strips builder ServiceAccount secrets",
		},
		{
			name:       "deployer secret should be filtered by default",
			secretName: "deployer-token-xyz12",
			annotations: map[string]interface{}{
				"kubernetes.io/service-account.name": "deployer",
			},
			extras:         map[string]string{},
			expectWhiteOut: true,
			description:    "Default behavior strips deployer ServiceAccount secrets",
		},
		{
			name:       "pipeline secret should be filtered by default",
			secretName: "pipeline-token-fgh78",
			annotations: map[string]interface{}{
				"kubernetes.io/service-account.name": "pipeline",
			},
			extras:         map[string]string{},
			expectWhiteOut: true,
			description:    "Default behavior strips pipeline ServiceAccount secrets",
		},
		{
			name:       "builder secret with strip-default-rbac=false",
			secretName: "builder-token-abcde",
			annotations: map[string]interface{}{
				"kubernetes.io/service-account.name": "builder",
			},
			extras:         map[string]string{StripDefaultRBACFlag: "false"},
			expectWhiteOut: false,
			description:    "Disabled flag allows the secret through",
		},
		{
			name:       "default ServiceAccount secret should not be filtered",
			secretName: "default-token-xyz12",
			annotations: map[string]interface{}{
				"kubernetes.io/service-account.name": "default",
			},
			extras:         map[string]string{},
			expectWhiteOut: false,
			description:    "Default ServiceAccount secrets are not filtered",
		},
		{
			name:       "custom secret should not be filtered",
			secretName: "my-app-secret",
			annotations: map[string]interface{}{
				"myapp.io/purpose": "configuration",
			},
			extras:         map[string]string{},
			expectWhiteOut: false,
			description:    "User secrets are not affected by the filter",
		},
		{
			name:           "secret without annotations should not be filtered",
			secretName:     "some-secret",
			annotations:    nil,
			extras:         map[string]string{},
			expectWhiteOut: false,
			description:    "Secrets without service-account annotation are not filtered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &OpenShiftTransformPlugin{
				Log: logrus.New(),
			}

			metadata := map[string]interface{}{
				"name":      tt.secretName,
				"namespace": "test-namespace",
			}
			if tt.annotations != nil {
				metadata["annotations"] = tt.annotations
			}

			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Secret",
					"metadata":   metadata,
					"type":       "kubernetes.io/service-account-token",
					"data": map[string]interface{}{
						"token": "ZXhhbXBsZS10b2tlbg==",
					},
				},
			}

			request := transform.PluginRequest{
				Unstructured: *resource,
				Extras:       tt.extras,
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if response.IsWhiteOut != tt.expectWhiteOut {
				t.Errorf("%s: expected IsWhiteOut=%v, got %v", tt.description, tt.expectWhiteOut, response.IsWhiteOut)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
