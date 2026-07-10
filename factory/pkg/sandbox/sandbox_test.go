package sandbox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestSuspendIdleSandboxes(t *testing.T) {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	ctx := context.Background()
	ns := "test-ns"

	// Create an old sandbox that completed its last task 2 hours ago
	oldTime := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	oldSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "old-sandbox",
				"namespace": ns,
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": "Completed",
					"sandbox.gemini.google.com/completion-time": oldTime,
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	// Create a recent sandbox that completed its last task 10 minutes ago
	recentTime := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	recentSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "recent-sandbox",
				"namespace": ns,
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": "Completed",
					"sandbox.gemini.google.com/completion-time": recentTime,
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, oldSandbox, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create old sandbox: %v", err)
	}
	_, err = fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, recentSandbox, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create recent sandbox: %v", err)
	}

	// Run SuspendIdleSandboxes with a 30-minute timeout
	count, err := sandbox.SuspendIdleSandboxes(ctx, kubeClient, ns, 30*time.Minute, false)
	if err != nil {
		t.Fatalf("SuspendIdleSandboxes failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 sandbox suspended, got %d", count)
	}

	// Check replicas of old sandbox (should be 0)
	updatedOld, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "old-sandbox", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get old sandbox: %v", err)
	}
	repOld, _, _ := unstructured.NestedInt64(updatedOld.Object, "spec", "replicas")
	if repOld != 0 {
		t.Errorf("Expected old-sandbox replicas=0, got %d", repOld)
	}

	// Check replicas of recent sandbox (should stay 1)
	updatedRecent, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "recent-sandbox", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get recent sandbox: %v", err)
	}
	repRecent, _, _ := unstructured.NestedInt64(updatedRecent.Object, "spec", "replicas")
	if repRecent != 1 {
		t.Errorf("Expected recent-sandbox replicas=1, got %d", repRecent)
	}
}

func TestIsCurrentSandbox_And_SuspendSkip(t *testing.T) {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	ctx := context.Background()
	ns := "test-namespace"
	sbName := "my-active-daemon"

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("HOSTNAME", sbName)

	oldTime := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	controllerSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":              sbName,
				"namespace":         ns,
				"creationTimestamp": oldTime,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	if !sandbox.IsCurrentSandbox(ctx, kubeClient, controllerSandbox, ns) {
		t.Errorf("Expected IsCurrentSandbox=true when HOSTNAME matches sandbox name inside cluster")
	}

	// Verify that outside the cluster (workstation without k8s env/tokens), IsCurrentSandbox returns false
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("SANDBOX_NAME", "")
	if sandbox.IsCurrentSandbox(ctx, kubeClient, controllerSandbox, ns) {
		if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); os.IsNotExist(err) {
			t.Errorf("Expected IsCurrentSandbox=false when running outside cluster on workstation")
		}
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")

	_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, controllerSandbox, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create controller sandbox: %v", err)
	}

	count, err := sandbox.SuspendIdleSandboxes(ctx, kubeClient, ns, 30*time.Minute, false)
	if err != nil {
		t.Fatalf("SuspendIdleSandboxes failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 sandboxes suspended when checking current sandbox, got %d", count)
	}

	updated, _ := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, sbName, metav1.GetOptions{})
	rep, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	if rep != 1 {
		t.Errorf("Expected current sandbox replicas=1 (skipped), got %d", rep)
	}
}

func TestUpdateSandboxTaskAnnotation_Resume(t *testing.T) {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	ctx := context.Background()
	ns := "test-ns"

	suspendedSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "suspended-sandbox",
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, suspendedSandbox, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create suspended sandbox: %v", err)
	}

	// Start a task on the suspended sandbox
	err = sandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, ns, "suspended-sandbox", "review", "Running")
	if err != nil {
		t.Fatalf("UpdateSandboxTaskAnnotation failed: %v", err)
	}

	// Check that replicas is set to 1
	updated, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "suspended-sandbox", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get suspended-sandbox: %v", err)
	}
	rep, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	if rep != 1 {
		t.Errorf("Expected replicas=1 when starting task, got %d", rep)
	}
}
