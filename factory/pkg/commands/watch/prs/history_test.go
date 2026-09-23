package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// TestFetchHistory_FailedInlineCommentListingIsFatal pins the error semantics of
// the inline comment listing.
//
// Tolerating the failure and returning a history with an empty revCommentsMap
// would be worse than useless: a pull request whose only outstanding feedback is
// inline would look clean, and reconcileReadiness would label it ready for a
// human and unassign the bot working on it. The evaluation is abandoned instead,
// to be retried on the next cycle.
func TestFetchHistory_FailedInlineCommentListingIsFatal(t *testing.T) {
	const num = 10

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/repos/test-owner/test-repo"
		switch r.URL.Path {
		case fmt.Sprintf("%s/pulls/%d/comments", base, num):
			w.WriteHeader(http.StatusInternalServerError)

		case fmt.Sprintf("%s/pulls/%d/commits", base, num):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{})

		case fmt.Sprintf("%s/issues/%d/comments", base, num):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})

		case fmt.Sprintf("%s/pulls/%d/reviews", base, num):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{{ID: githubv39.Int64(1)}})

		default:
			t.Errorf("unexpected request for %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	history, err := s.fetchHistory(context.Background(), num)
	if err == nil {
		t.Fatal("fetchHistory() error = nil, want the listing failure reported")
	}
	if !strings.Contains(err.Error(), "listing review comments") {
		t.Errorf("fetchHistory() error = %q, want it to name the listing that failed", err)
	}
	// A half-built history is as dangerous as no error at all: the caller has
	// no way to tell it apart from a complete one.
	if history != nil {
		t.Errorf("fetchHistory() history = %+v, want nil alongside the error", history)
	}
}
