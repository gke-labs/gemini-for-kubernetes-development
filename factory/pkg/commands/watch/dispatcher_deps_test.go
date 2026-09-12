package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
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

func newTestWatcher(t *testing.T, queueDir string) *Watcher {
	t.Helper()
	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-namespace",
			Image:     "custom-image:v1",
		},
		Flags: Flags{
			Repo:        RepoFlag{Owner: "test-owner", Repo: "test-repo"},
			QueueDir:    queueDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()
	return w
}

func TestWatcher_InitQueueManager_ConstructsDispatcher(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	if w.dispatcher == nil {
		t.Fatal("expected the watcher to construct a dispatcher")
	}
}

// TestWatcher_DispatchOnce_UsesWatcherAdapters exercises the dispatcher through the
// watcher-provided SandboxService and TaskCoordinator adapters against fake clients.
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

func TestWatcherSandboxService_ResolvesIssueSandboxName(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	sandboxes := &watcherSandboxService{w: w}

	if got := sandboxes.ResolveSandboxName(context.Background(), api.TypeIssueFix, 42); got != "fix-test-repo-42" {
		t.Errorf("ResolveSandboxName = %q, want %q", got, "fix-test-repo-42")
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
