package sandbox

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReviewSandbox_ResourceLimitsAndRequests(t *testing.T) {
	// 1. Verify default resources when not explicitly configured
	defaultOpt := ReviewSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      "factory-pr-kcc-1234",
			Namespace: "overseer-kcc",
		},
		PRNumber:   1234,
		PRTitle:    "Fix issue",
		PRHTMLURL:  "https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/1234",
		PRDiffURL:  "https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/1234.diff",
		PRCloneURL: "https://github.com/GoogleCloudPlatform/k8s-config-connector.git",
		RepoName:   "k8s-config-connector",
	}

	sb, _ := NewReviewSandbox(defaultOpt)
	containers, found, err := unstructured.NestedSlice(sb.Object, "spec", "podTemplate", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		t.Fatalf("Failed to find containers in sandbox spec: %v", err)
	}

	container, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Container is not a map: %T", containers[0])
	}

	resources, ok := container["resources"].(map[string]interface{})
	if !ok {
		t.Fatalf("Resources is not a map: %T", container["resources"])
	}

	requests, ok := resources["requests"].(map[string]interface{})
	if !ok {
		t.Fatalf("Requests is not a map: %T", resources["requests"])
	}
	if got := requests["memory"]; got != "2Gi" {
		t.Errorf("default memory request = %v, want 2Gi", got)
	}
	if got := requests["cpu"]; got != "500m" {
		t.Errorf("default cpu request = %v, want 500m", got)
	}

	limits, ok := resources["limits"].(map[string]interface{})
	if !ok {
		t.Fatalf("Limits is not a map: %T", resources["limits"])
	}
	if got := limits["memory"]; got != "6Gi" {
		t.Errorf("default memory limit = %v, want 6Gi", got)
	}
	if got := limits["cpu"]; got != "4" {
		t.Errorf("default cpu limit = %v, want 4", got)
	}

	// 2. Verify custom resources (e.g. KCC overseer configured limits)
	customOpt := ReviewSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:              "factory-pr-k8s-config-connector-12571",
			Namespace:         "overseer-kcc",
			CPURequest:        "2000m",
			CPULimit:          "8000m",
			MemoryRequest:     "4Gi",
			MemoryLimit:       "16Gi",
			EphemeralStorage:  "10Gi",
			WorkspaceDiskSize: "40Gi",
		},
		PRNumber:   12571,
		PRTitle:    "Update resource",
		PRHTMLURL:  "https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/12571",
		PRDiffURL:  "https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/12571.diff",
		PRCloneURL: "https://github.com/GoogleCloudPlatform/k8s-config-connector.git",
		RepoName:   "k8s-config-connector",
	}

	customSB, _ := NewReviewSandbox(customOpt)
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
