package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestGetTraceabilityFooter(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask: "SandboxTaskList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager:          manager,
		TraceabilityEnabled: true,
	}

	ctx := context.Background()
	namespace := "default"
	sandboxName := "test-sandbox"
	repo := "test-repo"

	t.Run("Generate footer when enabled", func(t *testing.T) {
		footer := server.getTraceabilityFooter(ctx, "", namespace, sandboxName, repo, "test-task")
		if !strings.Contains(footer, "<!-- repo-agent-metadata") {
			t.Errorf("Expected footer to contain metadata block, got: %s", footer)
		}
		if !strings.Contains(footer, "sandbox: test-sandbox") {
			t.Errorf("Expected footer to contain sandbox name, got: %s", footer)
		}
	})

	t.Run("Do not generate footer when disabled", func(t *testing.T) {
		server.TraceabilityEnabled = false
		footer := server.getTraceabilityFooter(ctx, "", namespace, sandboxName, repo, "test-task")
		if footer != "" {
			t.Errorf("Expected empty footer when disabled, got: %s", footer)
		}
		server.TraceabilityEnabled = true
	})

	t.Run("Do not generate footer when already present", func(t *testing.T) {
		existingBody := "Some text\n<!-- repo-agent-metadata\n... -->"
		footer := server.getTraceabilityFooter(ctx, existingBody, namespace, sandboxName, repo, "test-task")
		if footer != "" {
			t.Errorf("Expected empty footer when already present, got: %s", footer)
		}
	})

	t.Run("Include latest task info", func(t *testing.T) {
		// Create a couple of tasks with different timestamps
		task1 := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "SandboxTask",
				"metadata": map[string]interface{}{
					"name":              "task-1",
					"namespace":         namespace,
					"uid":               "uid-1",
					"creationTimestamp": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
					"labels": map[string]interface{}{
						"sandbox.gemini.google.com/sandbox-name": sandboxName,
					},
				},
			},
		}
		task2 := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "SandboxTask",
				"metadata": map[string]interface{}{
					"name":              "task-2",
					"namespace":         namespace,
					"uid":               "uid-2",
					"creationTimestamp": time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
					"labels": map[string]interface{}{
						"sandbox.gemini.google.com/sandbox-name": sandboxName,
					},
				},
			},
		}

		// Use metav1.Time for proper comparison in fake client if possible, 
		// but unstructured uses strings for timestamps.
		// Actually, we need to make sure the fake client handles timestamps correctly or just rely on the sort logic.
		
		_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, task1, metav1.CreateOptions{})
		_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, task2, metav1.CreateOptions{})

		footer := server.getTraceabilityFooter(ctx, "", namespace, sandboxName, repo, "test-task")
		if !strings.Contains(footer, "sandbox-task: default/task-2") {
			t.Errorf("Expected footer to contain latest task (task-2), got: %s", footer)
		}
		if !strings.Contains(footer, "sandbox-task-uid: uid-2") {
			t.Errorf("Expected footer to contain latest task UID (uid-2), got: %s", footer)
		}
	})
}
