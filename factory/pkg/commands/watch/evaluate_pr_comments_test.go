package watch

import (
	"context"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestEvaluatePRComments_PassingBotReview(t *testing.T) {
	w := &Watcher{
		githubLogin: "test-bot",
	}

	num := 123
	pr := &githubv39.PullRequest{
		User: &githubv39.User{Login: stringPtr("developer-bot")},
	}
	headSHA := "f3739abd"

	now := time.Now()
	lastCommitTime := now.Add(-10 * time.Minute)
	lastCommentAddressedTime := now.Add(-5 * time.Minute)

	// A passing bot review (state is COMMENT, no inline comments, no changes requested)
	reviews := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(1),
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			SubmittedAt: &now,
			State:       stringPtr("COMMENT"),
			Body:        stringPtr("KCC Auto-Review Results: Pass"),
		},
	}
	revCommentsMap := map[int64][]*githubv39.PullRequestComment{}

	analysis := w.evaluatePRComments(
		context.Background(),
		num,
		pr,
		nil, // no issue comments
		reviews,
		revCommentsMap,
		lastCommitTime,
		lastCommentAddressedTime,
		"",
		headSHA,
		nil,
	)

	if analysis.hasNewComments {
		t.Errorf("expected hasNewComments to be false for a passing bot review with no inline comments")
	}
}

func TestEvaluatePRComments_ActionableBotReviewWithInlineComments(t *testing.T) {
	w := &Watcher{
		githubLogin: "test-bot",
	}

	num := 123
	pr := &githubv39.PullRequest{
		User: &githubv39.User{Login: stringPtr("developer-bot")},
	}
	headSHA := "f3739abd"

	now := time.Now()
	lastCommitTime := now.Add(-10 * time.Minute)
	lastCommentAddressedTime := now.Add(-5 * time.Minute)

	// An actionable bot review with inline comments
	reviews := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(1),
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			SubmittedAt: &now,
			State:       stringPtr("COMMENT"),
			Body:        stringPtr("KCC Auto-Review Results: Fail"),
		},
	}
	revCommentsMap := map[int64][]*githubv39.PullRequestComment{
		1: {
			{
				ID:        int64Ptr(100),
				User:      &githubv39.User{Login: stringPtr("reviewbot-robot")},
				CreatedAt: &now,
				Body:      stringPtr("Please add test coverage"),
			},
		},
	}

	analysis := w.evaluatePRComments(
		context.Background(),
		num,
		pr,
		nil,
		reviews,
		revCommentsMap,
		lastCommitTime,
		lastCommentAddressedTime,
		"",
		headSHA,
		nil,
	)

	if !analysis.hasNewComments {
		t.Errorf("expected hasNewComments to be true for an actionable bot review with inline comments")
	}
}

func TestEvaluatePRComments_PreciseSHAGuardrail(t *testing.T) {
	w := &Watcher{
		githubLogin: "test-bot",
	}

	num := 123
	pr := &githubv39.PullRequest{
		User: &githubv39.User{Login: stringPtr("developer-bot")},
	}
	headSHA := "f3739abd"

	now := time.Now()
	lastCommitTime := now.Add(-30 * time.Minute)

	// Scenario A: A review with comments was submitted BEFORE lastCommentAddressedTime.
	// Since lastCommentAddressedSHA == headSHA, it should skip it.
	lastCommentAddressedTimeA := now.Add(-5 * time.Minute)
	reviewTimeA := now.Add(-10 * time.Minute)

	reviewsA := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(1),
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			SubmittedAt: &reviewTimeA,
			State:       stringPtr("COMMENT"),
			Body:        stringPtr("Fail"),
		},
	}
	revCommentsMapA := map[int64][]*githubv39.PullRequestComment{
		1: {
			{
				ID:        int64Ptr(100),
				User:      &githubv39.User{Login: stringPtr("reviewbot-robot")},
				CreatedAt: &reviewTimeA,
				Body:      stringPtr("Inline comment"),
			},
		},
	}

	analysisA := w.evaluatePRComments(
		context.Background(),
		num,
		pr,
		nil,
		reviewsA,
		revCommentsMapA,
		lastCommitTime,
		lastCommentAddressedTimeA,
		headSHA,
		headSHA,
		nil,
	)

	if analysisA.hasNewComments {
		t.Errorf("expected hasNewComments to be false when review is before lastCommentAddressedTime and lastCommentAddressedSHA matches headSHA")
	}

	// Scenario B: A NEW review with comments was submitted AFTER lastCommentAddressedTime.
	// Even though lastCommentAddressedSHA == headSHA, it should NOT skip it because it is a new review!
	lastCommentAddressedTimeB := now.Add(-15 * time.Minute)
	reviewTimeB := now.Add(-5 * time.Minute)

	reviewsB := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(2),
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			SubmittedAt: &reviewTimeB,
			State:       stringPtr("COMMENT"),
			Body:        stringPtr("Fail"),
		},
	}
	revCommentsMapB := map[int64][]*githubv39.PullRequestComment{
		2: {
			{
				ID:        int64Ptr(101),
				User:      &githubv39.User{Login: stringPtr("reviewbot-robot")},
				CreatedAt: &reviewTimeB,
				Body:      stringPtr("New inline comment"),
			},
		},
	}

	analysisB := w.evaluatePRComments(
		context.Background(),
		num,
		pr,
		nil,
		reviewsB,
		revCommentsMapB,
		lastCommitTime,
		lastCommentAddressedTimeB,
		headSHA,
		headSHA,
		nil,
	)

	if !analysisB.hasNewComments {
		t.Errorf("expected hasNewComments to be true when a new review is submitted after lastCommentAddressedTime, even if lastCommentAddressedSHA matches headSHA")
	}
}
