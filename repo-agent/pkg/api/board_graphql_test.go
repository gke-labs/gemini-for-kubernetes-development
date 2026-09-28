package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/ghquota"
)

// A real answer from GitHub, captured against gke-labs/open-rl and trimmed
// to two nodes per connection. The point of keeping the genuine article is
// that the rest of the board's tests build their GraphQL out of the REST
// fixtures they were already written in — a helpful fake, and a fake can
// agree with a wrong decoder. This one cannot: a null body, a null author,
// an empty labels connection and the exact key spellings are all GitHub's,
// not ours.
const capturedBoardAnswer = `{
  "data": {
    "rateLimit": { "cost": 9, "remaining": 4969, "resetAt": "2026-09-28T20:11:19Z" },
    "repository": {
      "pullRequests": { "nodes": [
        { "id": "PR_kwDORjgNyc8AAAABFLs9nw", "number": 270,
          "title": "chore(deps): batch open dependabot PRs",
          "body": "Fixes #269", "url": "https://github.com/gke-labs/open-rl/pull/270",
          "updatedAt": "2026-09-25T22:07:13Z", "isDraft": false,
          "author": { "login": "barney-s" },
          "labels": { "nodes": [] },
          "reviewRequests": { "nodes": [] },
          "reviews": { "nodes": [] } },
        { "id": "PR_kwDORjgNyc8AAAABFABdxQ", "number": 266,
          "title": "Read worker parallelism from user_metadata",
          "body": null, "url": "https://github.com/gke-labs/open-rl/pull/266",
          "updatedAt": "2026-09-25T04:34:54Z", "isDraft": true,
          "author": { "login": "ShubyM" },
          "labels": { "nodes": [ { "name": "kind/feature" } ] },
          "reviewRequests": { "nodes": [ { "requestedReviewer": { "login": "barney-s" } } ] },
          "reviews": { "nodes": [ { "state": "PENDING" } ] } }
      ] },
      "assigned": { "nodes": [] },
      "created": { "nodes": [
        { "number": 269, "title": "Batch depandabot PRs into a single PR",
          "url": "https://github.com/gke-labs/open-rl/issues/269",
          "updatedAt": "2026-09-25T21:08:34Z", "author": { "login": "barney-s" },
          "labels": { "nodes": [] }, "assignees": { "nodes": [] }, "body": null }
      ] },
      "triage": { "nodes": [
        { "number": 200, "title": "Use metadata field in the request payload",
          "url": "https://github.com/gke-labs/open-rl/issues/200",
          "updatedAt": "2026-09-20T13:59:52Z", "author": null,
          "labels": { "nodes": [ { "name": "good first issue" }, { "name": "priority:P1" } ] },
          "assignees": { "nodes": [ { "login": "droot" } ] }, "body": null }
      ] }
    }
  }
}`

