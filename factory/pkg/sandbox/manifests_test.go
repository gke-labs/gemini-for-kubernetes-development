package sandbox

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAgentSandbox_ResourceLimitsAndRequests(t *testing.T) {
	customOpt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:              "fix-k8s-config-connector-12571",
			Namespace:         "overseer-kcc",
			CPURequest:        "2000m",
			CPULimit:          "8000m",
			MemoryRequest:     "4Gi",
			MemoryLimit:       "16Gi",
			EphemeralStorage:  "10Gi",
			WorkspaceDiskSize: "40Gi",
		},
	}

	customSB, _ := NewAgentSandbox(customOpt)
	customContainers, found, err := unstructured.NestedSlice(customSB.Object, "spec", "podTemplate", "spec", "containers")
	if err != nil || !found || len(customContainers) == 0 {
		t.Fatalf("Failed to find containers in custom sandbox spec: %v", err)
	}

	customContainer := customContainers[0].(map[string]interface{})
	customRes := customContainer["resources"].(map[string]interface{})
	customReq := customRes["requests"].(map[string]interface{})
	customLim := customRes["limits"].(map[string]interface{})

	if got := customReq["memory"]; got != "4Gi" {
		t.Errorf("custom memory request = %v, want 4Gi", got)
	}
	if got := customReq["cpu"]; got != "2" {
		t.Errorf("custom cpu request = %v, want 2", got)
	}
	if got := customReq["ephemeral-storage"]; got != "10Gi" {
		t.Errorf("custom ephemeral-storage request = %v, want 10Gi", got)
	}

	if got := customLim["memory"]; got != "16Gi" {
		t.Errorf("custom memory limit = %v, want 16Gi", got)
	}
	if got := customLim["cpu"]; got != "8" {
		t.Errorf("custom cpu limit = %v, want 8", got)
	}
	if got := customLim["ephemeral-storage"]; got != "10Gi" {
		t.Errorf("custom ephemeral-storage limit = %v, want 10Gi", got)
	}
}

func TestSandbox_WorkspaceStorageClass(t *testing.T) {
	t.Run("default options omit storageClassName", func(t *testing.T) {
		opt := AgentSandboxOptions{
			DevSandboxOptions: DevSandboxOptions{
				Name:      "test-agent",
				Namespace: "default",
			},
		}
		sb, _ := NewAgentSandbox(opt)
		pvcs, found, err := unstructured.NestedSlice(sb.Object, "spec", "volumeClaimTemplates")
		if err != nil || !found || len(pvcs) == 0 {
			t.Fatalf("Failed to find volumeClaimTemplates: %v", err)
		}
		pvcSpec, ok := pvcs[0].(map[string]interface{})["spec"].(map[string]interface{})
		if !ok {
			t.Fatalf("Invalid PVC spec structure")
		}
		if sc, exists := pvcSpec["storageClassName"]; exists {
			t.Errorf("expected storageClassName to be omitted by default, got: %v", sc)
		}

	})

	t.Run("explicit storageClassName is set on PVC template", func(t *testing.T) {
		opt := AgentSandboxOptions{
			DevSandboxOptions: DevSandboxOptions{
				Name:                  "test-agent",
				Namespace:             "default",
				WorkspaceStorageClass: "premium-rwo",
			},
		}
		sb, _ := NewAgentSandbox(opt)
		pvcs, found, err := unstructured.NestedSlice(sb.Object, "spec", "volumeClaimTemplates")
		if err != nil || !found || len(pvcs) == 0 {
			t.Fatalf("Failed to find volumeClaimTemplates: %v", err)
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

func TestSandbox_LauncherLabel(t *testing.T) {
	defer func(old string) { Launcher = old }(Launcher)

	for _, tc := range []struct{ launcher, want string }{
		{"factory", "factory"},
		{"repo-agent", "repo-agent"},
	} {
		Launcher = tc.launcher
		sb, _ := NewAgentSandbox(AgentSandboxOptions{DevSandboxOptions: DevSandboxOptions{Name: "a"}})
		if got := sb.GetLabels()[LabelLauncher]; got != tc.want {
			t.Errorf("agent sandbox launcher = %q, want %q", got, tc.want)
		}
	}
}
