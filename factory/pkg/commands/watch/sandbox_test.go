package watch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var fakeKubeSecretData string

func newFakeKubeClient(objects ...runtime.Object) *clients.KubernetesClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, obj := range objects {
		u := obj.(*unstructured.Unstructured)
		_, _ = dynamicClient.Resource(k8s.SandboxGVR).Namespace(u.GetNamespace()).Create(context.Background(), u, metav1.CreateOptions{})
	}

	restConfig := &rest.Config{
		Host: "http://localhost",
		ContentConfig: rest.ContentConfig{
			GroupVersion: &schema.GroupVersion{Group: "", Version: "v1"},
			ContentType:  "application/json",
		},
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/secrets/") {
				if fakeKubeSecretData != "" {
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(fakeKubeSecretData)),
						Header:     make(http.Header),
					}
				}
				return &http.Response{
					StatusCode: 404,
					Body:       io.NopCloser(bytes.NewBufferString(`{"metadata":{},"status":"Failure","message":"secrets not found","reason":"NotFound","code":404}`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				Header:     make(http.Header),
			}
		}),
	}
	clientset, _ := kubernetes.NewForConfig(restConfig)

	return &clients.KubernetesClient{
		DynamicClient: dynamicClient,
		Clientset:     clientset,
	}
}

func newFakeSandbox(name, namespace, taskState, taskType string) *unstructured.Unstructured {
	sb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
	sb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	if taskState != "" || taskType != "" {
		annotations := make(map[string]string)
		if taskState != "" {
			annotations["sandbox.gemini.google.com/last-task-state"] = taskState
		}
		if taskType != "" {
			annotations["sandbox.gemini.google.com/last-task-type"] = taskType
		}
		sb.SetAnnotations(annotations)
	}
	return sb
}

func TestIsSandboxTaskRunning(t *testing.T) {
	ctx := context.Background()

	// Backup original executor
	origExecutor := envdSandboxTaskExecutor
	defer func() { envdSandboxTaskExecutor = origExecutor }()

	tests := []struct {
		name          string
		sandbox       *unstructured.Unstructured
		exitCode      string
		executorError error
		expected      bool
		expectedState string
	}{
		{
			name:     "Sandbox does not exist",
			sandbox:  nil,
			expected: false,
		},
		{
			name:     "Sandbox has no annotations (defaults to running)",
			sandbox:  newFakeSandbox("sb-1", "test-ns", "", ""),
			exitCode: "",
			expected: true,
		},
		{
			name:          "Sandbox is running, task terminates successfully",
			sandbox:       newFakeSandbox("sb-2", "test-ns", "Running", "fix-issue"),
			exitCode:      "0",
			expected:      false,
			expectedState: "Completed",
		},
		{
			name:          "Sandbox is running, task fails with exit code 1",
			sandbox:       newFakeSandbox("sb-3", "test-ns", "Running", "fix-issue"),
			exitCode:      "1",
			expected:      false,
			expectedState: "Failed",
		},
		{
			name:     "Sandbox already completed",
			sandbox:  newFakeSandbox("sb-4", "test-ns", "Completed", "fix-issue"),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var kubeClient *clients.KubernetesClient
			var name string
			if tc.sandbox != nil {
				kubeClient = newFakeKubeClient(tc.sandbox)
				name = tc.sandbox.GetName()
			} else {
				kubeClient = newFakeKubeClient()
				name = "non-existent"
			}

			// Mock the executor
			envdSandboxTaskExecutor = func(ctx context.Context, namespace, name string) (string, error) {
				return tc.exitCode, tc.executorError
			}

			got, err := isSandboxTaskRunning(ctx, kubeClient, "test-ns", name)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("isSandboxTaskRunning() = %v; want %v", got, tc.expected)
			}

			if tc.expectedState != "" {
				// Assert annotation update
				updated, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, tc.sandbox.GetName(), metav1.GetOptions{})
				if err != nil {
					t.Fatalf("failed to fetch updated sandbox: %v", err)
				}
				gotState := updated.GetAnnotations()["sandbox.gemini.google.com/last-task-state"]
				if gotState != tc.expectedState {
					t.Errorf("last-task-state = %q; want %q", gotState, tc.expectedState)
				}
			}
		})
	}
}