// answering stands up a fake GitHub that returns one body for the board
// query, and hands back the snapshot the feed would have built from it.
func answering(t *testing.T, status int, body string) (*boardSnapshot, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("the board query must be a POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := githubGraphQLEndpoint
	githubGraphQLEndpoint = srv.URL
	t.Cleanup(func() { githubGraphQLEndpoint = prev })

	return fetchBoardSnapshot(context.Background(), srv.Client(), "gke-labs", "open-rl", "barney-s")
}

func TestTheBoardReadsAnAnswerGitHubActuallySent(t *testing.T) {
	snap, err := answering(t, http.StatusOK, capturedBoardAnswer)
	if err != nil {
		t.Fatalf("decoding a real answer failed: %v", err)
	}

	if len(snap.prs) != 2 {
		t.Fatalf("got %d PRs, want 2", len(snap.prs))
	}
	first := snap.prs[0]
	if first.GetNumber() != 270 {
		t.Errorf("first PR is #%d, want #270", first.GetNumber())
	}
	// The row builders read these off go-github types, so the mapping has
	// to land in the fields they already reach for — html_url, not url.
	if first.GetHTMLURL() != "https://github.com/gke-labs/open-rl/pull/270" {
		t.Errorf("PR html url is %q", first.GetHTMLURL())
	}
	if first.GetUser().GetLogin() != "barney-s" {
		t.Errorf("PR author is %q, want barney-s", first.GetUser().GetLogin())
	}
	if first.GetBody() != "Fixes #269" {
		t.Errorf("PR body is %q — closingRefs reads it to link the issue", first.GetBody())
	}
	if first.GetNodeID() != "PR_kwDORjgNyc8AAAABFLs9nw" {
		t.Errorf("PR node id is %q", first.GetNodeID())
	}
	if first.UpdatedAt == nil || first.GetUpdatedAt().Year() != 2026 {
		t.Errorf("PR updatedAt did not survive: %v", first.UpdatedAt)
	}

	second := snap.prs[1]
	if !second.GetDraft() {
		t.Error("#266 is a draft on GitHub and must arrive as one")
	}
	if len(second.Labels) != 1 || second.Labels[0].GetName() != "kind/feature" {
		t.Errorf("labels did not survive the connection wrapper: %v", second.Labels)
	}
	if len(second.RequestedReviewers) != 1 || second.RequestedReviewers[0].GetLogin() != "barney-s" {
		t.Errorf("requested reviewers did not survive: %v", second.RequestedReviewers)
	}
	// A null body is ordinary on GitHub and must not be an error.
	if second.GetBody() != "" {
		t.Errorf("a null body should read as empty, got %q", second.GetBody())
	}

	// The review states are the whole reason this query exists: they used
	// to cost one REST call per PR.
	if st := snap.reviews[266]; !st.pending || st.reviewed {
		t.Errorf("#266 has a parked pending review; got pending=%v reviewed=%v", st.pending, st.reviewed)
	}
	if st := snap.reviews[270]; st.pending || st.reviewed {
		t.Errorf("#270 has no review by the member; got pending=%v reviewed=%v", st.pending, st.reviewed)
	}

	if len(snap.created) != 1 || snap.created[0].GetNumber() != 269 {
		t.Errorf("created issues did not survive: %v", snap.created)
	}
	if len(snap.assigned) != 0 {
		t.Errorf("assigned should be empty, got %d", len(snap.assigned))
	}
	if len(snap.triage) != 1 {
		t.Fatalf("got %d triage issues, want 1", len(snap.triage))
	}
	tri := snap.triage[0]
	// A ghost account reports a null author, and the issue still belongs
	// on the board.
	if tri.User != nil {
		t.Errorf("a null author should stay nil, got %v", tri.User)
	}
	if len(tri.Assignees) != 1 || tri.Assignees[0].GetLogin() != "droot" {
		t.Errorf("assignees did not survive: %v", tri.Assignees)
	}
	if len(tri.Labels) != 2 {
		t.Errorf("got %d labels, want 2", len(tri.Labels))
	}
	// An issue from repository.issues is never a pull request, which is
	// what lets the feed stop asking.
	if tri.IsPullRequest() {
		t.Error("a GraphQL issue must not look like a pull request")
	}
}

func TestSubmittedReviewsReadAsReviewed(t *testing.T) {
	const body = `{"data":{"rateLimit":{"cost":1,"remaining":4999},"repository":{
		"pullRequests":{"nodes":[
			{"number":1,"reviews":{"nodes":[{"state":"APPROVED"}]}},
			{"number":2,"reviews":{"nodes":[{"state":"CHANGES_REQUESTED"},{"state":"PENDING"}]}},
			{"number":3,"reviews":{"nodes":[{"state":"DISMISSED"}]}}]},
		"assigned":{"nodes":[]},"created":{"nodes":[]},"triage":{"nodes":[]}}}}`
	snap, err := answering(t, http.StatusOK, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st := snap.reviews[1]; !st.reviewed || st.pending {
		t.Errorf("#1 was approved: got pending=%v reviewed=%v", st.pending, st.reviewed)
	}
	// Both at once is real: a submitted review last week, a new one parked
	// today. The row has to know about the parked one.
	if st := snap.reviews[2]; !st.reviewed || !st.pending {
		t.Errorf("#2 is both reviewed and pending: got pending=%v reviewed=%v", st.pending, st.reviewed)
	}
	// A dismissed review is neither — it no longer counts as done.
	if st := snap.reviews[3]; st.reviewed || st.pending {
		t.Errorf("#3 was dismissed: got pending=%v reviewed=%v", st.pending, st.reviewed)
	}
}

// GraphQL answers a spent budget with 200 and an error in the body, so the
// transport gate never sees a 403 and never records the hold. If this did
// not translate, the feed would treat exhaustion as an ordinary failure and
// keep hammering.
func TestASpentGraphQLBudgetReadsAsRateLimited(t *testing.T) {
	reset := time.Now().Add(42 * time.Minute).UTC().Format(time.RFC3339)
	body := `{"data":{"rateLimit":{"cost":0,"remaining":0,"resetAt":"` + reset + `"}},
		"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`
	_, err := answering(t, http.StatusOK, body)
	if err == nil {
		t.Fatal("a spent budget must be an error")
	}
	if !ghquota.IsRateLimited(err) {
		t.Fatalf("a spent budget must read as rate limited, got %v", err)
	}
	until, ok := ghquota.ResetAt(err)
	if !ok {
		t.Fatal("the reset must come through so the board can hold until then")
	}
	if wait := time.Until(until); wait < 40*time.Minute {
		t.Errorf("reset is %v away, want GitHub's ~42 minutes", wait)
	}
}

// One query means there is no such thing as a partial board any more. A
// half-answer that rendered would show fewer rows than exist, and "nothing
// needs you" is the one wrong answer a work queue must not give — so the
// rebuild fails and the caller keeps serving the feed it already had.
func TestAPartialAnswerFailsTheRebuild(t *testing.T) {
	const body = `{"data":{"repository":{"pullRequests":{"nodes":[{"number":1}]},
		"assigned":{"nodes":[]},"created":{"nodes":[]},"triage":null}},
		"errors":[{"type":"SERVICE_UNAVAILABLE","message":"timed out reading issues"}]}`
	snap, err := answering(t, http.StatusOK, body)
	if err == nil {
		t.Fatalf("a partial answer must fail the rebuild, got %d PRs", len(snap.prs))
	}
	if ghquota.IsRateLimited(err) {
		t.Errorf("a service failure is not a rate limit: %v", err)
	}
}

func TestAnHTTPFailureIsNotAnEmptyBoard(t *testing.T) {
	_, err := answering(t, http.StatusBadGateway, `<html>bad gateway</html>`)
	if err == nil {
		t.Fatal("a 502 must fail the rebuild rather than empty the board")
	}
}
