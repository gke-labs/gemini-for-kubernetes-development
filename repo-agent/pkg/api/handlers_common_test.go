package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{
			name:  "No truncation needed",
			s:     "hello",
			limit: 10,
			want:  "hello",
		},
		{
			name:  "Exact limit",
			s:     "hello",
			limit: 5,
			want:  "hello",
		},
		{
			name:  "Basic truncation with buffer",
			s:     "hello world how are you",
			limit: 15,
			want:  "hello", // 15 (limit) - 10 (buffer) = 5 limit. s[:5] is "hello".
		},
		{
			name:  "Truncation with code block",
			s:     "```\nsome code\n```\nmore text",
			limit: 15,
			want:  "```\ns\n```", // safeLimit = 15 - 10 = 5. s[:5] is "```\ns". strings.Count is 1. Adds \n```.
		},
		{
			name:  "UTF-8 truncation - safe",
			s:     "世界", // 6 bytes
			limit: 16,
			want:  "世界",
		},
		{
			name:  "UTF-8 truncation - middle of rune",
			s:     "世界 hello world", // 6 bytes + 12 = 18 bytes
			limit: 13,
			want:  "世", // safeLimit = 13 - 10 = 3. s[:3] is "世".
		},
		{
			name:  "Empty string with small limit",
			s:     "",
			limit: 5,
			want:  "[Bot-",
		},
		{
			name:  "Zero limit",
			s:     "hello",
			limit: 0,
			want:  "",
		},
		{
			name:  "Tilde code block",
			s:     "~~~\nsome code\n~~~\nmore text",
			limit: 15,
			want:  "~~~\ns\n~~~",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.s, tt.limit); got != tt.want {
				t.Errorf("truncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetRepoWatchName(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandbox: "SandboxList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager: manager,
	}

	ctx := context.Background()
	namespace := "default"
	sandboxName := "test-sandbox"

	t.Run("Sandbox exists with repowatch label", func(t *testing.T) {
		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": namespace,
					"labels": map[string]interface{}{
						"review.gemini.google.com/repowatch": "test-repowatch",
					},
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrSandbox).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})

		got := server.getRepoWatchName(ctx, namespace, sandboxName)
		if got != "test-repowatch" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "test-repowatch")
		}
	})

	t.Run("Sandbox does not exist", func(t *testing.T) {
		got := server.getRepoWatchName(ctx, namespace, "non-existent")
		if got != "n/a" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "n/a")
		}
	})

	t.Run("Sandbox lacks repowatch label", func(t *testing.T) {
		sandboxNameNoLabel := "no-label"
		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxNameNoLabel,
					"namespace": namespace,
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrSandbox).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})

		got := server.getRepoWatchName(ctx, namespace, sandboxNameNoLabel)
		if got != "n/a" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "n/a")
		}
	})
}

func TestGetTaskMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask: "SandboxTaskList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager: manager,
	}

	ctx := context.Background()
	namespace := "default"
	sandboxName := "test-sandbox"

	t.Run("Explicit task metadata provided", func(t *testing.T) {
		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "task1", "uid1")
		if gotName != "task1" || gotUID != "uid1" {
			t.Errorf("getTaskMetadata() = (%q, %q), want (%q, %q)", gotName, gotUID, "task1", "uid1")
		}
	})

	t.Run("Explicit n/a provided - no fallback", func(t *testing.T) {
		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "n/a", "n/a")
		if gotName != "n/a" || gotUID != "n/a" {
			t.Errorf("getTaskMetadata() with n/a = (%q, %q), want (%q, %q)", gotName, gotUID, "n/a", "n/a")
		}
	})

	t.Run("Fallback to latest task", func(t *testing.T) {
		// Create a few tasks
		now := time.Now()
		for i := 1; i <= 3; i++ {
			task := &sandboxtaskv1alpha1.SandboxTask{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "custom.agents.x-k8s.io/v1alpha1",
					Kind:       "SandboxTask",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:              fmt.Sprintf("task-%d", i),
					Namespace:         namespace,
					UID:               types.UID(fmt.Sprintf("uid-%d", i)),
					CreationTimestamp: metav1.Time{Time: now.Add(time.Duration(i) * time.Minute)},
					Labels: map[string]string{
						"sandbox.gemini.google.com/sandbox-name": sandboxName,
					},
				},
			}
			unstructuredTask, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
			_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredTask}, metav1.CreateOptions{})
		}

		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "", "")
		expectedName := fmt.Sprintf("%s/%s", namespace, "task-3")
		expectedUID := "uid-3"
		if gotName != expectedName || gotUID != expectedUID {
			t.Errorf("getTaskMetadata() fallback = (%q, %q), want (%q, %q)", gotName, gotUID, expectedName, expectedUID)
		}
	})
}
