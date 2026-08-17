package watch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestIsSandboxTaskCompleted(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()
	ns := "test-ns"

	tests := []struct {
		taskType          string
		annotatedTaskType string
		state             string
		expectedCompleted bool
	}{
		{"pr-comments", "address-comments", "Completed", true},
		{"pr-comments", "address-comments", "Running", false},
		{"pr-investigate", "investigate", "Completed", true},
		{"pr-iterate", "iterate", "Completed", true},
		{"pr-review", "review", "Completed", true},
		{"issue-fix", "fix-issue", "Completed", true},
		{"agent-chore", "agent", "Completed", true},
		{"pr-comments", "wrong-type", "Completed", false},
	}

	for _, tc := range tests {
		sbName := fmt.Sprintf("sb-%s-%s", tc.taskType, tc.state)
		fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
			k8s.SandboxGVR: "SandboxList",
		})
		kubeClient := &clients.KubernetesClient{
			DynamicClient: fakeDynamic,
		}

		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sbName,
					"namespace": ns,
					"annotations": map[string]interface{}{
						"sandbox.gemini.google.com/last-task-state": tc.state,
						"sandbox.gemini.google.com/last-task-type":  tc.annotatedTaskType,
					},
				},
			},
		}

		_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, sb, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create mock sandbox: %v", err)
		}

		completed, err := isSandboxTaskCompleted(ctx, kubeClient, ns, sbName, tc.taskType)
		if err != nil {
			t.Errorf("Unexpected error for %s: %v", tc.taskType, err)
		}
		if completed != tc.expectedCompleted {
			t.Errorf("For taskType=%s, annotatedTaskType=%s, state=%s: expected completed=%v, got %v",
				tc.taskType, tc.annotatedTaskType, tc.state, tc.expectedCompleted, completed)
		}
	}
}

func TestParseEvictionAge(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"", 7 * 24 * time.Hour, false},
		{"3d", 3 * 24 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"invalid", 0, true},
		{"xd", 0, true},
	}

	for _, tc := range tests {
		got, err := parseEvictionAge(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseEvictionAge(%q) expected error, got nil", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("parseEvictionAge(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseEvictionAge(%q) = %v, want %v", tc.input, got, tc.want)
			}
		}
	}
}

func TestCountRunningSandboxTasks(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()
	ns := "test-ns"

	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	// 1. Running sandbox (no annotations)
	sb1 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "sb-1",
				"namespace": ns,
			},
		},
	}
	// 2. Completed sandbox
	sb2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "sb-2",
				"namespace": ns,
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": "Completed",
				},
			},
		},
	}
	// 3. Scaled down sandbox (replicas: 0)
	sb3 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "sb-3",
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, _ = fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, sb1, metav1.CreateOptions{})
	_, _ = fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, sb2, metav1.CreateOptions{})
	_, _ = fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, sb3, metav1.CreateOptions{})

	count, err := countRunningSandboxTasks(ctx, kubeClient, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 running sandbox, got %d", count)
	}
}

func TestResolveSandboxName(t *testing.T) {
	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-ns",
		},
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
		},
	}

	// Issue task name
	name := w.resolveSandboxName(context.Background(), nil, nil, "issue-fix", 10)
	if name != "fix-test-repo-10" {
		t.Errorf("expected 'fix-test-repo-10', got %q", name)
	}

	// Chore task name fallback
	name = w.resolveSandboxName(context.Background(), nil, nil, "agent-chore", 10)
	if name != "fix-test-repo-10" {
		t.Errorf("expected 'fix-test-repo-10', got %q", name)
	}

	// PR task name fallback
	name = w.resolveSandboxName(context.Background(), nil, nil, "pr-review", 55)
	if name != "factory-pr-55" {
		t.Errorf("expected 'factory-pr-55', got %q", name)
	}
}
