package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// staleTestCoordinator is a coordinator whose GitHub answers with handler.
func staleTestCoordinator(t *testing.T, handler http.HandlerFunc) *watcherTaskCoordinator {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	w := newTestWatcher(t, t.TempDir())
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	w.repoClient = github.ForRepo(gh, "test-owner", "test-repo")
	return &watcherTaskCoordinator{w: w}
}

// A PR task decided at a head is stale once the PR has moved on, and holds
// while it has not; a PR it cannot read is taken to hold.
func TestStaleTaskComparesTheHead(t *testing.T) {
	task := &api.QueueTask{Type: api.TypePRInvestigate, Number: 7, CommitSHA: "aaaaaaaaaaaa", Expect: []string{api.ExpectHead}}
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		stale  bool
		reason string
	}{
		{"moved", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"number":7,"head":{"sha":"bbbbbbbbbbbb"}}`)) }, true, "head moved aaaaaaa→bbbbbbb"},
		{"unchanged", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"number":7,"head":{"sha":"aaaaaaaaaaaa"}}`)) }, false, ""},
		{"unreadable", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := staleTestCoordinator(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/repos/test-owner/test-repo/pulls/7" {
					tc.answer(w)
					return
				}
				_, _ = w.Write([]byte(`[]`))
			})
			stale, reason := c.StaleTask(context.Background(), task)
			if stale != tc.stale || reason != tc.reason {
				t.Errorf("StaleTask = %v, %q; want %v, %q", stale, reason, tc.stale, tc.reason)
			}
		})
	}
}

// A fix decided on the issue having no linked PR is stale once an open PR
// is connected to it; if the check fails, the task runs as it would
// without it.
func TestStaleTaskChecksForALinkedPR(t *testing.T) {
	task := &api.QueueTask{Type: api.TypeIssueFix, Number: 5, Expect: []string{api.ExpectNoLinkedPR}}
	linked := `[{"event":"connected","source":{"issue":{"number":9,"state":"open","pull_request":{"url":"x"}}}}]`
	for _, tc := range []struct {
		name     string
		timeline func(w http.ResponseWriter)
		search   func(w http.ResponseWriter)
		stale    bool
	}{
		{"linked", func(w http.ResponseWriter) { _, _ = w.Write([]byte(linked)) }, nil, true},
		{"none", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[]`)) }, nil, false},
		{"unreadable", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := staleTestCoordinator(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/repos/test-owner/test-repo/issues/5/timeline":
					tc.timeline(w)
				case "/search/issues":
					if tc.search != nil {
						tc.search(w)
						return
					}
					_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))
				default:
					_, _ = w.Write([]byte(`[]`))
				}
			})
			if stale, reason := c.StaleTask(context.Background(), task); stale != tc.stale {
				t.Errorf("StaleTask = %v (%q), want %v", stale, reason, tc.stale)
			}
		})
	}
}

// A task with no recorded facts, or a chore, is never stale.
func TestStaleTaskWithoutFacts(t *testing.T) {
	c := staleTestCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("GitHub was asked about a task with nothing to check")
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, task := range []*api.QueueTask{
		{Type: api.TypePRComments, Number: 7, CommitSHA: "aaaa"},
		{Type: api.TypeAgentChore, Number: 0, Expect: []string{api.ExpectHead}},
	} {
		if stale, _ := c.StaleTask(context.Background(), task); stale {
			t.Errorf("task %+v is stale", task)
		}
	}
}
