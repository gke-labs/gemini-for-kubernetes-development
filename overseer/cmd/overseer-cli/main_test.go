package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestInjectEnvVar(t *testing.T) {
	sb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name": "main",
								"env": []interface{}{
									map[string]interface{}{
										"name":  "EXISTING",
										"value": "value",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := injectEnvVar(sb, "NEW_VAR", "new_value")
	if err != nil {
		t.Fatalf("injectEnvVar failed: %v", err)
	}

	// Verify
	containers, _, _ := unstructured.NestedSlice(sb.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]interface{})
	env, _, _ := unstructured.NestedSlice(container, "env")

	found := false
	for _, e := range env {
		envVar := e.(map[string]interface{})
		if envVar["name"] == "NEW_VAR" && envVar["value"] == "new_value" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("NEW_VAR not found in env")
	}
}
