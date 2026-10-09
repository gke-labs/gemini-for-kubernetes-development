package watch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
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

// stubRunner is a dispatcher.TaskRunner that records invocations instead of spawning processes.
type stubRunner struct {
	mu    sync.Mutex
	calls []string
}

var _ dispatcher.TaskRunner = (*stubRunner)(nil)

func (s *stubRunner) Run(_ context.Context, taskFilename string, _ *api.QueueTask, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, taskFilename)
	return nil
}

func (s *stubRunner) invocations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// TestWatcher_DispatchOnce_UsesWatcherAdapters exercises the dispatcher through the
// watcher's sandbox service and TaskCoordinator adapter against fake clients.
func TestWatcher_DispatchOnce_UsesWatcherAdapters(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())

	runner := &stubRunner{}
	w.dispatcher = w.newDispatcher(runner)

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     42,
		URL:        "https://github.com/test-owner/test-repo/issues/42",
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue("task-issue-42.yaml", task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	w.dispatcher.DispatchOnce(context.Background())
	w.Wait()

	if got := runner.invocations(); len(got) != 1 || got[0] != "task-issue-42.yaml" {
		t.Fatalf("expected the task to be dispatched once, got %v", got)
	}
	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1), got (%d, %d, %d)", inc, proc, done)
	}

	// Given repo "test-repo", an issue-fix task for #42 resolves to "fix-test-repo-42"
	if w.sandboxLocks.IsBusy("fix-test-repo-42") {
		t.Error("expected the sandbox lease to be released after the worker completed")
	}
}

func TestWatcherSandboxes_ResolveName_IssueSandbox(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())

	if got := w.sandboxes.ResolveName(context.Background(), api.TypeIssueFix, 42); got != "fix-test-repo-42" {
		t.Errorf("ResolveName = %q, want %q", got, "fix-test-repo-42")
	}
}

func TestWatcherTaskCoordinator_SelectUser_PrefersTaskAssignee(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	w.targetAssignee = "watcher-bot"
	coordinator := &watcherTaskCoordinator{w: w}

	user, err := coordinator.SelectUser(context.Background(), &api.QueueTask{Type: api.TypeIssueFix, Assignee: "coder-bot"})
	if err != nil {
		t.Fatalf("SelectUser returned error: %v", err)
	}
	if user != "coder-bot" {
		t.Errorf("expected the task assignee to be used, got %q", user)
	}
}

