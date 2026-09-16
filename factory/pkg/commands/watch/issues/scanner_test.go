package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// fakeQueue records what the scanner queued and withdrew.
type fakeQueue struct {
	mu       sync.Mutex
	existing map[string]bool
	enqueued map[string]*api.QueueTask
	removed  []int
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{existing: map[string]bool{}, enqueued: map[string]*api.QueueTask{}}
}

func (q *fakeQueue) TaskExists(filename string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.existing[filename]
}

func (q *fakeQueue) Enqueue(filename string, task *api.QueueTask) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued[filename] = task
	q.existing[filename] = true
	return nil
}

func (q *fakeQueue) RemovePendingTasksForNumber(number int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.removed = append(q.removed, number)
	return nil
}

// fakeEntities stands in for the shared entity cache.
type fakeEntities struct {
	hasPRs     bool
	referenced map[int]bool
	openIssues []int
	prsSet     int
}

func (e *fakeEntities) HasOpenPRs() bool { return e.hasPRs }

func (e *fakeEntities) UpdateOpenPRs(prs []*githubv39.PullRequest) {
	e.prsSet++
	e.hasPRs = true
	if e.referenced == nil {
		e.referenced = map[int]bool{}
	}
}

func (e *fakeEntities) GetReferencedIssuesMap() map[int]bool { return e.referenced }

func (e *fakeEntities) SetOpenIssueNumbers(nums []int) { e.openIssues = nums }

// fakeSandboxes reports the sandboxes that are mid-run.
type fakeSandboxes struct {
	running map[string]bool
}

func (s fakeSandboxes) IsTaskRunning(_ context.Context, name string) (bool, error) {
	return s.running[name], nil
}

// fakeUsers pins every task to one account.
type fakeUsers struct{ user string }

func (u fakeUsers) SelectUser(context.Context, api.TaskType, int) (string, error) {
	return u.user, nil
}

// newScanner builds a scanner over fakes, with the cache already primed so that
// tests exercising the queueing path do not have to serve a PR listing.
func newScanner(t *testing.T, cfg Config, deps Deps) (*Scanner, *fakeQueue, *fakeEntities) {
	t.Helper()
	queue := newFakeQueue()
	entities := &fakeEntities{hasPRs: true, referenced: map[int]bool{}}

	if cfg.TriggerLabel == "" {
		cfg.TriggerLabel = "factory"
	}
	if cfg.ProcessedDir == "" {
		cfg.ProcessedDir = t.TempDir()
	}
	if deps.GitHub == nil {
		// A client with no transport still carries the owner and repo, which
		// the scanner reads for sandbox names and task URLs.
		deps.GitHub = github.ForRepo(nil, "test-owner", "test-repo")
	}
	if deps.Queue == nil {
		deps.Queue = queue
	}
	if deps.Entities == nil {
		deps.Entities = entities
	}
	if deps.Sandboxes == nil {
		deps.Sandboxes = fakeSandboxes{}
	}
	if deps.Users == nil {
		deps.Users = fakeUsers{user: "bot1"}
	}
	return New(cfg, deps), queue, entities
}

func TestQueueTasks_Filters(t *testing.T) {
	s, queue, _ := newScanner(t, Config{MinNumber: 6}, Deps{})

	issues := []*githubv39.Issue{
		{Number: githubv39.Int(5)},
		{
			Number: githubv39.Int(10),
			Labels: []*githubv39.Label{{Name: githubv39.String("overseer/stop")}},
		},
		{Number: githubv39.Int(15)},
	}

	s.queueTasks(context.Background(), issues, map[int]bool{15: true})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %d tasks, want 0: %v", len(queue.enqueued), queue.enqueued)
	}
	// A stopped issue must also have its pending work withdrawn, not merely be
	// skipped: the operator applied the label to stop work already queued.
	if len(queue.removed) != 1 || queue.removed[0] != 10 {
		t.Errorf("withdrew pending tasks for %v, want [10]", queue.removed)
	}
}

// TestScanOnce_ColdPRCacheFailsClosed is the regression test for
// k8s-config-connector#9259: after a restart into a rate limit window the open
// PR cache is empty, which makes every issue look like it has no linked PR.
func TestScanOnce_ColdPRCacheFailsClosed(t *testing.T) {
	listedIssues := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/test-owner/test-repo/pulls":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		default:
			listedIssues = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]*githubv39.Issue{{Number: githubv39.Int(1)}})
		}
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	entities := &fakeEntities{referenced: map[int]bool{}}
	s, queue, _ := newScanner(t, Config{BotUsers: []string{"bot1"}}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo"), Entities: entities})

	s.ScanOnce(context.Background())

	if listedIssues {
		t.Error("scanned issues with an unpopulated open PR cache; want the cycle to stop")
	}
	if len(queue.enqueued) != 0 {
		t.Errorf("queued %d tasks with an unpopulated open PR cache, want 0", len(queue.enqueued))
	}
}

