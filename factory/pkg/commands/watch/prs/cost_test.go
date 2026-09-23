package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// The tests in this file are about what an evaluation *costs*, rather than what
// it decides. They exist because the cost is not visible in the behaviour: a
// scanner that fetches the same parent issue three times and one that fetches it
// once queue exactly the same work, and the difference only shows up as a rate
// limit in production. Each test therefore asserts on the requests the scanner
// made, not on its conclusions.

// prFixture is a GitHub stand-in for one pull request that records every path
// it is asked for.
type prFixture struct {
	mu sync.Mutex

	num     int
	headSHA string
	// body is the pull request body, which is where a "Fixes #7" reference
	// that pulls a parent issue into the evaluation comes from.
	body string
	// updated is the updated_at the listing reports for the pull request.
	updated time.Time
	// reviews are the submitted reviews, whose inline comments used to cost a
	// request each.
	reviews []*githubv39.PullRequestReview
	// inline are the review comments the pull request-wide listing returns.
	inline []*githubv39.PullRequestComment

	calls []string
}

// start brings up the server and returns a client pointed at it.
func (f *prFixture) start(t *testing.T) *githubv39.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		call := r.URL.Path
		if q := r.URL.RawQuery; q != "" {
			call += "?" + q
		}
		f.calls = append(f.calls, call)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		f.route(w, r)
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	return gh
}

func (f *prFixture) route(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	base := "/repos/test-owner/test-repo"
	mergeable := true
	now := time.Now()

	switch r.URL.Path {
	case base + "/issues":
		// Both the fast pass's assignee query and the sweep's label query.
		_ = json.NewEncoder(w).Encode([]*githubv39.Issue{f.listingIssueLocked()})

	case base + "/pulls":
		_ = json.NewEncoder(w).Encode([]*githubv39.PullRequest{})

	case fmt.Sprintf("%s/pulls/%d", base, f.num):
		_ = json.NewEncoder(w).Encode(&githubv39.PullRequest{
			Number:    githubv39.Int(f.num),
			Mergeable: &mergeable,
			State:     githubv39.String("open"),
			Body:      githubv39.String(f.body),
			User:      &githubv39.User{Login: githubv39.String("bot1")},
			Head:      &githubv39.PullRequestBranch{SHA: githubv39.String(f.headSHA)},
			CreatedAt: &now,
		})

	case fmt.Sprintf("%s/pulls/%d/commits", base, f.num):
		_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{{
			SHA:    githubv39.String(f.headSHA),
			Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: &now}},
		}})

	case fmt.Sprintf("%s/issues/%d/comments", base, f.num):
		_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})

	case fmt.Sprintf("%s/pulls/%d/reviews", base, f.num):
		_ = json.NewEncoder(w).Encode(f.reviews)

	case fmt.Sprintf("%s/pulls/%d/comments", base, f.num):
		_ = json.NewEncoder(w).Encode(f.inline)

	case base + "/commits/" + f.headSHA + "/check-runs":
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []*githubv39.CheckRun{}})

	case base + "/commits/" + f.headSHA + "/statuses":
		_ = json.NewEncoder(w).Encode([]*githubv39.RepoStatus{})

	case fmt.Sprintf("%s/issues/%d/labels", base, f.num):
		// Label reads and writes both land here, and both answer with a list.
		_ = json.NewEncoder(w).Encode([]*githubv39.Label{})

	default:
		// Referenced issue reads, assignee writes and anything else the
		// evaluation happens to touch.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": 7,
			"labels": []interface{}{},
		})
	}
}

// listingIssueLocked is the pull request as the issue listings report it. The
// caller must hold f.mu.
func (f *prFixture) listingIssueLocked() *githubv39.Issue {
	updated := f.updated
	return &githubv39.Issue{
		Number:           githubv39.Int(f.num),
		UpdatedAt:        &updated,
		PullRequestLinks: &githubv39.PullRequestLinks{},
		Assignees:        []*githubv39.User{{Login: githubv39.String("bot1")}},
		Labels:           []*githubv39.Label{{Name: githubv39.String("factory")}},
	}
}

// count returns how many recorded paths contain substr.
func (f *prFixture) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// newFixtureScanner wires a scanner to the fixture with the settings every test
// in this file shares.
func newFixtureScanner(t *testing.T, f *prFixture) *Scanner {
	t.Helper()
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       f.start(t),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})
	return s
}

// TestFetchHistory_ReadsInlineCommentsInOneRequest covers the change from the
// per-review endpoint to the pull request-wide one: the cost of reading a
// conversation must not grow with the number of reviews it contains.
func TestFetchHistory_ReadsInlineCommentsInOneRequest(t *testing.T) {
	f := &prFixture{
		num:     10,
		headSHA: "sha-1",
		reviews: []*githubv39.PullRequestReview{
			{ID: githubv39.Int64(1)},
			{ID: githubv39.Int64(2)},
			{ID: githubv39.Int64(3)},
		},
		inline: []*githubv39.PullRequestComment{
			{ID: githubv39.Int64(11), PullRequestReviewID: githubv39.Int64(1), Body: githubv39.String("a")},
			{ID: githubv39.Int64(12), PullRequestReviewID: githubv39.Int64(1), Body: githubv39.String("b")},
			{ID: githubv39.Int64(13), PullRequestReviewID: githubv39.Int64(3), Body: githubv39.String("c")},
			// An inline comment with no review behind it, which must not end
			// up filed under review zero.
			{ID: githubv39.Int64(14), Body: githubv39.String("orphan")},
		},
	}
	s := newFixtureScanner(t, f)

	history, err := s.fetchHistory(context.Background(), 10)
	if err != nil {
		t.Fatalf("fetchHistory() error = %v", err)
	}

	if got := f.count("/pulls/10/comments"); got != 1 {
		t.Errorf("inline comment requests = %d, want 1 for the whole pull request", got)
	}
	if got := f.count("/reviews/"); got != 0 {
		t.Errorf("made %d per-review requests, want 0", got)
	}

	// The grouping the per-review fetch used to provide has to survive the
	// change, or every consumer that looks a review up by ID silently sees
	// nothing.
	if got := len(history.revCommentsMap[1]); got != 2 {
		t.Errorf("review 1 has %d inline comments, want 2", got)
	}
	if got := len(history.revCommentsMap[3]); got != 1 {
		t.Errorf("review 3 has %d inline comments, want 1", got)
	}
	if _, ok := history.revCommentsMap[2]; ok {
		t.Error("review 2 has no inline comments and must not appear in the map")
	}
	if _, ok := history.revCommentsMap[0]; ok {
		t.Error("the review-less comment was filed under review 0, want it dropped")
	}
}

// TestEvaluate_FetchesReferencedIssueOnce covers the memoisation: the label
// sync, the review opt-in check and the readiness check all want the issues the
// pull request closes, and they used to fetch them one after another.
func TestEvaluate_FetchesReferencedIssueOnce(t *testing.T) {
	f := &prFixture{
		num:     10,
		headSHA: "sha-1",
		body:    "Fixes #7",
		updated: time.Now().Add(-time.Hour),
	}
	s := newFixtureScanner(t, f)

	updated := time.Now()
	s.evaluate(context.Background(), &githubv39.Issue{
		Number:           githubv39.Int(10),
		UpdatedAt:        &updated,
		PullRequestLinks: &githubv39.PullRequestLinks{},
		Labels:           []*githubv39.Label{{Name: githubv39.String("factory")}},
	})

	if got := f.count("/issues/7"); got != 1 {
		t.Errorf("fetched referenced issue #7 %d times in one evaluation, want 1", got)
	}
}
