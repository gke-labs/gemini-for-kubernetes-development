package issues

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan/scantest"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// sharedScanRun is one scanner over a freshly seeded fake repository: what it
// queued and what it wrote to GitHub.
type sharedScanRun struct {
	enqueued []string
	writes   []string
	calls    int
}

func runIssueScan(t *testing.T, shared bool, seed func(*scantest.Repo), cycles int) sharedScanRun {
	t.Helper()
	repo := scantest.NewRepo("test-owner", "test-repo")
	seed(repo)
	var mu sync.Mutex
	var writes []string
	repo.Extra = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			writes = append(writes, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
			mu.Unlock()
			_, _ = w.Write([]byte("[]"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/timeline") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_, _ = w.Write([]byte("{}"))
	}
	srv := repo.Server()
	defer srv.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	client := github.ForRepo(gh, "test-owner", "test-repo")

	cfg := Config{BotUsers: []string{"bot1", "bot2"}, TargetAssignee: "bot1", GitHubLogin: "watcher", ScanLimit: 2, TriggerLabel: "overseer"}
	deps := Deps{GitHub: client}
	if shared {
		deps.Listings = scan.New(scan.Config{Repo: "test-owner/test-repo", TriggerLabel: "overseer", BotUsers: cfg.BotUsers, GitHubLogin: "watcher", ReuseFor: time.Nanosecond}, client)
	}
	s, queue, _ := newScanner(t, cfg, deps)
	for range cycles {
		s.ScanOnce(context.Background())
	}

	var run sharedScanRun
	for fn := range queue.enqueued {
		run.enqueued = append(run.enqueued, fn)
	}
	slices.Sort(run.enqueued)
	run.writes = slices.Clone(writes)
	slices.Sort(run.writes)
	run.calls = repo.ListCalls()
	return run
}

func seedIssues(repo *scantest.Repo) {
	now := time.Now()
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	repo.Put(scantest.Item{Number: 10, Labels: []string{"overseer"}, Author: "alice", Created: ago(600), Updated: ago(100)})       // labelled: fix
	repo.Put(scantest.Item{Number: 11, Assignees: []string{"bot1"}, Author: "alice", Created: ago(600), Updated: ago(10)})         // assigned: labelled, fix
	repo.Put(scantest.Item{Number: 18, Assignees: []string{"bot1"}, Author: "alice", Created: ago(600), Updated: ago(20)})         // assigned: second on bot1's page
	repo.Put(scantest.Item{Number: 19, Assignees: []string{"bot1"}, Author: "alice", Created: ago(600), Updated: ago(30)})         // assigned: off bot1's first page (ScanLimit 2)
	repo.Put(scantest.Item{Number: 12, Author: "watcher", Created: ago(600), Updated: ago(40)})                                    // the watcher's own: adopted
	repo.Put(scantest.Item{Number: 13, PR: true, Assignees: []string{"bot1"}, Author: "bot1", Created: ago(600), Updated: ago(5)}) // a PR: the PR scanner's
	repo.Put(scantest.Item{Number: 14, Labels: []string{"overseer", "overseer/stop"}, Author: "alice", Created: ago(600), Updated: ago(50)})
	repo.Put(scantest.Item{Number: 15, Author: "alice", Created: ago(600), Updated: ago(1)}) // not the watcher's
	repo.Put(scantest.Item{Number: 16, State: "closed", Labels: []string{"overseer"}, Author: "alice", Created: ago(600), Updated: ago(1)})
	repo.Put(scantest.Item{Number: 17, Labels: []string{"overseer", "overseer/fanout"}, Author: "alice", Created: ago(600), Updated: ago(60)})
	repo.Put(scantest.Item{Number: 20, Assignees: []string{"bot2"}, Labels: []string{"overseer"}, Author: "alice", Created: ago(600), Updated: ago(70)})
}

// With the shared scan on, the issue scanner queues the same tasks and
// writes the same labels and assignees as when it lists for itself: the
// shared snapshot serves the same lists, the fast pass's first page of each
// bot's assignments included.
func TestSharedScanQueuesTheSame(t *testing.T) {
	off := runIssueScan(t, false, seedIssues, 1)
	on := runIssueScan(t, true, seedIssues, 1)
	if !slices.Equal(off.enqueued, on.enqueued) {
		t.Errorf("enqueued differ:\n off %v\n on  %v", off.enqueued, on.enqueued)
	}
	if !slices.Equal(off.writes, on.writes) {
		t.Errorf("writes differ:\n off %v\n on  %v", off.writes, on.writes)
	}
	if !slices.Contains(off.enqueued, "task-issue-11-rule-fix.yaml") || slices.Contains(off.enqueued, "task-issue-19-rule-fix.yaml") {
		t.Errorf("the scenario is not exercising the fast pass's first page: %v", off.enqueued)
	}
	t.Logf("first cycle listing calls: own listings %d, shared scan %d; queued %v; writes %v", off.calls, on.calls, on.enqueued, on.writes)
}

// Over several cycles the shared scan lists less: after its first full
// refresh, each cycle is one incremental listing for the repository.
func TestSharedScanListsLess(t *testing.T) {
	off := runIssueScan(t, false, seedIssues, 3)
	on := runIssueScan(t, true, seedIssues, 3)
	if !slices.Equal(off.enqueued, on.enqueued) {
		t.Errorf("enqueued differ:\n off %v\n on  %v", off.enqueued, on.enqueued)
	}
	if on.calls >= off.calls {
		t.Errorf("listing calls: shared %d, own %d; want fewer shared", on.calls, off.calls)
	}
	t.Logf("three cycles' listing calls: own listings %d, shared scan %d", off.calls, on.calls)
}
