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

// The sandboxes run the factory the overseer was built with: this
// project's factory image at :latest, untagged or unset is pinned to the
// overseer's own tag; anything else is left as given.
func TestFactoryImageIsPinnedToTheOverseersBuild(t *testing.T) {
	const sha = "8e847a75fd7382f65514dd85334c17eae7963656"
	overseer := "ghcr.io/gke-labs/gemini-for-kubernetes-development/overseer:" + sha
	for _, tc := range []struct{ spec, overseer, want string }{
		{factoryRepo + ":latest", overseer, factoryRepo + ":" + sha},
		{factoryRepo, overseer, factoryRepo + ":" + sha},
		{"", overseer, factoryRepo + ":" + sha},
		{factoryRepo + ":abc123", overseer, factoryRepo + ":abc123"},
		{"registry.example/my-factory:latest", overseer, "registry.example/my-factory:latest"},
		{factoryRepo + "@sha256:deadbeef", overseer, factoryRepo + "@sha256:deadbeef"},
		// An unpinned overseer pins nothing.
		{factoryRepo + ":latest", "ghcr.io/gke-labs/gemini-for-kubernetes-development/overseer:latest", factoryRepo + ":latest"},
		{"", "", ""},
		{"", "localhost:5000/overseer", ""},
	} {
		if got := factoryImage(tc.spec, tc.overseer); got != tc.want {
			t.Errorf("factoryImage(%q, %q) = %q, want %q", tc.spec, tc.overseer, got, tc.want)
		}
	}
}

// The manifest carries the pinned image as FACTORY_IMAGE, from the
// controller's OVERSEER_IMAGE.
func TestBuildSandboxManifest_FactoryImage(t *testing.T) {
	t.Setenv("OVERSEER_IMAGE", "ghcr.io/gke-labs/gemini-for-kubernetes-development/overseer:abc")
	env := overseerEnv(t, &overseerv1alpha1.Overseer{
		Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo", Image: factoryRepo + ":latest"},
	})
	if got := env["FACTORY_IMAGE"]; got != factoryRepo+":abc" {
		t.Errorf("FACTORY_IMAGE = %v, want %s:abc", got, factoryRepo)
	}
}

func TestBuildSandboxManifest_SessionURL(t *testing.T) {
	const tmpl = "https://board.example/#/overseers/kcc/task-session/{sandbox}/{task}"
	env := overseerEnv(t, &overseerv1alpha1.Overseer{
		Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"},
	})
	if v, ok := env["SESSION_URL"]; ok {
		t.Errorf("unset: SESSION_URL = %v, want none", v)
	}
	env = overseerEnv(t, &overseerv1alpha1.Overseer{
		Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo", SessionURL: tmpl},
	})
	if got := env["SESSION_URL"]; got != tmpl {
		t.Errorf("SESSION_URL = %v, want %q", got, tmpl)
	}
}

