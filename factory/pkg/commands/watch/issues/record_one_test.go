package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// Record evaluates one issue and returns the facts it recorded, the open
// pull requests listed to tell whether one closes it (factory watch rules
// explain); a pull request's number is refused.
func TestRecordOneIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/test-owner/test-repo/issues/5":
			_ = json.NewEncoder(w).Encode(fanoutIssue(5, "please fix", "factory"))
		case r.URL.Path == "/repos/test-owner/test-repo/issues/6":
			_ = json.NewEncoder(w).Encode(&githubv39.Issue{Number: githubv39.Int(6), PullRequestLinks: &githubv39.PullRequestLinks{}})
		case r.URL.Path == "/repos/test-owner/test-repo/pulls":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequest{})
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
		case r.URL.Path == "/search/issues":
			_ = json.NewEncoder(w).Encode(githubv39.IssuesSearchResult{Total: githubv39.Int(0)})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	s, queue, entities := newScanner(t, Config{DryRun: true}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo")})

	rec, err := s.Record(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if it := rec.Item; it.Kind != rules.OnIssue || it.Number != 5 || it.LinkedPR != "" || it.Workflow != "" || len(it.Labels) != 1 {
		t.Errorf("item = %+v", it)
	}
	if entities.prsSet == 0 {
		t.Error("the open pull requests were not listed")
	}
	if len(queue.enqueued) != 0 {
		t.Errorf("dry-run queued %v", queue.enqueued)
	}
	if _, err := s.Record(context.Background(), 6); err == nil || !strings.Contains(err.Error(), "is a pull request") {
		t.Errorf("a pull request: %v", err)
	}
}