func TestIsSandboxTaskCompleted(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		sandbox  *unstructured.Unstructured
		taskType string
		expected bool
	}{
		{
			name:     "Sandbox does not exist",
			sandbox:  nil,
			taskType: "issue-fix",
			expected: false,
		},
		{
			name:     "Sandbox has no annotations",
			sandbox:  newFakeSandbox("sb-1", "test-ns", "", ""),
			taskType: "issue-fix",
			expected: false,
		},
		{
			name:     "Completed match for issue-fix (fix-issue in annotations)",
			sandbox:  newFakeSandbox("sb-2", "test-ns", "Completed", "fix-issue"),
			taskType: "issue-fix",
			expected: true,
		},
		{
			name:     "Completed match for agent-chore (agent in annotations)",
			sandbox:  newFakeSandbox("sb-3", "test-ns", "Completed", "agent"),
			taskType: "agent-chore",
			expected: true,
		},
		{
			name:     "State completed but task type mismatch",
			sandbox:  newFakeSandbox("sb-4", "test-ns", "Completed", "agent"),
			taskType: "issue-fix",
			expected: false,
		},
		{
			name:     "State running but task type matches",
			sandbox:  newFakeSandbox("sb-5", "test-ns", "Running", "fix-issue"),
			taskType: "issue-fix",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var kubeClient *clients.KubernetesClient
			if tc.sandbox != nil {
				kubeClient = newFakeKubeClient(tc.sandbox)
			} else {
				kubeClient = newFakeKubeClient()
			}

			var name string
			if tc.sandbox != nil {
				name = tc.sandbox.GetName()
			} else {
				name = "non-existent"
			}

			got, err := isSandboxTaskCompleted(ctx, kubeClient, "test-ns", name, tc.taskType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("isSandboxTaskCompleted() = %v; want %v", got, tc.expected)
			}
		})
	}
}

func TestCountRunningSandboxTasks(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		objects  []runtime.Object
		expected int
	}{
		{
			name:     "Empty list",
			objects:  []runtime.Object{},
			expected: 0,
		},
		{
			name: "Counts running and empty state tasks",
			objects: []runtime.Object{
				newFakeSandbox("sb-1", "test-ns", "Running", "fix-issue"),
				newFakeSandbox("sb-2", "test-ns", "", "fix-issue"),
				newFakeSandbox("sb-3", "test-ns", "Completed", "fix-issue"),
			},
			expected: 2,
		},
		{
			name: "Ignores overseer prefix sandboxes",
			objects: []runtime.Object{
				newFakeSandbox("overseer-sb-1", "test-ns", "Running", "fix-issue"),
				newFakeSandbox("sb-2", "test-ns", "Running", "fix-issue"),
			},
			expected: 1,
		},
		{
			name: "Ignores items with overseer label",
			objects: []runtime.Object{
				func() *unstructured.Unstructured {
					sb := newFakeSandbox("sb-1", "test-ns", "Running", "fix-issue")
					sb.SetLabels(map[string]string{"overseer.gemini.google.com/overseer": "true"})
					return sb
				}(),
				newFakeSandbox("sb-2", "test-ns", "Running", "fix-issue"),
			},
			expected: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kubeClient := newFakeKubeClient(tc.objects...)
			got, err := countRunningSandboxTasks(ctx, kubeClient, "test-ns")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("countRunningSandboxTasks() = %d; want %d", got, tc.expected)
			}
		})
	}
}

func TestCleanupClosedPRSandboxes(t *testing.T) {
	ctx := context.Background()

	// Case 1: DryRun = true -> sandbox should NOT be deleted
	sbDryRun := newFakeSandbox("factory-pr-10", "test-ns", "Running", "fix-issue")
	kubeClient1 := newFakeKubeClient([]runtime.Object{sbDryRun}...)
	httpClient1 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":10,"state":"closed"}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient1 := githubv39.NewClient(httpClient1)

	err := cleanupClosedPRSandboxes(ctx, ghClient1, kubeClient1, "owner", "repo", "test-ns", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient1.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "factory-pr-10", metav1.GetOptions{})
	if err != nil {
		t.Errorf("expected sandbox 'factory-pr-10' to still exist since dryRun=true, but got error: %v", err)
	}

	// Case 2: GitHub API Error -> sandbox should NOT be deleted
	sbError := newFakeSandbox("factory-pr-11", "test-ns", "Running", "fix-issue")
	kubeClient2 := newFakeKubeClient([]runtime.Object{sbError}...)
	httpClient2 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient2 := githubv39.NewClient(httpClient2)

	err = cleanupClosedPRSandboxes(ctx, ghClient2, kubeClient2, "owner", "repo", "test-ns", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient2.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "factory-pr-11", metav1.GetOptions{})
	if err != nil {
		t.Errorf("expected sandbox 'factory-pr-11' to still exist since github API returned error, but got error: %v", err)
	}

	// Case 3: Standard clean up (State closed) -> deleted
	sbClosed := newFakeSandbox("factory-pr-12", "test-ns", "Running", "fix-issue")
	kubeClient3 := newFakeKubeClient([]runtime.Object{sbClosed}...)
	httpClient3 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":12,"state":"closed"}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient3 := githubv39.NewClient(httpClient3)

	err = cleanupClosedPRSandboxes(ctx, ghClient3, kubeClient3, "owner", "repo", "test-ns", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient3.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "factory-pr-12", metav1.GetOptions{})
	if err == nil {
		t.Errorf("expected sandbox 'factory-pr-12' to be deleted, but it still exists")
	}
}

