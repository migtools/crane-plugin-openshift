package openshift

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestStripSecurityContext(t *testing.T) {
	tests := []struct {
		name        string
		resource    *unstructured.Unstructured
		expectPatch bool
		description string
	}{
		{
			name: "Strip SCC-injected runAsUser (>= 1000000000) from Pod",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "test-pod",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsUser": int64(1000560000),
							"fsGroup":   int64(1000560000),
							"seLinuxOptions": map[string]interface{}{
								"level": "s0:c26,c5",
							},
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "test-container",
								"image": "test:latest",
								"securityContext": map[string]interface{}{
									"runAsUser": int64(1000560000),
									"seLinuxOptions": map[string]interface{}{
										"level": "s0:c26,c5",
									},
								},
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should strip SCC-injected values (runAsUser, fsGroup >= 1000000000, seLinuxOptions.level)",
		},
		{
			name: "Keep user-configured runAsUser (< 1000000000) in Pod",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "test-pod",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsUser": int64(1001),
							"fsGroup":   int64(2000),
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "test-container",
								"image": "test:latest",
								"securityContext": map[string]interface{}{
									"runAsUser": int64(1001),
									"capabilities": map[string]interface{}{
										"drop": []interface{}{"ALL"},
									},
								},
							},
						},
					},
				},
			},
			expectPatch: false,
			description: "Should keep user-configured runAsUser < 1000000000 and other security settings",
		},
		{
			name: "Keep readOnlyRootFilesystem and other user settings in Deployment",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "test-deployment",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"securityContext": map[string]interface{}{
									"runAsUser": int64(1000560000),
									"fsGroup":   int64(2000),
								},
								"containers": []interface{}{
									map[string]interface{}{
										"name":  "test-container",
										"image": "test:latest",
										"securityContext": map[string]interface{}{
											"runAsUser":             int64(1000560000),
											"readOnlyRootFilesystem": true,
											"allowPrivilegeEscalation": false,
											"capabilities": map[string]interface{}{
												"drop": []interface{}{"ALL"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should strip SCC-injected runAsUser but keep readOnlyRootFilesystem and other user settings",
		},
		{
			name: "Strip seLinuxOptions.level but keep other seLinuxOptions",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "test-pod",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"seLinuxOptions": map[string]interface{}{
								"level": "s0:c26,c5",
								"type":  "spc_t",
								"user":  "system_u",
								"role":  "system_r",
							},
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "test-container",
								"image": "test:latest",
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should strip seLinuxOptions.level but keep user-configured type, user, role",
		},
		{
			name: "Handle mixed SCC and user values in StatefulSet",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "StatefulSet",
					"metadata": map[string]interface{}{
						"name":      "test-statefulset",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"securityContext": map[string]interface{}{
									"runAsUser":    int64(1000560000), // SCC-injected
									"fsGroup":      int64(1000560000), // SCC-injected
									"runAsNonRoot": true,              // user-configured
								},
								"containers": []interface{}{
									map[string]interface{}{
										"name":  "test-container",
										"image": "test:latest",
										"securityContext": map[string]interface{}{
											"runAsUser":                int64(1000560000), // SCC-injected
											"readOnlyRootFilesystem":   true,             // user-configured
											"allowPrivilegeEscalation": false,            // user-configured
										},
									},
								},
								"initContainers": []interface{}{
									map[string]interface{}{
										"name":  "init-container",
										"image": "init:latest",
										"securityContext": map[string]interface{}{
											"runAsUser": int64(1000560000), // SCC-injected
											"seLinuxOptions": map[string]interface{}{
												"level": "s0:c26,c5", // SCC-injected
												"type":  "init_t",    // user-configured
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should handle mixed SCC-injected and user-configured values across containers and initContainers",
		},
		{
			name: "Handle CronJob with nested template",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "batch/v1",
					"kind":       "CronJob",
					"metadata": map[string]interface{}{
						"name":      "test-cronjob",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"jobTemplate": map[string]interface{}{
							"spec": map[string]interface{}{
								"template": map[string]interface{}{
									"spec": map[string]interface{}{
										"securityContext": map[string]interface{}{
											"runAsUser": int64(1000560000),
											"fsGroup":   int64(1000560000),
										},
										"containers": []interface{}{
											map[string]interface{}{
												"name":  "test-container",
												"image": "test:latest",
												"securityContext": map[string]interface{}{
													"runAsUser": int64(1000560000),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should handle CronJob's nested jobTemplate structure",
		},
		{
			name: "Strip SCC-injected values from ephemeralContainers",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "test-pod-with-ephemeral",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "main-container",
								"image": "main:latest",
							},
						},
						"ephemeralContainers": []interface{}{
							map[string]interface{}{
								"name":  "debug-container",
								"image": "debug:latest",
								"securityContext": map[string]interface{}{
									"runAsUser": int64(1000560000), // SCC-injected
									"seLinuxOptions": map[string]interface{}{
										"level": "s0:c26,c5", // SCC-injected
										"type":  "spc_t",     // user-configured
									},
									"capabilities": map[string]interface{}{ // user-configured
										"add": []interface{}{"SYS_PTRACE"},
									},
								},
							},
						},
					},
				},
			},
			expectPatch: true,
			description: "Should strip SCC-injected values from ephemeralContainers while preserving user-configured fields",
		},
		{
			name: "Non-workload resource should not be processed",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Service",
					"metadata": map[string]interface{}{
						"name":      "test-service",
						"namespace": "test-ns",
					},
					"spec": map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{
								"port": 80,
							},
						},
					},
				},
			},
			expectPatch: false,
			description: "Should skip non-workload resources",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &OpenShiftTransformPlugin{
				Log: logrus.New(),
			}

			request := transform.PluginRequest{
				Unstructured: *tt.resource,
				Extras: map[string]string{
					StripDefaultPullSecretsFlag: "false",
				},
			}

			response, err := plugin.Run(request)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			hasPatch := len(response.Patches) > 0
			if hasPatch != tt.expectPatch {
				t.Errorf("%s: expected patch=%v, got %v (patches: %+v)", tt.description, tt.expectPatch, hasPatch, response.Patches)
			}

			// Apply patches and verify the result
			if tt.expectPatch && len(response.Patches) > 0 {
				originalJSON, err := tt.resource.MarshalJSON()
				if err != nil {
					t.Fatalf("failed to marshal original: %v", err)
				}

				modifiedJSON, err := response.Patches.Apply(originalJSON)
				if err != nil {
					t.Fatalf("failed to apply patches: %v", err)
				}

				// Unmarshal to verify structure
				var modified map[string]interface{}
				err = json.Unmarshal(modifiedJSON, &modified)
				if err != nil {
					t.Fatalf("failed to unmarshal modified: %v", err)
				}

				// Verify SCC-injected values were removed
				verifySecurityContext := func(sc interface{}, path string, isPodLevel bool) {
					if sc == nil {
						return
					}
					scMap, ok := sc.(map[string]interface{})
					if !ok {
						return
					}

					// Check runAsUser
					if runAsUser, ok := scMap["runAsUser"].(float64); ok {
						if int64(runAsUser) >= SCCNamespaceUIDMin {
							t.Errorf("%s at %s: SCC-injected runAsUser %d should have been stripped", tt.description, path, int64(runAsUser))
						}
					}

					// Check fsGroup (only valid at pod level)
					if isPodLevel {
						if fsGroup, ok := scMap["fsGroup"].(float64); ok {
							if int64(fsGroup) >= SCCNamespaceUIDMin {
								t.Errorf("%s at %s: SCC-injected fsGroup %d should have been stripped", tt.description, path, int64(fsGroup))
							}
						}
					}

					// Check seLinuxOptions.level
					if seLinuxOpts, ok := scMap["seLinuxOptions"].(map[string]interface{}); ok {
						if _, hasLevel := seLinuxOpts["level"]; hasLevel {
							t.Errorf("%s at %s: seLinuxOptions.level should have been stripped", tt.description, path)
						}
					}
				}

				// Get the appropriate spec path
				var spec map[string]interface{}
				var expectedPath string
				if tt.resource.GetKind() == "Pod" {
					spec, _ = modified["spec"].(map[string]interface{})
					expectedPath = "spec"
				} else if tt.resource.GetKind() == "CronJob" {
					jobTemplate, _ := modified["spec"].(map[string]interface{})
					jobSpec, _ := jobTemplate["jobTemplate"].(map[string]interface{})
					templateSpec, _ := jobSpec["spec"].(map[string]interface{})
					template, _ := templateSpec["template"].(map[string]interface{})
					spec, _ = template["spec"].(map[string]interface{})
					expectedPath = "spec.jobTemplate.spec.template.spec"
				} else {
					templateObj, _ := modified["spec"].(map[string]interface{})
					template, _ := templateObj["template"].(map[string]interface{})
					spec, _ = template["spec"].(map[string]interface{})
					expectedPath = "spec.template.spec"
				}

				// Validate that spec was found when patches are expected
				if spec == nil && tt.expectPatch {
					t.Fatalf("%s: expected to find pod spec at %s for kind %s after patch application, but spec is nil - patch may have failed or test data is incorrect",
						tt.description, expectedPath, tt.resource.GetKind())
				}

				if spec != nil {
					// Verify pod-level security context
					if podSC, ok := spec["securityContext"]; ok {
						verifySecurityContext(podSC, "spec.securityContext", true)
					}

					// Verify container security contexts
					if containers, ok := spec["containers"].([]interface{}); ok {
						for i, c := range containers {
							container, _ := c.(map[string]interface{})
							if containerSC, ok := container["securityContext"]; ok {
								verifySecurityContext(containerSC, fmt.Sprintf("spec.containers[%d].securityContext", i), false)
							}
						}
					}

					// Verify initContainer security contexts
					if initContainers, ok := spec["initContainers"].([]interface{}); ok {
						for i, c := range initContainers {
							container, _ := c.(map[string]interface{})
							if containerSC, ok := container["securityContext"]; ok {
								verifySecurityContext(containerSC, fmt.Sprintf("spec.initContainers[%d].securityContext", i), false)
							}
						}
					}

					// Verify ephemeralContainer security contexts
					if ephemeralContainers, ok := spec["ephemeralContainers"].([]interface{}); ok {
						for i, c := range ephemeralContainers {
							container, _ := c.(map[string]interface{})
							if containerSC, ok := container["securityContext"]; ok {
								verifySecurityContext(containerSC, fmt.Sprintf("spec.ephemeralContainers[%d].securityContext", i), false)
							}
						}
					}

					// Verify user-configured fields are preserved (positive assertions)
					// Check that fields which existed in the original and are not SCC-injected are still present
					verifyPreservedFields := func(origSpec, modSpec map[string]interface{}, pathPrefix string) {
						// Helper to check if a security context field is preserved.
						// modSC can be empty map (when entire securityContext is missing) - this triggers
						// proper validation errors for removed user-configured fields.
						checkPreserved := func(origSC, modSC map[string]interface{}, field, path string) {
							if origVal, hasOrig := origSC[field]; hasOrig {
								modVal, hasMod := modSC[field]
								if !hasMod {
									// Field was removed - check if it should have been preserved
									// Skip fields that might be legitimately removed (SCC-injected UIDs, etc.)
									switch field {
									case "runAsUser", "fsGroup":
										// Only complain if the value was < 1000000000 (user-configured)
										if val, ok := origVal.(int64); ok && val < SCCNamespaceUIDMin {
											t.Errorf("%s at %s: user-configured %s=%v was removed but should be preserved",
												tt.description, path, field, origVal)
										}
									case "seLinuxOptions":
										// Check individual seLinuxOptions fields
										if origOpts, ok := origVal.(map[string]interface{}); ok {
											for optField, optVal := range origOpts {
												if optField != "level" { // level is SCC-injected, always removed
													t.Errorf("%s at %s: user-configured seLinuxOptions.%s=%v was removed but should be preserved",
														tt.description, path, optField, optVal)
												}
											}
										}
									default:
										// All other fields should be preserved
										t.Errorf("%s at %s: user-configured %s=%v was removed but should be preserved",
											tt.description, path, field, origVal)
									}
									return
								}

								// Field exists; for seLinuxOptions validate nested preserved keys too.
								if field == "seLinuxOptions" {
									origOpts, ok1 := origVal.(map[string]interface{})
									modOpts, ok2 := modVal.(map[string]interface{})
									if ok1 && ok2 {
										for optField, optVal := range origOpts {
											if optField != "level" {
												if _, hasModOpt := modOpts[optField]; !hasModOpt {
													t.Errorf("%s at %s: user-configured seLinuxOptions.%s=%v was removed but should be preserved",
														tt.description, path, optField, optVal)
												}
											}
										}
									}
								}
							}
						}

						// Check pod-level security context
						if origPodSC, ok := origSpec["securityContext"].(map[string]interface{}); ok {
							if modPodSC, ok := modSpec["securityContext"].(map[string]interface{}); ok {
								for field := range origPodSC {
									checkPreserved(origPodSC, modPodSC, field, pathPrefix+".securityContext")
								}
							} else {
								// modPodSC is missing entirely - check if origPodSC had user-configured fields
								for field := range origPodSC {
									checkPreserved(origPodSC, map[string]interface{}{}, field, pathPrefix+".securityContext")
								}
							}
						}

						// Check container security contexts
						origContainers, _ := origSpec["containers"].([]interface{})
						modContainers, _ := modSpec["containers"].([]interface{})
						for i := 0; i < len(origContainers) && i < len(modContainers); i++ {
							origCont, _ := origContainers[i].(map[string]interface{})
							modCont, _ := modContainers[i].(map[string]interface{})
							if origSC, ok := origCont["securityContext"].(map[string]interface{}); ok {
								if modSC, ok := modCont["securityContext"].(map[string]interface{}); ok {
									for field := range origSC {
										checkPreserved(origSC, modSC, field, fmt.Sprintf("%s.containers[%d].securityContext", pathPrefix, i))
									}
								} else {
									// modSC is missing entirely - check if origSC had user-configured fields
									for field := range origSC {
										checkPreserved(origSC, map[string]interface{}{}, field, fmt.Sprintf("%s.containers[%d].securityContext", pathPrefix, i))
									}
								}
							}
						}

						// Check initContainer security contexts
						origInitContainers, _ := origSpec["initContainers"].([]interface{})
						modInitContainers, _ := modSpec["initContainers"].([]interface{})
						for i := 0; i < len(origInitContainers) && i < len(modInitContainers); i++ {
							origCont, _ := origInitContainers[i].(map[string]interface{})
							modCont, _ := modInitContainers[i].(map[string]interface{})
							if origSC, ok := origCont["securityContext"].(map[string]interface{}); ok {
								if modSC, ok := modCont["securityContext"].(map[string]interface{}); ok {
									for field := range origSC {
										checkPreserved(origSC, modSC, field, fmt.Sprintf("%s.initContainers[%d].securityContext", pathPrefix, i))
									}
								} else {
									// modSC is missing entirely - check if origSC had user-configured fields
									for field := range origSC {
										checkPreserved(origSC, map[string]interface{}{}, field, fmt.Sprintf("%s.initContainers[%d].securityContext", pathPrefix, i))
									}
								}
							}
						}

						// Check ephemeralContainer security contexts
						origEphemeralContainers, _ := origSpec["ephemeralContainers"].([]interface{})
						modEphemeralContainers, _ := modSpec["ephemeralContainers"].([]interface{})
						for i := 0; i < len(origEphemeralContainers) && i < len(modEphemeralContainers); i++ {
							origCont, _ := origEphemeralContainers[i].(map[string]interface{})
							modCont, _ := modEphemeralContainers[i].(map[string]interface{})
							if origSC, ok := origCont["securityContext"].(map[string]interface{}); ok {
								if modSC, ok := modCont["securityContext"].(map[string]interface{}); ok {
									for field := range origSC {
										checkPreserved(origSC, modSC, field, fmt.Sprintf("%s.ephemeralContainers[%d].securityContext", pathPrefix, i))
									}
								} else {
									// modSC is missing entirely - check if origSC had user-configured fields
									for field := range origSC {
										checkPreserved(origSC, map[string]interface{}{}, field, fmt.Sprintf("%s.ephemeralContainers[%d].securityContext", pathPrefix, i))
									}
								}
							}
						}
					}

					// Get original spec for comparison
					var origSpec map[string]interface{}
					if tt.resource.GetKind() == "Pod" {
						origSpec, _ = tt.resource.Object["spec"].(map[string]interface{})
						verifyPreservedFields(origSpec, spec, "spec")
					} else if tt.resource.GetKind() == "CronJob" {
						jobTemplate, _ := tt.resource.Object["spec"].(map[string]interface{})
						jobSpec, _ := jobTemplate["jobTemplate"].(map[string]interface{})
						templateSpec, _ := jobSpec["spec"].(map[string]interface{})
						template, _ := templateSpec["template"].(map[string]interface{})
						origSpec, _ = template["spec"].(map[string]interface{})
						verifyPreservedFields(origSpec, spec, "spec.jobTemplate.spec.template.spec")
					} else {
						templateObj, _ := tt.resource.Object["spec"].(map[string]interface{})
						template, _ := templateObj["template"].(map[string]interface{})
						origSpec, _ = template["spec"].(map[string]interface{})
						verifyPreservedFields(origSpec, spec, "spec.template.spec")
					}
				}
			}
		})
	}
}