// spec.sharedScan reaches the watcher as WATCH_SHARED_SCAN, and only when set.
func TestBuildSandboxManifest_SharedScan(t *testing.T) {
	o := &overseerv1alpha1.Overseer{Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"}}
	if _, ok := overseerEnv(t, o)["WATCH_SHARED_SCAN"]; ok {
		t.Error("WATCH_SHARED_SCAN is set with sharedScan unset")
	}
	o.Spec.SharedScan = true
	if got := overseerEnv(t, o)["WATCH_SHARED_SCAN"]; got != "true" {
		t.Errorf("WATCH_SHARED_SCAN = %v, want true", got)
	}
}

// spec.rules.engine reaches the watcher as WATCH_RULES_ENGINE, and only
// when it is not off.
func TestBuildSandboxManifest_RulesEngine(t *testing.T) {
	o := &overseerv1alpha1.Overseer{Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"}}
	o.Spec.Rules = &overseerv1alpha1.OverseerRules{}
	if _, ok := overseerEnv(t, o)["WATCH_RULES_DISABLED"]; ok {
		t.Error("WATCH_RULES_DISABLED is set with nothing disabled")
	}
	o.Spec.Rules = &overseerv1alpha1.OverseerRules{Disabled: []string{"review", "inactive"}}
	env := overseerEnv(t, o)
	if env["WATCH_RULES_DISABLED"] != "review,inactive" {
		t.Errorf("WATCH_RULES_DISABLED = %v", env["WATCH_RULES_DISABLED"])
	}
	for _, gone := range []string{"WATCH_RULES_ENGINE", "WATCH_RULES_USE", "WATCH_RULES_CANARY_LABEL", "WATCH_RULES_CANARY_PERCENT"} {
		if _, ok := env[gone]; ok {
			t.Errorf("%s is set: %v", gone, env)
		}
	}
}

// spec.rulesRecord reaches the watcher as WATCH_RULES_RECORD, a file on
// the overseer's volume, and only when set.
func TestBuildSandboxManifest_RulesRecord(t *testing.T) {
	o := &overseerv1alpha1.Overseer{Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"}}
	if _, ok := overseerEnv(t, o)["WATCH_RULES_RECORD"]; ok {
		t.Error("WATCH_RULES_RECORD is set with rulesRecord unset")
	}
	o.Spec.RulesRecord = true
	if got := overseerEnv(t, o)["WATCH_RULES_RECORD"]; got != "/workspaces/.cache/factory-watch/decisions.jsonl" {
		t.Errorf("WATCH_RULES_RECORD = %v", got)
	}
}

// spec.graphqlPRDetails reaches the watcher as WATCH_GRAPHQL_PR_DETAILS, and
// only when set.
func TestBuildSandboxManifest_GraphQLPRDetails(t *testing.T) {
	o := &overseerv1alpha1.Overseer{Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"}}
	if _, ok := overseerEnv(t, o)["WATCH_GRAPHQL_PR_DETAILS"]; ok {
		t.Error("WATCH_GRAPHQL_PR_DETAILS is set with graphqlPRDetails unset")
	}
	o.Spec.GraphQLPRDetails = true
	if got := overseerEnv(t, o)["WATCH_GRAPHQL_PR_DETAILS"]; got != "true" {
		t.Errorf("WATCH_GRAPHQL_PR_DETAILS = %v, want true", got)
	}
}

// spec.repoRecipe reaches the watcher, and the factory commands it runs,
// as FACTORY_REPO_RECIPE_MODE and FACTORY_REPO_RECIPE_COMMIT (latest when
// unset), and only when its mode is not off.
func TestBuildSandboxManifest_RepoRecipe(t *testing.T) {
	o := &overseerv1alpha1.Overseer{Spec: overseerv1alpha1.OverseerSpec{RepoURL: "https://github.com/owner/repo"}}
	for _, r := range []*overseerv1alpha1.OverseerRepoRecipe{nil, {}, {Mode: "off", GitCommit: "abc1234"}} {
		o.Spec.RepoRecipe = r
		env := overseerEnv(t, o)
		if _, ok := env["FACTORY_REPO_RECIPE_MODE"]; ok {
			t.Errorf("repoRecipe %+v: mode set: %v", r, env)
		}
		if _, ok := env["FACTORY_REPO_RECIPE_COMMIT"]; ok {
			t.Errorf("repoRecipe %+v: commit set: %v", r, env)
		}
	}
	o.Spec.RepoRecipe = &overseerv1alpha1.OverseerRepoRecipe{Mode: "augment"}
	if env := overseerEnv(t, o); env["FACTORY_REPO_RECIPE_MODE"] != "augment" || env["FACTORY_REPO_RECIPE_COMMIT"] != "latest" {
		t.Errorf("augment: %v %v", env["FACTORY_REPO_RECIPE_MODE"], env["FACTORY_REPO_RECIPE_COMMIT"])
	}
	o.Spec.RepoRecipe = &overseerv1alpha1.OverseerRepoRecipe{Mode: "replace", GitCommit: "0123abcd"}
	if env := overseerEnv(t, o); env["FACTORY_REPO_RECIPE_MODE"] != "replace" || env["FACTORY_REPO_RECIPE_COMMIT"] != "0123abcd" || env["FACTORY_REPO_RECIPE_DIR"] != "/workspaces/repo" {
		t.Errorf("replace: %v %v", env["FACTORY_REPO_RECIPE_MODE"], env["FACTORY_REPO_RECIPE_COMMIT"])
	}
}
