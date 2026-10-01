package overseer

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
)

func TestBuildSandboxManifest_WorkspaceStorageClass(t *testing.T) {
	t.Run("default options omit storageClassName and WORKSPACE_STORAGE_CLASS env", func(t *testing.T) {
		o := &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL: "https://github.com/owner/repo",
			},
		}

		manifest := newOverseerSandboxFromOverseer(o, "test-overseer", "overseer-system", false)

		// 1. Verify WORKSPACE_STORAGE_CLASS env var is not set
		containers, found, err := unstructured.NestedSlice(manifest.Object, "spec", "podTemplate", "spec", "containers")
		if err != nil || !found || len(containers) == 0 {
			t.Fatalf("Failed to find containers in manifest: %v", err)
		}
		container := containers[0].(map[string]interface{})
		envs, found, err := unstructured.NestedSlice(container, "env")
		if err != nil || !found {
			t.Fatalf("Failed to find env in container: %v", err)
		}
		for _, envEntry := range envs {
			e := envEntry.(map[string]interface{})
			if e["name"] == "WORKSPACE_STORAGE_CLASS" {
				t.Errorf("expected WORKSPACE_STORAGE_CLASS to not be set by default, got: %v", e["value"])
			}
		}

		// 2. Verify volumeClaimTemplates has no storageClassName
		pvcs, found, err := unstructured.NestedSlice(manifest.Object, "spec", "volumeClaimTemplates")
		if err != nil || !found || len(pvcs) == 0 {
			t.Fatalf("Failed to find volumeClaimTemplates in manifest: %v", err)
		}
		pvcSpec, ok := pvcs[0].(map[string]interface{})["spec"].(map[string]interface{})
		if !ok {
			t.Fatalf("Invalid PVC spec structure")
		}
		if sc, exists := pvcSpec["storageClassName"]; exists {
			t.Errorf("expected storageClassName to be omitted by default, got: %v", sc)
		}
	})

	t.Run("explicit workspaceStorageClassName sets storageClassName and WORKSPACE_STORAGE_CLASS env", func(t *testing.T) {
		o := &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL:                   "https://github.com/owner/repo",
				WorkspaceDiskSize:         "40Gi",
				WorkspaceStorageClassName: "premium-rwo",
			},
		}

		manifest := newOverseerSandboxFromOverseer(o, "test-overseer", "overseer-system", false)

		// 1. Verify WORKSPACE_STORAGE_CLASS env var is set
		containers, found, err := unstructured.NestedSlice(manifest.Object, "spec", "podTemplate", "spec", "containers")
		if err != nil || !found || len(containers) == 0 {
			t.Fatalf("Failed to find containers in manifest: %v", err)
		}
		container := containers[0].(map[string]interface{})
		envs, found, err := unstructured.NestedSlice(container, "env")
		if err != nil || !found {
			t.Fatalf("Failed to find env in container: %v", err)
		}
		foundStorageClassEnv := false
		for _, envEntry := range envs {
			e := envEntry.(map[string]interface{})
			if e["name"] == "WORKSPACE_STORAGE_CLASS" {
				foundStorageClassEnv = true
				if e["value"] != "premium-rwo" {
					t.Errorf("expected WORKSPACE_STORAGE_CLASS to be 'premium-rwo', got: %v", e["value"])
				}
			}
		}
		if !foundStorageClassEnv {
			t.Errorf("expected WORKSPACE_STORAGE_CLASS env var to be present")
		}

		// 2. Verify volumeClaimTemplates has storageClassName
		pvcs, found, err := unstructured.NestedSlice(manifest.Object, "spec", "volumeClaimTemplates")
		if err != nil || !found || len(pvcs) == 0 {
			t.Fatalf("Failed to find volumeClaimTemplates in manifest: %v", err)
		}
		pvcSpec, ok := pvcs[0].(map[string]interface{})["spec"].(map[string]interface{})
		if !ok {
			t.Fatalf("Invalid PVC spec structure")
		}
		if sc, ok := pvcSpec["storageClassName"].(string); !ok || sc != "premium-rwo" {
			t.Errorf("expected storageClassName 'premium-rwo', got: %v", pvcSpec["storageClassName"])
		}
	})
}
