package overseer

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// overseerEnv returns the overseer container's env as a name -> value map.
func overseerEnv(t *testing.T, o *overseerv1alpha1.Overseer) map[string]interface{} {
	t.Helper()
	manifest := newOverseerSandboxFromOverseer(o, "test-overseer", "overseer-system", false)
	containers, found, err := unstructured.NestedSlice(manifest.Object, "spec", "podTemplate", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		t.Fatalf("Failed to find containers in manifest: %v", err)
	}
	envs, found, err := unstructured.NestedSlice(containers[0].(map[string]interface{}), "env")
	if err != nil || !found {
		t.Fatalf("Failed to find env in container: %v", err)
	}
	out := make(map[string]interface{})
	for _, envEntry := range envs {
		e := envEntry.(map[string]interface{})
		out[e["name"].(string)] = e["value"]
	}
	return out
}

func TestBuildSandboxManifest_AllowlistedUsers(t *testing.T) {
	t.Run("unset omits ALLOWLISTED_USERS", func(t *testing.T) {
		env := overseerEnv(t, &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"},
		})
		if v, ok := env["ALLOWLISTED_USERS"]; ok {
			t.Errorf("expected ALLOWLISTED_USERS to not be set by default, got: %v", v)
		}
	})

	t.Run("set passes a comma-separated ALLOWLISTED_USERS", func(t *testing.T) {
		env := overseerEnv(t, &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL:          "https://github.com/owner/repo",
				AllowlistedUsers: []string{"alice", "bob"},
			},
		})
		if got := env["ALLOWLISTED_USERS"]; got != "alice,bob" {
			t.Errorf("ALLOWLISTED_USERS = %v, want %q", got, "alice,bob")
		}
	})
}

func TestBuildSandboxManifest_WarmWorkspace(t *testing.T) {
	t.Run("unset omits WARM_WORKSPACE_*", func(t *testing.T) {
		env := overseerEnv(t, &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"},
		})
		for name := range env {
			if strings.HasPrefix(name, "WARM_WORKSPACE_") {
				t.Errorf("%s is set without warmWorkspace", name)
			}
		}
	})

	t.Run("set passes the interval, keep and the one script", func(t *testing.T) {
		keep := int32(3)
		env := overseerEnv(t, &overseerv1alpha1.Overseer{
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL: "https://github.com/owner/repo",
				WarmWorkspace: &overseerv1alpha1.WarmWorkspaceSpec{
					Interval:   metav1.Duration{Duration: 24 * time.Hour},
					Keep:       &keep,
					ScriptPath: "dev/tasks/warm-workspace",
				},
			},
		})
		want := map[string]interface{}{
			"WARM_WORKSPACE_INTERVAL":    "24h0m0s",
			"WARM_WORKSPACE_KEEP":        "3",
			"WARM_WORKSPACE_SCRIPT_PATH": "dev/tasks/warm-workspace",
		}
		for name, v := range want {
			if env[name] != v {
				t.Errorf("%s = %v, want %q", name, env[name], v)
			}
		}
		for _, name := range []string{"WARM_WORKSPACE_SCRIPT", "WARM_WORKSPACE_SCRIPT_URL"} {
			if v, ok := env[name]; ok {
				t.Errorf("%s = %v, want unset", name, v)
			}
		}
	})
}
