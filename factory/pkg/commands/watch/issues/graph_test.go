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
)

// An issue graph's parent (the graph label) is not fixed - fix says
// graph: false, and the scanner reads the fact from the label - while an
// issue of the trigger label alone is.
func TestQueueTasks_SkipsGraphParents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
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
	s, queue, _ := newScanner(t, Config{GitHubLogin: "watcher", TriggerLabel: "factory"}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo")})

	if it := s.issueItemOf(fanoutIssue(100, "x", "factory", "overseer/graph")); !it.GraphParent {
		t.Error("the graph label does not make a graph parent")
	}
	if it := s.issueItemOf(fanoutIssue(101, "x", "factory", "factory/graph")); !it.GraphParent {
		t.Error("the trigger's spelling of the graph label does not make a graph parent")
	}
	s.queueTasks(context.Background(), []*githubv39.Issue{
		fanoutIssue(100, "x", "factory", "overseer/graph"),
		fanoutIssue(102, "x", "factory"),
	}, map[int]bool{})
	if _, ok := queue.enqueued["task-issue-100-rule-fix.yaml"]; ok {
		t.Error("a graph parent was fixed")
	}
	if _, ok := queue.enqueued["task-issue-102-rule-fix.yaml"]; !ok {
		t.Errorf("a plain issue was not fixed: %v", queue.enqueued)
	}
}
