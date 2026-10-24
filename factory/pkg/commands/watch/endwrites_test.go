package watch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// A task's lifecycle hooks are the watcher's writes: on start (with
// {{bot}} the task's bot), on success when it completed, on failure when
// it failed - a stop withdrawing the item's pending tasks - and on end
// either way, after.
func TestEndWrites(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	w := newTestWatcher(t, t.TempDir())
	w.repoClient = github.ForRepo(gh, "test-owner", "test-repo")
	w.triggerLabel = "overseer"
	coordinator := &watcherTaskCoordinator{w: w}
	task := func() *api.QueueTask {
		return &api.QueueTask{Type: api.TypePRRule, Number: 7, Rule: "adopt", Assignee: "coder-bot",
			OnStart:   &api.TaskWrites{Assign: []string{"{{bot}}"}},
			OnSuccess: &api.TaskWrites{Unlabel: []string{"overseer/adopt"}},
			OnFailure: &api.TaskWrites{Label: []string{"overseer/adopt-failed"}},
			OnEnd:     &api.TaskWrites{Comment: "done"}}
	}
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"completed", nil, []string{"DELETE /repos/test-owner/test-repo/issues/7/labels/overseer/adopt", "POST /repos/test-owner/test-repo/issues/7/comments"}},
		{"failed", errors.New("boom"), []string{"POST /repos/test-owner/test-repo/issues/7/labels", "POST /repos/test-owner/test-repo/issues/7/comments"}},
	} {
		mu.Lock()
		calls = nil
		mu.Unlock()
		coordinator.NotifyTaskFinished(context.Background(), task(), tc.err)
		mu.Lock()
		got := slices.Clone(calls)
		mu.Unlock()
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: calls %v, want %v", tc.name, got, tc.want)
		}
	}
	// On start: {{bot}} is the task's bot.
	mu.Lock()
	calls = nil
	mu.Unlock()
	coordinator.hookWrites(context.Background(), task(), "on-start", task().OnStart)
	mu.Lock()
	if !slices.Equal(calls, []string{"POST /repos/test-owner/test-repo/issues/7/assignees"}) {
		t.Errorf("on-start calls %v", calls)
	}
	mu.Unlock()

	// Run as the watcher itself (no pool bot): claimed, not unassigned.
	mu.Lock()
	calls = nil
	mu.Unlock()
	self := &api.QueueTask{Type: api.TypeIssueRule, Number: 7, Rule: "fix", Assignee: "watcher-bot",
		OnStart: &api.TaskWrites{Assign: []string{"{{bot}}"}, Unassign: []string{"watcher-bot"}}}
	coordinator.hookWrites(context.Background(), self, "on-start", self.OnStart)
	mu.Lock()
	if !slices.Equal(calls, []string{"POST /repos/test-owner/test-repo/issues/7/assignees"}) {
		t.Errorf("claim as the watcher: calls %v", calls)
	}
	mu.Unlock()

	// A task declaring nothing makes no call.
	mu.Lock()
	calls = nil
	mu.Unlock()
	coordinator.NotifyTaskFinished(context.Background(), &api.QueueTask{Type: api.TypePRRule, Number: 7}, nil)
	if len(calls) != 0 && !strings.Contains(strings.Join(calls, " "), "reactions") {
		t.Errorf("calls for a task declaring nothing: %v", calls)
	}
}

// An on-failure stop: the stop label and its comment, and the item's
// pending tasks withdrawn, as a rule's stop does.
func TestEndWrites_FailureStop(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+r.URL.EscapedPath()+" "+string(b))
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	dir := t.TempDir()
	w := newTestWatcher(t, dir)
	w.repoClient = github.ForRepo(gh, "test-owner", "test-repo")
	w.triggerLabel = "overseer"
	if err := w.queueMgr.Enqueue("task-pr-7-rule-review.yaml", &api.QueueTask{Type: api.TypePRRule, Number: 7, Rule: "review"}); err != nil {
		t.Fatal(err)
	}
	task := &api.QueueTask{Type: api.TypePRRule, Number: 7, Rule: "adopt", OnFailure: &api.TaskWrites{Stop: true, Comment: "Adopting failed."}}
	(&watcherTaskCoordinator{w: w}).NotifyTaskFinished(context.Background(), task, errors.New("boom"))
	mu.Lock()
	all := strings.Join(bodies, "\n")
	mu.Unlock()
	if !strings.Contains(all, `"overseer/stop"`) || !strings.Contains(all, "Adopting failed.") {
		t.Errorf("calls:\n%s", all)
	}
	if w.queueMgr.TaskExists("task-pr-7-rule-review.yaml") {
		t.Error("the item's pending task was not withdrawn")
	}
}
