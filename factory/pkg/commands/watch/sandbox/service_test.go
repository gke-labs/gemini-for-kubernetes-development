package sandbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

const testNamespace = "test-ns"

// newFakeKubeClient returns a Kubernetes client whose dynamic and typed clients
// are both fakes, preloaded with the given sandbox objects.
func newFakeKubeClient(t *testing.T, objects ...*unstructured.Unstructured) *clients.KubernetesClient {
	t.Helper()
	return newFakeKubeClientWithPods(t, nil, objects...)
}

// newFakeKubeClientWithPods is newFakeKubeClient with the typed client seeded
// with pods, which the reconciler inspects when cleaning up evicted sandboxes.
func newFakeKubeClientWithPods(t *testing.T, pods []runtime.Object, objects ...*unstructured.Unstructured) *clients.KubernetesClient {
	t.Helper()

	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	for _, obj := range objects {
		if _, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(testNamespace).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create mock sandbox %s: %v", obj.GetName(), err)
		}
	}
	return &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
		Clientset:     k8sfake.NewSimpleClientset(pods...),
	}
}

// newSandbox builds an unstructured sandbox object with the given annotations.
func newSandbox(name string, annotations map[string]interface{}) *unstructured.Unstructured {
	metadata := map[string]interface{}{
		"name":      name,
		"namespace": testNamespace,
	}
	if annotations != nil {
		metadata["annotations"] = annotations
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata":   metadata,
		},
	}
}

func TestServiceIsTaskCompleted(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		taskType          api.TaskType
		annotatedTaskType string
		state             string
		expectedCompleted bool
	}{
		{api.TypePRComments, "address-comments", "Completed", true},
		{api.TypePRComments, "address-comments", "Running", false},
		{api.TypePRInvestigate, "investigate", "Completed", true},
		{api.TypePRIterate, "iterate", "Completed", true},
		{api.TypePRReview, "review", "Completed", true},
		{api.TypeIssueFix, "fix-issue", "Completed", true},
		{api.TypeAgentChore, "agent", "Completed", true},
		{api.TypePRComments, "wrong-type", "Completed", false},
	}

	for _, tc := range tests {
		sbName := fmt.Sprintf("sb-%s-%s", tc.taskType, tc.state)
		kubeClient := newFakeKubeClient(t, newSandbox(sbName, map[string]interface{}{
			annotationLastTaskState: tc.state,
			annotationLastTaskType:  tc.annotatedTaskType,
		}))
		svc := NewService(ServiceConfig{Namespace: testNamespace}, ServiceDeps{Kube: kubeClient})

		completed, err := svc.IsTaskCompleted(ctx, sbName, tc.taskType)
		if err != nil {
			t.Errorf("Unexpected error for %s: %v", tc.taskType, err)
		}
		if completed != tc.expectedCompleted {
			t.Errorf("For taskType=%s, annotatedTaskType=%s, state=%s: expected completed=%v, got %v",
				tc.taskType, tc.annotatedTaskType, tc.state, tc.expectedCompleted, completed)
		}
	}
}

func TestServiceCountRunningTasks(t *testing.T) {
	ctx := context.Background()

	// 1. Running sandbox (no annotations)
	sb1 := newSandbox("sb-1", nil)
	// 2. Completed sandbox
	sb2 := newSandbox("sb-2", map[string]interface{}{annotationLastTaskState: "Completed"})
	// 3. Scaled down sandbox (replicas: 0)
	sb3 := newSandbox("sb-3", nil)
	sb3.Object["spec"] = map[string]interface{}{"replicas": int64(0)}

	svc := NewService(ServiceConfig{Namespace: testNamespace}, ServiceDeps{
		Kube: newFakeKubeClient(t, sb1, sb2, sb3),
	})

	count, err := svc.CountRunningTasks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 running sandbox, got %d", count)
	}
}

func TestServiceCountRunningTasksWithoutCluster(t *testing.T) {
	count, err := NewService(ServiceConfig{Namespace: testNamespace}, ServiceDeps{}).CountRunningTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 running sandboxes without a cluster, got %d", count)
	}
}

func TestServiceResolveName(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ServiceConfig{
		Namespace: testNamespace,
		Owner:     "test-owner",
		Repo:      "test-repo",
	}, ServiceDeps{})

	tests := []struct {
		name     string
		taskType api.TaskType
		num      int
		want     string
	}{
		{"issue task", api.TypeIssueFix, 10, "fix-test-repo-10"},
		{"chore task falls back to the issue sandbox", api.TypeAgentChore, 10, "fix-test-repo-10"},
		{"pr task", api.TypePRReview, 55, "factory-pr-55"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.ResolveName(ctx, tc.taskType, tc.num); got != tc.want {
				t.Errorf("ResolveName(%s, %d) = %q, want %q", tc.taskType, tc.num, got, tc.want)
			}
		})
	}
}

func TestServiceResolveNamePrefersWorkflowSandbox(t *testing.T) {
	svc := NewService(ServiceConfig{
		Namespace: testNamespace,
		Owner:     "test-owner",
		Repo:      "test-repo",
	}, ServiceDeps{
		Kube: newFakeKubeClient(t, newSandbox("wf-issue-10", nil)),
	})

	if got := svc.ResolveName(context.Background(), api.TypeIssueFix, 10); got != "wf-issue-10" {
		t.Errorf("expected the existing workflow sandbox to win, got %q", got)
	}
}
