package sandbox

import (
	"context"
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
		recipe   string
		num      int
		want     string
	}{
		{"issue task", api.TypeIssueRule, "fix", 10, "fix-test-repo-10"},
		{"pr recipe runs in the PR's fix sandbox", api.TypePRRule, "care-comments", 55, "fix-test-repo-55"},
		{"rebase runs in the PR's fix sandbox", api.TypePRRule, "care-rebase", 55, "fix-test-repo-55"},
		// A credentials: clone recipe has a sandbox of its own, whatever
		// the task's type: review's, and a repository rule's run of it.
		{"review runs in the review recipe's sandbox", api.TypePRRule, "review", 55, "review-test-repo-55"},
		{"a rule task of a clone recipe too", api.TypePRRule, "review", 55, "review-test-repo-55"},
		{"a rule task of another recipe in the PR's", api.TypePRRule, "summarize", 55, "fix-test-repo-55"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.ResolveName(ctx, tc.taskType, tc.recipe, tc.num); got != tc.want {
				t.Errorf("ResolveName(%s, %d) = %q, want %q", tc.taskType, tc.num, got, tc.want)
			}
		})
	}
}

// A recipe on a PR (care-ci) runs in the PR's fix sandbox, the one
// `factory recipe` (EnsurePRSandbox) picks: never a factory-pr-* one.
func TestServiceResolveNamePRRecipeFollowsFixSandbox(t *testing.T) {
	const prURL = "https://github.com/test-owner/test-repo/pull/55"
	fixSandbox := newSandbox("fix-test-repo-50", map[string]interface{}{"repo": "test-repo", "htmlURL": prURL})
	fixSandbox.SetLabels(map[string]string{labelPR: "55"})

	tests := []struct {
		name      string
		sandboxes []*unstructured.Unstructured
		want      string
	}{
		{"none: the PR's own fix name", nil, "fix-test-repo-55"},
		{"not a factory-pr one", []*unstructured.Unstructured{newSandbox("factory-pr-test-repo-55", nil)}, "fix-test-repo-55"},
		{"the PR's fix sandbox", []*unstructured.Unstructured{fixSandbox}, "fix-test-repo-50"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
				Kube: newFakeKubeClient(t, tc.sandboxes...),
			})
			if got := svc.ResolveName(context.Background(), api.TypePRRule, "care-ci", 55); got != tc.want {
				t.Errorf("ResolveName = %q, want %q", got, tc.want)
			}
		})
	}
}

// An agent chore runs in its agent's sandbox, as factory agent create
// names it: a scheduled chore's by its name (or, queued before tasks
// carried it, its file's), a workflow's by its issue. Any other task is
// ResolveName's.
func TestServiceResolveTaskName(t *testing.T) {
	svc := NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{})
	for _, tc := range []struct {
		name string
		task api.QueueTask
		want string
	}{
		{"scheduled chore", api.QueueTask{Type: api.TypeAgentChore, AgentFile: ".agents/kcc-release.md", AgentName: "KCC Release Scheduler"}, "agent-test-repo-kcc-release-scheduler"},
		{"chore queued without its name", api.QueueTask{Type: api.TypeAgentChore, AgentFile: ".agents/dependency-agent.md"}, "agent-test-repo-dependency-agent"},
		{"workflow", api.QueueTask{Type: api.TypeAgentChore, Number: 12700, AgentFile: ".agents/workflows/kcc-example.txt", SessionID: "issue-12700"}, "wf-issue-12700"},
		{"fix", api.QueueTask{Type: api.TypeIssueRule, Number: 10}, "fix-test-repo-10"},
	} {
		if got := svc.ResolveTaskName(context.Background(), &tc.task); got != tc.want {
			t.Errorf("%s: ResolveTaskName = %q, want %q", tc.name, got, tc.want)
		}
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

	if got := svc.ResolveName(context.Background(), api.TypeIssueRule, "fix", 10); got != "wf-issue-10" {
		t.Errorf("expected the existing workflow sandbox to win, got %q", got)
	}
}

func TestServiceSuspend(t *testing.T) {
	ctx := context.Background()
	kube := newFakeKubeClient(t, newSandbox("sb-to-suspend", nil))
	svc := NewService(ServiceConfig{
		Namespace: testNamespace,
		Owner:     "test-owner",
		Repo:      "test-repo",
	}, ServiceDeps{
		Kube: kube,
	})

	if err := svc.Suspend(ctx, "sb-to-suspend"); err != nil {
		t.Fatalf("Suspend failed: %v", err)
	}

	updated, err := kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(testNamespace).Get(ctx, "sb-to-suspend", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get sandbox: %v", err)
	}
	rep, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	if rep != 0 {
		t.Errorf("Expected replicas=0 after suspend, got %d", rep)
	}
}
