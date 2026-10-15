package repoboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

// countingGitHub answers GET /user and one open PR, whose updated_at the
// test moves, and counts the calls by path.
type countingGitHub struct {
	mu      sync.Mutex
	calls   map[string]int
	updated time.Time
}

func (c *countingGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[req.URL.Path]++
	body := `{}`
	switch {
	case req.URL.Path == "/user":
		body = `{"login": "alice"}`
	case strings.HasSuffix(req.URL.Path, "/pulls"):
		body = fmt.Sprintf(`[{"number": 42, "title": "pr", "updated_at": %q, "labels": []}]`, c.updated.UTC().Format(time.RFC3339))
	case strings.HasSuffix(req.URL.Path, "/reviews"):
		body = `[{"id": 9, "state": "APPROVED", "user": {"login": "alice"}}]`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: req}, nil
}

func (c *countingGitHub) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[path]
}

// The owner's login is asked of GitHub once per token for identityTTL,
// not every discovery.
func TestOwnerLoginIsCachedPerToken(t *testing.T) {
	gh := &countingGitHub{updated: time.Now().Add(-time.Hour)}
	r := newTestReconciler(newFakeLauncher(), clients.NewGitHubClientFromHTTP(&http.Client{Transport: gh}), testBoard(nil), githubSecret())
	for range 3 {
		if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if n := gh.count("/user"); n != 1 {
		t.Errorf("GET /user %d times over three discoveries, want 1", n)
	}
	// Once the login is older than identityTTL, it is asked again.
	r.identities.Store(tokenKey("gho_alice"), cachedIdentity{login: "alice", at: time.Now().Add(-identityTTL - time.Minute)})
	if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
		t.Fatal(err)
	}
	if n := gh.count("/user"); n != 2 {
		t.Errorf("GET /user %d times after the login went stale, want 2", n)
	}
}

// A PR a Review rule found the owner had reviewed is not asked about
// again (ListReviews) until it changes.
func TestReviewRuleRemembersTheOwnersReview(t *testing.T) {
	gh := &countingGitHub{updated: time.Now().Add(-time.Hour)}
	client := clients.NewGitHubClientFromHTTP(&http.Client{Transport: gh})
	r := newTestReconciler(newFakeLauncher(), client, autoReviewBoard(), githubSecret())
	newGithubClientFromToken = func(context.Context, string) *github.Client { return client }
	reviews := "/repos/test/repo/pulls/42/reviews"
	for range 3 {
		if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if n := gh.count(reviews); n != 1 {
		t.Errorf("ListReviews %d times over three discoveries of an unchanged PR, want 1", n)
	}
	if reqs := recipeRequests(t, r); len(reqs) != 0 {
		t.Errorf("filed %d Requests over the owner's review", len(reqs))
	}
	gh.mu.Lock()
	gh.updated = time.Now().Add(-time.Minute)
	gh.mu.Unlock()
	if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
		t.Fatal(err)
	}
	if n := gh.count(reviews); n != 2 {
		t.Errorf("ListReviews %d times after the PR changed, want 2", n)
	}
}
