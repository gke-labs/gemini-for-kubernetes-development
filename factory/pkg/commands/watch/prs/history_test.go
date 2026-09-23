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
	"time"

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

func TestGetLastPRActivityTime(t *testing.T) {
	baseTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pr := &githubv39.PullRequest{
		CreatedAt: &baseTime,
	}

	githubLogin := "factory-bot"
	bots := []string{"allowlisted-bot"}

	// Case 1: No comments/reviews
	got := getLastPRActivityTime(pr, nil, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 1 failed: expected %v, got %v", baseTime, got)
	}

	// Case 2: Human comment on issue
	humanTime := baseTime.Add(1 * time.Hour)
	comments := []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &humanTime,
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(humanTime) {
		t.Errorf("Case 2 failed: expected %v, got %v", humanTime, got)
	}

	// Case 3: Bot comment (ignored)
	botTime := baseTime.Add(2 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("allowlisted-bot")},
			CreatedAt: &botTime,
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 3 failed: expected %v, got %v", baseTime, got)
	}

	// Case 4: Bot pause comment (ignored as it is not human activity)
	pauseTime := baseTime.Add(3 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("factory-bot")},
			CreatedAt: &pauseTime,
			Body:      stringPtr("🤖 AI Factory has paused automated processing on this pull request due to a period of inactivity"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 4 failed: expected %v, got %v", baseTime, got)
	}

	// Case 5: Human review
	reviewTime := baseTime.Add(4 * time.Hour)
	reviews := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(1),
			User:        &githubv39.User{Login: stringPtr("human-user2")},
			SubmittedAt: &reviewTime,
		},
	}
	got = getLastPRActivityTime(pr, nil, reviews, nil, githubLogin, bots, "factory")
	if !got.Equal(reviewTime) {
		t.Errorf("Case 5 failed: expected %v, got %v", reviewTime, got)
	}

	// Case 6: Review comment by human under a bot review
	botReviewTime := baseTime.Add(5 * time.Hour)
	humanReviewCommentTime := baseTime.Add(6 * time.Hour)
	reviews = []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(2),
			User:        &githubv39.User{Login: stringPtr("factory-bot")},
			SubmittedAt: &botReviewTime,
		},
	}
	revComments := map[int64][]*githubv39.PullRequestComment{
		2: {
			{
				User:      &githubv39.User{Login: stringPtr("human-user3")},
				CreatedAt: &humanReviewCommentTime,
			},
		},
	}
	got = getLastPRActivityTime(pr, nil, reviews, revComments, githubLogin, bots, "factory")
	if !got.Equal(humanReviewCommentTime) {
		t.Errorf("Case 6 failed: expected %v, got %v", humanReviewCommentTime, got)
	}

	// Case 7: Human comment with /overseer-ignore (ignored)
	ignoreTime := baseTime.Add(7 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &ignoreTime,
			Body:      stringPtr("/overseer-ignore: This is side-channel conversation"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 7 failed: expected /overseer-ignore comment to be ignored and return %v, got %v", baseTime, got)
	}

	// Case 8: Human comment with /factory-ignore (ignored because triggerLabel is factory)
	factoryIgnoreTime := baseTime.Add(8 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &factoryIgnoreTime,
			Body:      stringPtr("/factory-ignore: This is side-channel conversation with custom prefix"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 8 failed: expected /factory-ignore comment to be ignored when triggerLabel is 'factory' and return %v, got %v", baseTime, got)
	}
}

func TestHasInactivityComment(t *testing.T) {
	baseTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pauseBody := "🤖 AI Factory has paused automated processing on this pull request due to a period of inactivity with no human comments"

	// Case 1: No comments
	if hasInactivityComment(nil, baseTime) {
		t.Errorf("Case 1 failed: expected false for nil comments")
	}

	// Case 2: Inactivity comment posted AFTER lastActivity
	commentAfter := baseTime.Add(2 * time.Hour)
	comments := []*githubv39.IssueComment{
		{
			CreatedAt: &commentAfter,
			Body:      &pauseBody,
		},
	}
	if !hasInactivityComment(comments, baseTime) {
		t.Errorf("Case 2 failed: expected true when pause comment is after lastActivity")
	}

	// Case 3: Inactivity comment posted BEFORE lastActivity (e.g. human commented afterwards)
	humanTimeAfter := baseTime.Add(4 * time.Hour)
	if hasInactivityComment(comments, humanTimeAfter) {
		t.Errorf("Case 3 failed: expected false when pause comment is before lastActivity")
	}

	// Case 4: Other comments with different body
	otherBody := "LGTM"
	otherComments := []*githubv39.IssueComment{
		{
			CreatedAt: &commentAfter,
			Body:      &otherBody,
		},
	}
	if hasInactivityComment(otherComments, baseTime) {
		t.Errorf("Case 4 failed: expected false for non-pause comment")
	}
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