// TestScanOnce_PrimesPRCache covers the mode where no pull request scanner
// runs: the issue scanner pays for the one listing itself rather than stalling
// forever on a cache nobody is going to fill.
func TestScanOnce_PrimesPRCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/test-owner/test-repo/pulls":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequest{})
		default:
			_ = json.NewEncoder(w).Encode([]*githubv39.Issue{})
		}
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	entities := &fakeEntities{referenced: map[int]bool{}}
	s, _, _ := newScanner(t, Config{}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo"), Entities: entities})

	s.ScanOnce(context.Background())

	if entities.prsSet != 1 {
		t.Errorf("published the open PR listing %d times, want 1", entities.prsSet)
	}

	// Once a scan has published, the scanner leaves that half of the cache to
	// the pull request scanner that owns it.
	s.ScanOnce(context.Background())
	if entities.prsSet != 1 {
		t.Errorf("published the open PR listing %d times after priming, want 1", entities.prsSet)
	}
}

func TestScanOnce_QueuesAssignedIssue(t *testing.T) {
	updated := time.Now().Add(-time.Hour)
	created := updated.Add(-time.Hour)

	var addedLabels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/test-owner/test-repo/issues" && r.Method == http.MethodGet:
			// Only the assignee query returns anything; the labelled sweep and
			// the creator query are served empty.
			if r.URL.Query().Get("assignee") != "bot1" {
				_ = json.NewEncoder(w).Encode([]*githubv39.Issue{})
				return
			}
			_ = json.NewEncoder(w).Encode([]*githubv39.Issue{{
				Number:    githubv39.Int(7),
				CreatedAt: &created,
				UpdatedAt: &updated,
				Body:      githubv39.String("please fix"),
			}})
		case r.URL.Path == "/repos/test-owner/test-repo/issues/7/timeline":
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
		case r.URL.Path == "/repos/test-owner/test-repo/issues/7/labels" && r.Method == http.MethodPost:
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	s, queue, entities := newScanner(t, Config{BotUsers: []string{"bot1"}, TargetAssignee: "bot1"}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo")})

	s.ScanOnce(context.Background())

	task, ok := queue.enqueued["task-issue-7.yaml"]
	if !ok {
		t.Fatalf("issue 7 was not queued; queued: %v", queue.enqueued)
	}
	if task.Type != api.TypeIssueFix {
		t.Errorf("task type = %q, want %q", task.Type, api.TypeIssueFix)
	}
	if task.Assignee != "bot1" {
		t.Errorf("task assignee = %q, want 'bot1'", task.Assignee)
	}
	// An issue picked up by assignment is adopted with the trigger label, so
	// that the label always records what the watcher is acting on.
	if len(addedLabels) != 1 || addedLabels[0] != "factory" {
		t.Errorf("added labels %v, want ['factory']", addedLabels)
	}
	// The labelled sweep runs on the first cycle, so the open issue set is
	// published for the sandbox reconciler to collect against.
	if entities.openIssues == nil {
		t.Error("the open issue set was not published after the first sweep")
	}

	// A second cycle must not re-queue: the issue has not been updated since.
	delete(queue.existing, "task-issue-7.yaml")
	delete(queue.enqueued, "task-issue-7.yaml")
	s.ScanOnce(context.Background())
	if _, ok := queue.enqueued["task-issue-7.yaml"]; ok {
		t.Error("issue 7 was queued twice without having been updated in between")
	}
}

func TestScanOnce_PausedWhileDraining(t *testing.T) {
	s, queue, _ := newScanner(t, Config{}, Deps{Paused: func() bool { return true }})

	s.ScanOnce(context.Background())

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %d tasks while draining, want 0", len(queue.enqueued))
	}
}

func TestScanOnce_SkipsIssueWithRunningSandbox(t *testing.T) {
	// The in-flight sandbox check is the last gate before queueing, so the
	// timeline lookup ahead of it has to be served for the test to reach it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	updated := time.Now()
	s, queue, _ := newScanner(t, Config{}, Deps{
		GitHub:    github.ForRepo(gh, "test-owner", "test-repo"),
		Sandboxes: fakeSandboxes{running: map[string]bool{"fix-test-repo-9": true}},
	})

	s.queueTasks(context.Background(), []*githubv39.Issue{{
		Number:    githubv39.Int(9),
		UpdatedAt: &updated,
		Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
	}}, nil)

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %d tasks for an issue with an in-flight sandbox, want 0", len(queue.enqueued))
	}
}

func TestRun_StopsOnContextCancellation(t *testing.T) {
	s, _, _ := newScanner(t, Config{Interval: time.Hour}, Deps{Paused: func() bool { return true }})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		// Cancellation is how the subcontroller is asked to stop, so it is not
		// reported as a failure.
		if err != nil {
			t.Errorf("Run() = %v, want nil after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}
}