// The fanout recipe runs as the account a fix of the parent would, never
// silently as the watcher's default secret.
func TestWatcher_ProposeFanout_RunsAsTheIssueFixAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/test-owner/test-repo/issues/5" {
			_, _ = w.Write([]byte(`{"number":5,"assignees":[{"login":"coder-b"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	w := newTestWatcher(t, t.TempDir())
	w.ghClient = githubv39.NewClient(nil)
	w.ghClient.BaseURL, _ = url.Parse(server.URL + "/")
	w.githubLogin = "factory-user-login"
	w.cfg = &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{"coder": {Users: []string{"coder-a", "coder-b"}}},
	}
	var gotURL, gotUser string
	w.ProposeFanout = func(_ context.Context, issueURL, user string) error {
		gotURL, gotUser = issueURL, user
		return nil
	}

	if err := w.proposeFanout(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://github.com/test-owner/test-repo/issues/5" || gotUser != "coder-b" {
		t.Errorf("ProposeFanout(%q, %q), want the issue as its assignee coder-b", gotURL, gotUser)
	}
}

func TestWatcherTaskCoordinator_ShouldCancelTask_NoGitHubClient(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	coordinator := &watcherTaskCoordinator{w: w}

	if cancel, reason := coordinator.ShouldCancelTask(context.Background(), &api.QueueTask{Number: 1}); cancel {
		t.Errorf("expected no cancellation without a GitHub client, got reason %q", reason)
	}
}

func TestTaskStartedComment(t *testing.T) {
	for _, tc := range []struct {
		taskType api.TaskType
		want     bool
	}{
		{api.TypeIssueFix, true},
		{api.TypePRInvestigate, true},
		{api.TypePRComments, true},
		{api.TypePRIterate, true},
		{api.TypePRReview, true},
		{api.TypeAgentChore, false},
	} {
		got := taskStartedComment(tc.taskType) != ""
		if got != tc.want {
			t.Errorf("taskStartedComment(%s) has comment = %v, want %v", tc.taskType, got, tc.want)
		}
	}
}

// TestWatcherTaskCoordinator_NotifyTaskFinished_ResolvesTaskFeedback pins how
// the task lifecycle wires up the resolver: review-bot feedback is resolved
// (ReviewerLogins reaches it), and feedback older than the task's trigger is
// left alone (Since is the task's TriggerEventTime).
func TestWatcherTaskCoordinator_NotifyTaskFinished_ResolvesTaskFeedback(t *testing.T) {
	const (
		watcherLogin  = "factory-bot"
		reviewerLogin = "gemini-code-assist[bot]"
	)
	trigger := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name    string
		taskErr error
		want    string
	}{
		{"success", nil, "THUMBS_UP"},
		{"failure", errors.New("agent failed"), "CONFUSED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				reacted = map[string]string{}
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/graphql":
					var req struct {
						Query     string                 `json:"query"`
						Variables map[string]interface{} `json:"variables"`
					}
					_ = json.NewDecoder(r.Body).Decode(&req)
					if strings.Contains(req.Query, "addReaction") {
						mu.Lock()
						reacted[req.Variables["subjectId"].(string)] = req.Variables["content"].(string)
						mu.Unlock()
						_, _ = w.Write([]byte(`{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`))
						return
					}
					// Every review carries the watcher's acknowledgement.
					_, _ = w.Write([]byte(`{"data":{"node":{"reactions":{"nodes":[{"content":"EYES","user":{"login":"` + watcherLogin + `"}}]}}}}`))
				case r.URL.Path == "/repos/test-owner/test-repo/pulls/7/reviews":
					_, _ = w.Write([]byte(`[
						{"id":1,"node_id":"PRR_before","user":{"login":"` + reviewerLogin + `","type":"Bot"},"body":"old","state":"COMMENTED","submitted_at":"2026-09-01T11:00:00Z"},
						{"id":2,"node_id":"PRR_after","user":{"login":"` + reviewerLogin + `","type":"Bot"},"body":"new","state":"COMMENTED","submitted_at":"2026-09-01T13:00:00Z"}
					]`))
				default:
					_, _ = w.Write([]byte(`[]`))
				}
			}))
			t.Cleanup(server.Close)

			w := newTestWatcher(t, t.TempDir())
			gh := githubv39.NewClient(nil)
			gh.BaseURL, _ = url.Parse(server.URL + "/")
			w.repoClient = github.ForRepo(gh, "test-owner", "test-repo")
			w.githubLogin = watcherLogin
			w.cfg = &config.FactoryConfig{
				Roles: map[string]config.RoleConfig{"reviewer": {Users: []string{reviewerLogin}}},
			}

			coordinator := &watcherTaskCoordinator{w: w}
			coordinator.NotifyTaskFinished(context.Background(), &api.QueueTask{
				Type:             api.TypePRComments,
				Number:           7,
				TriggerEventTime: trigger,
			}, tc.taskErr)

			mu.Lock()
			defer mu.Unlock()
			if got := reacted["PRR_after"]; got != tc.want {
				t.Errorf("reaction on the review after the trigger = %q, want %q", got, tc.want)
			}
			if got, ok := reacted["PRR_before"]; ok {
				t.Errorf("review before the trigger got reaction %q, want none", got)
			}
		})
	}
}

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

// newAdoptionTestWatcher builds a watcher whose dispatcher polls adopted sandboxes quickly.
func newAdoptionTestWatcher(t *testing.T, queueDir, ns string, kubeClient *clients.KubernetesClient) *Watcher {
	t.Helper()
	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: ns,
		},
		Flags: Flags{
			QueueDir: queueDir,
			Repo:     RepoFlag{Owner: "test-owner", Repo: "test-repo"},
		},
		kubeClient: kubeClient,
	}
	w.initComponents()
	w.dispatcher = dispatcher.New(dispatcher.Config{
		AdoptionPollInterval: 10 * time.Millisecond,
	}, dispatcher.Deps{
		Queue:        w.queueMgr,
		SandboxLocks: w.sandboxLocks,
		Sandboxes:    w.sandboxes,
		Coordinator:  &watcherTaskCoordinator{w: w},
		Runner:       &stubRunner{},
	})

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(queueDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}
	return w
}

func writeStuckTask(t *testing.T, queueDir, filename, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(queueDir, "processing", filename), []byte(body), 0644); err != nil {
		t.Fatalf("failed to write task file: %v", err)
	}
}

func TestRecover_AdoptsRunningTaskAndReleasesOnCompletion(t *testing.T) {
	tempDir := t.TempDir()
	ns := "test-ns"
	sbName := "fix-test-repo-100"

	kubeClient, fakeDynamic := newTestKubeClientWithSandbox(sbName, ns, "Running", "fix-issue")
	w := newAdoptionTestWatcher(t, tempDir, ns, kubeClient)

	fn := "task-issue-100.yaml"
	writeStuckTask(t, tempDir, fn, `type: issue-fix
number: 100
status: Running
url: https://github.com/test-owner/test-repo/issues/100
`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.dispatcher.Recover(ctx)

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
	if _, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Update(ctx, updatedSB, metav1.UpdateOptions{}); err != nil {
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

func TestRecover_AlreadyCompletedReleasesLease(t *testing.T) {
	tempDir := t.TempDir()
	ns := "test-ns"
	sbName := "fix-test-repo-101"

	kubeClient, _ := newTestKubeClientWithSandbox(sbName, ns, "Completed", "fix-issue")
	w := newAdoptionTestWatcher(t, tempDir, ns, kubeClient)

	fn := "task-issue-101.yaml"
	writeStuckTask(t, tempDir, fn, `type: issue-fix
number: 101
status: Running
url: https://github.com/test-owner/test-repo/issues/101
`)

	w.dispatcher.Recover(context.Background())

	// Task should be in processed immediately
	if _, err := os.Stat(filepath.Join(tempDir, "processed", fn)); err != nil {
		t.Errorf("expected %s in processed dir: %v", fn, err)
	}

	// Lease should NOT be held
	if w.sandboxLocks.IsBusy(sbName) {
		t.Errorf("expected sandbox %s not to be busy", sbName)
	}
}

func TestRecover_MissingSandboxRequeuesAndReleasesLease(t *testing.T) {
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
	w := newAdoptionTestWatcher(t, tempDir, ns, kubeClient)

	fn := "task-issue-102.yaml"
	writeStuckTask(t, tempDir, fn, `type: issue-fix
number: 102
status: Running
url: https://github.com/test-owner/test-repo/issues/102
`)

	w.dispatcher.Recover(context.Background())

	// Task should be requeued to incoming
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s in incoming dir: %v", fn, err)
	}

	// Lease should NOT be held
	if w.sandboxLocks.IsBusy(sbName) {
		t.Errorf("expected sandbox %s not to be busy", sbName)
	}
}
