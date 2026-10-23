package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

const childBody = "Do a.\n\nPart of #100.\n\n<!-- factory:fanout parent=100 items=a -->\n"

// TestAdoptCreatedIssues_LeavesFanoutParentsAlone pins that a fan-out's
// parent, filed as the operator, is not adopted: work would start on the
// parent itself. Its children are created when they are to be worked on
// (create: lazy), so they are adopted like any issue the operator files.
func TestAdoptCreatedIssues_LeavesFanoutParentsAlone(t *testing.T) {
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
	if len(got) != 3 || got[0] != 101 || got[1] != 102 || got[2] != 103 {
		t.Errorf("adopted %v, want [101 102 103]: both children and the plain issue", got)
	}
}

// TestQueueTasks_SkipsFanoutParents pins that a parent is never worked on,
// and that a child is fixed like any issue, labelled or not (one created
// without triage access carries no label).
func TestQueueTasks_SkipsFanoutParents(t *testing.T) {
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
	s, queue, _ := newScanner(t, Config{GitHubLogin: "watcher"}, Deps{GitHub: github.ForRepo(gh, "test-owner", "test-repo")})

	// A child the watcher opened is the overseer's (pointed at) without
	// the trigger label yet.
	unlabelled := fanoutIssue(101, childBody)
	unlabelled.User = &githubv39.User{Login: githubv39.String("watcher")}
	s.queueTasks(context.Background(), []*githubv39.Issue{
		fanoutIssue(100, "spec", "factory", "overseer/fanout"),
		unlabelled,
		fanoutIssue(102, childBody, "factory"),
	}, map[int]bool{})

	var got []string
	for name := range queue.enqueued {
		got = append(got, name)
	}
	slices.Sort(got)
	if len(got) != 2 || got[0] != "task-issue-101-rule-fix.yaml" || got[1] != "task-issue-102-rule-fix.yaml" {
		t.Errorf("queued %v, want both children, task-issue-101-rule-fix.yaml and task-issue-102-rule-fix.yaml", got)
	}
}
