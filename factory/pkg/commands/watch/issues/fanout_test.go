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

func fanoutIssue(n int, body string, labels ...string) *githubv39.Issue {
	issue := &githubv39.Issue{Number: githubv39.Int(n), Body: githubv39.String(body)}
	for _, l := range labels {
		issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	return issue
}

const childBody = "Do a.\n\nPart of #100.\n\n<!-- factory:fanout parent=100 item=a -->\n"

// TestAdoptCreatedIssues_LeavesFanoutsAlone pins that the issues a fan-out
// files as the operator are not adopted: adopting would label every child at
// once, defeating the window, and start work on the parent itself.
func TestAdoptCreatedIssues_LeavesFanoutsAlone(t *testing.T) {
	s, _, _ := newScanner(t, Config{DryRun: true}, Deps{})

	adopted := s.adoptCreatedIssues(context.Background(), []*githubv39.Issue{
		fanoutIssue(100, "spec", "factory/fanout"),
		fanoutIssue(101, childBody),
		fanoutIssue(102, childBody, "factory"),
		fanoutIssue(103, "please fix"),
	})

	var got []int
	for _, issue := range adopted {
		got = append(got, issue.GetNumber())
	}
	if len(got) != 2 || got[0] != 102 || got[1] != 103 {
		t.Errorf("adopted %v, want [102 103]: the started child and the plain issue", got)
	}
}

// TestQueueTasks_SkipsFanoutParentsAndWaitingChildren pins that a parent is
// never worked on, and that a child reaching the queue unlabelled - assigned to
// a bot by hand - waits for the fan-out to label it instead of being labelled
// here, out of turn.
func TestQueueTasks_SkipsFanoutParentsAndWaitingChildren(t *testing.T) {
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
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo")})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		fanoutIssue(100, "spec", "factory", "overseer/fanout"),
		fanoutIssue(101, childBody),
		fanoutIssue(102, childBody, "factory"),
	}, map[int]bool{})

	var got []string
	for name := range queue.enqueued {
		got = append(got, name)
	}
	if len(got) != 1 || got[0] != "task-issue-102.yaml" {
		t.Errorf("queued %v, want only the started child, task-issue-102.yaml", got)
	}
}