func TestCleanupClosedIssueSandboxes(t *testing.T) {
	ctx := context.Background()

	// Case 1: DryRun = true -> sandbox should NOT be deleted
	sbDryRun := newFakeSandbox("fix-repo-20", "test-ns", "Running", "fix-issue")
	kubeClient1 := newFakeKubeClient([]runtime.Object{sbDryRun}...)
	httpClient1 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":20,"state":"closed"}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient1 := githubv39.NewClient(httpClient1)

	err := cleanupClosedIssueSandboxes(ctx, ghClient1, kubeClient1, "owner", "repo", "test-ns", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient1.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "fix-repo-20", metav1.GetOptions{})
	if err != nil {
		t.Errorf("expected sandbox 'fix-repo-20' to still exist since dryRun=true, but got error: %v", err)
	}

	// Case 2: GitHub API Error -> sandbox should NOT be deleted
	sbError := newFakeSandbox("fix-repo-21", "test-ns", "Running", "fix-issue")
	kubeClient2 := newFakeKubeClient([]runtime.Object{sbError}...)
	httpClient2 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewBufferString("Internal Error")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient2 := githubv39.NewClient(httpClient2)

	err = cleanupClosedIssueSandboxes(ctx, ghClient2, kubeClient2, "owner", "repo", "test-ns", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient2.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "fix-repo-21", metav1.GetOptions{})
	if err != nil {
		t.Errorf("expected sandbox 'fix-repo-21' to still exist since github API returned error, but got error: %v", err)
	}

	// Case 3: Standard clean up (State closed) -> deleted
	sbClosed := newFakeSandbox("fix-repo-22", "test-ns", "Running", "fix-issue")
	kubeClient3 := newFakeKubeClient([]runtime.Object{sbClosed}...)
	httpClient3 := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"number":22,"state":"closed"}`)),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient3 := githubv39.NewClient(httpClient3)

	err = cleanupClosedIssueSandboxes(ctx, ghClient3, kubeClient3, "owner", "repo", "test-ns", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = kubeClient3.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "fix-repo-22", metav1.GetOptions{})
	if err == nil {
		t.Errorf("expected sandbox 'fix-repo-22' to be deleted, but it still exists")
	}
}

func TestResolveSandboxName(t *testing.T) {
	ctx := context.Background()

	// Case 1: issue-fix, sandbox exists
	sbWf := newFakeSandbox("wf-issue-10", "test-ns", "", "")
	kubeClient1 := newFakeKubeClient([]runtime.Object{sbWf}...)
	name := resolveSandboxName(ctx, kubeClient1, nil, "issue-fix", 10, "owner", "repo", "test-ns")
	if name != "wf-issue-10" {
		t.Errorf("expected wf-issue-10, got %s", name)
	}

	// Case 2: issue-fix, sandbox does not exist -> fallback
	kubeClient2 := newFakeKubeClient()
	name2 := resolveSandboxName(ctx, kubeClient2, nil, "issue-fix", 10, "owner", "repo", "test-ns")
	if name2 != "fix-repo-10" {
		t.Errorf("expected fix-repo-10, got %s", name2)
	}

	// Case 3: pr task, sandbox with label exists
	sbPR := newFakeSandbox("custom-pr-sb", "test-ns", "", "")
	sbPR.SetLabels(map[string]string{"factory.gemini.google.com/pr": "11"})
	kubeClient3 := newFakeKubeClient([]runtime.Object{sbPR}...)
	name3 := resolveSandboxName(ctx, kubeClient3, nil, "pr-iterate", 11, "owner", "repo", "test-ns")
	if name3 != "custom-pr-sb" {
		t.Errorf("expected custom-pr-sb, got %s", name3)
	}

	// Case 4: pr task, no labeled sandbox, but issue sandbox exists (Self-healing Aliasing)
	sbIssue := newFakeSandbox("fix-repo-12", "test-ns", "", "")
	kubeClient4 := newFakeKubeClient([]runtime.Object{sbIssue}...)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls/11") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":11,"body":"fixes #12","html_url":"https://github.com/owner/repo/pull/11"}`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	name4 := resolveSandboxName(ctx, kubeClient4, ghClient, "pr-iterate", 11, "owner", "repo", "test-ns")
	if name4 != "fix-repo-12" {
		t.Errorf("expected fix-repo-12, got %s", name4)
	}

	// Verify that the issue sandbox has been aliased with the PR label
	updated, err := kubeClient4.DynamicClient.Resource(k8s.SandboxGVR).Namespace("test-ns").Get(ctx, "fix-repo-12", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to fetch updated sandbox: %v", err)
	}
	if updated.GetLabels()["factory.gemini.google.com/pr"] != "11" {
		t.Errorf("expected sandbox fix-repo-12 to be labeled with pr=11, got labels: %v", updated.GetLabels())
	}
}
