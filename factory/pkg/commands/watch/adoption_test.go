package watch

import (
	"context"
	"os"
	"path/filepath"
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func newTestKubeClientWithSandbox(sbName, ns, taskState, taskType string) (*clients.KubernetesClient, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})

	sb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sbName,
				"namespace": ns,
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": taskState,
					"sandbox.gemini.google.com/last-task-type":  taskType,
				},
			},
		},
	}
	_, _ = fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(context.Background(), sb, metav1.CreateOptions{})

	cs, _ := kubernetes.NewForConfig(&rest.Config{})
	client := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
		Clientset:     cs,
	}
	return client, fakeDynamic
}

func TestRecoverStuckTasks_AdoptsRunningTaskAndReleasesOnCompletion(t *testing.T) {
	tempDir := t.TempDir()
	ns := "test-ns"
	sbName := "fix-test-repo-100"

	kubeClient, fakeDynamic := newTestKubeClientWithSandbox(sbName, ns, "Running", "fix-issue")

	oldInterval := adoptionPollInterval
	adoptionPollInterval = 10 * time.Millisecond
	defer func() { adoptionPollInterval = oldInterval }()

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: ns,
		},
		Flags: Flags{
			QueueDir: tempDir,
			Repo:     RepoFlag{Owner: "test-owner", Repo: "test-repo"},
		},
		kubeClient: kubeClient,
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	fn := "task-issue-100.yaml"
	taskYAML := `type: issue-fix
number: 100
status: Running
url: https://github.com/test-owner/test-repo/issues/100
`
	if err := os.WriteFile(filepath.Join(tempDir, "processing", fn), []byte(taskYAML), 0644); err != nil {
		t.Fatalf("failed to write task file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run recovery
	w.recoverStuckTasks(ctx)

	// Verify the sandbox lease was acquired
	if !w.sandboxLocks.IsBusy(sbName) {
		t.Fatalf("expected sandbox %s to be leased by adoption supervisor", sbName)
	}

	// Task should still be in processing
	if _, err := os.Stat(filepath.Join(tempDir, "processing", fn)); err != nil {
		t.Errorf("expected %s to remain in processing while running: %v", fn, err)
	}

	// Now simulate the cluster task completing: update sandbox annotation to Completed
	updatedSB := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sbName,
				"namespace": ns,
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": "Completed",
					"sandbox.gemini.google.com/last-task-type":  "fix-issue",
				},
			},
		},
	}
	_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Update(ctx, updatedSB, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update mock sandbox: %v", err)
	}

	// Wait for adoption monitor to complete task and release lease
	waitDone := make(chan struct{})
	go func() {
		w.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for adopted task to complete")
	}

	// Verify lease is released
	if w.sandboxLocks.IsBusy(sbName) {
		t.Errorf("expected sandbox lease for %s to be released after task completed", sbName)
	}

	// Verify file moved to processed
	if _, err := os.Stat(filepath.Join(tempDir, "processed", fn)); err != nil {
		t.Errorf("expected %s in processed dir: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s removed from processing dir", fn)
	}
}

func TestRecoverStuckTasks_AlreadyCompletedReleasesLease(t *testing.T) {
	tempDir := t.TempDir()
	ns := "test-ns"
	sbName := "fix-test-repo-101"

	kubeClient, _ := newTestKubeClientWithSandbox(sbName, ns, "Completed", "fix-issue")

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: ns,
		},
		Flags: Flags{
			QueueDir: tempDir,
			Repo:     RepoFlag{Owner: "test-owner", Repo: "test-repo"},
		},
		kubeClient: kubeClient,
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	fn := "task-issue-101.yaml"
	taskYAML := `type: issue-fix
number: 101
status: Running
url: https://github.com/test-owner/test-repo/issues/101
`
	if err := os.WriteFile(filepath.Join(tempDir, "processing", fn), []byte(taskYAML), 0644); err != nil {
		t.Fatalf("failed to write task file: %v", err)
	}

	w.recoverStuckTasks(context.Background())

	// Task should be in processed immediately
	if _, err := os.Stat(filepath.Join(tempDir, "processed", fn)); err != nil {
		t.Errorf("expected %s in processed dir: %v", fn, err)
	}

	// Lease should NOT be held
	if w.sandboxLocks.IsBusy(sbName) {
		t.Errorf("expected sandbox %s not to be busy", sbName)
	}
}

func TestRecoverStuckTasks_MissingSandboxRequeuesAndReleasesLease(t *testing.T) {
	tempDir := t.TempDir()
	ns := "test-ns"
	sbName := "fix-test-repo-102"

	// Kube client has no sandbox
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	cs, _ := kubernetes.NewForConfig(&rest.Config{})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
		Clientset:     cs,
	}

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: ns,
		},
		Flags: Flags{
			QueueDir: tempDir,
			Repo:     RepoFlag{Owner: "test-owner", Repo: "test-repo"},
		},
		kubeClient: kubeClient,
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	fn := "task-issue-102.yaml"
	taskYAML := `type: issue-fix
number: 102
status: Running
url: https://github.com/test-owner/test-repo/issues/102
`
	if err := os.WriteFile(filepath.Join(tempDir, "processing", fn), []byte(taskYAML), 0644); err != nil {
		t.Fatalf("failed to write task file: %v", err)
	}

	w.recoverStuckTasks(context.Background())

	// Task should be requeued to incoming
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s in incoming dir: %v", fn, err)
	}

	// Lease should NOT be held
	if w.sandboxLocks.IsBusy(sbName) {
		t.Errorf("expected sandbox %s not to be busy", sbName)
	}
}
