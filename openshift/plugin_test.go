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
			name: "ImageStream should be whitelisted and warn",
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
			name: "ImageStreamTag should be whitelisted without ImageStream warning",
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
			name: "ImageTag should be whitelisted without ImageStream warning",
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
			name: "Regular Pod should not be whitelisted",
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
		t.Error("expected ImageStream to be whitelisted")
	}

	logOutput := logBuffer.String()
	if !contains(logOutput, "ImageStream") {
		t.Errorf("expected ImageStream warning, got: %s", logOutput)
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
