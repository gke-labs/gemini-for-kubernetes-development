package watch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string {
	return &s
}

func TestGetMissingLabelsForPR(t *testing.T) {
	tests := []struct {
		name      string
		prLabels  []string
		refIssues [][]string
		expected  []string
	}{
		{
			name:      "All issue labels are missing from PR",
			prLabels:  []string{},
			refIssues: [][]string{{"greenfield", "step/controller"}},
			expected:  []string{"greenfield", "step/controller"},
		},
		{
			name:      "Some labels already exist on PR",
			prLabels:  []string{"greenfield"},
			refIssues: [][]string{{"greenfield", "step/controller", "area/direct"}},
			expected:  []string{"greenfield", "step/controller", "area/direct"},
		},
		{
			name:     "Duplicate labels across multiple issues are deduplicated",
			prLabels: []string{"priority/medium"},
			refIssues: [][]string{
				{"greenfield", "step/controller"},
				{"step/controller", "area/direct"},
			},
			expected: []string{"priority/medium", "greenfield", "step/controller", "area/direct"},
		},
		{
			name:      "No missing labels",
			prLabels:  []string{"greenfield", "step/controller"},
			refIssues: [][]string{{"greenfield"}},
			expected:  []string{"greenfield", "step/controller"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var prLabels []*githubv39.Label
			for _, name := range tc.prLabels {
				prLabels = append(prLabels, &githubv39.Label{Name: stringPtr(name)})
			}

			var refIssues []*githubv39.Issue
			for _, issueLabels := range tc.refIssues {
				var labels []*githubv39.Label
				for _, name := range issueLabels {
					labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
				}
				refIssues = append(refIssues, &githubv39.Issue{Labels: labels})
			}

			got := getMissingLabelsForPR(prLabels, refIssues)

			// Build the final set of labels on the PR (original labels + added labels)
			finalLabelsMap := make(map[string]bool)
			var finalLabels []string
			for _, name := range tc.prLabels {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}
			for _, name := range got {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}

			if len(finalLabels) != len(tc.expected) {
				t.Fatalf("Final labels list length is %d (%v); want %d (%v)", len(finalLabels), finalLabels, len(tc.expected), tc.expected)
			}
			for i, val := range tc.expected {
				if finalLabels[i] != val {
					t.Errorf("Final label at index %d = %q; want %q", i, finalLabels[i], val)
				}
			}
		})
	}
}

func TestGetInvestigationCount(t *testing.T) {
	tests := []struct {
		name          string
		comments      []*githubv39.IssueComment
		allBotUsers   []string
		githubLogin   string
		allowlist     []string
		expectedCount int
	}{
		{
			name:          "No comments, should be 0",
			comments:      []*githubv39.IssueComment{},
			expectedCount: 0,
		},
		{
			name: "Only bot investigate comments, should be counted",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 2,
		},
		{
			name: "Prow comments should not reset the circuit breaker",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-3 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("google-oss-prow"), Type: stringPtr("Bot")},
					Body:      stringPtr("Some prow CI failure"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 2,
		},
		{
			name: "Human comments should reset the circuit breaker",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-3 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("real-human"), Type: stringPtr("User")},
					Body:      stringPtr("Can you look into this?"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lastCommitTime := time.Now().Add(-24 * time.Hour)
			count := getInvestigationCount(tc.comments, lastCommitTime, tc.allBotUsers, tc.githubLogin, tc.allowlist)
			if count != tc.expectedCount {
				t.Errorf("expected count %d, got %d", tc.expectedCount, count)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestGetLastPRActivityTime(t *testing.T) {
	baseTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pr := &githubv39.PullRequest{
		CreatedAt: &baseTime,
	}

	githubLogin := "factory-bot"
	bots := []string{"allowlisted-bot"}

	// Case 1: No comments/reviews
	got := getLastPRActivityTime(pr, nil, nil, nil, githubLogin, bots)
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
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
	got = getLastPRActivityTime(pr, nil, reviews, nil, githubLogin, bots)
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
	got = getLastPRActivityTime(pr, nil, reviews, revComments, githubLogin, bots)
	if !got.Equal(humanReviewCommentTime) {
		t.Errorf("Case 6 failed: expected %v, got %v", humanReviewCommentTime, got)
	}
}

func int64Ptr(i int64) *int64 {
	return &i
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

func TestIsReviewerBot(t *testing.T) {
	loginReviewBot := "reviewbot-robot"
	userReviewBot := &githubv39.User{Login: &loginReviewBot}
	loginCoderBot := "neumann-coder-bot"
	userCoderBot := &githubv39.User{Login: &loginCoderBot}

	cfg := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"reviewer": {Users: []string{"reviewbot-robot"}},
		},
	}

	if !isReviewerBot(userReviewBot, cfg) {
		t.Errorf("expected reviewbot-robot to be identified as reviewer bot")
	}
	if isReviewerBot(userCoderBot, cfg) {
		t.Errorf("expected neumann-coder-bot to not be identified as reviewer bot")
	}
}

func TestShouldIgnoreUser(t *testing.T) {
	selfLogin := "factory-bot"
	allowlistedBots := []string{"trusted-bot"}

	tests := []struct {
		user     *githubv39.User
		expected bool
	}{
		{nil, false},
		{&githubv39.User{Login: stringPtr("factory-bot")}, true},
		{&githubv39.User{Login: stringPtr("trusted-bot"), Type: stringPtr("Bot")}, false},
		{&githubv39.User{Login: stringPtr("untrusted-bot"), Type: stringPtr("Bot")}, true},
		{&githubv39.User{Login: stringPtr("some-user[bot]")}, true},
		{&githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, false},
	}

	for _, tc := range tests {
		got := shouldIgnoreUser(tc.user, selfLogin, allowlistedBots)
		if got != tc.expected {
			t.Errorf("shouldIgnoreUser(%v) = %v, want %v", tc.user, got, tc.expected)
		}
	}
}

func TestHasStopLabel(t *testing.T) {
	labelsWithOverseerStop := []*githubv39.Label{{Name: stringPtr("overseer/stop")}}
	labelsWithCustomStop := []*githubv39.Label{{Name: stringPtr("mybot/stop")}}
	labelsWithoutStop := []*githubv39.Label{{Name: stringPtr("bug")}}

	if !hasStopLabel(labelsWithOverseerStop, "") {
		t.Errorf("expected hasStopLabel with overseer/stop to be true")
	}
	if !hasStopLabel(labelsWithCustomStop, "mybot") {
		t.Errorf("expected hasStopLabel with mybot/stop and triggerLabel=mybot to be true")
	}
	if hasStopLabel(labelsWithoutStop, "mybot") {
		t.Errorf("expected hasStopLabel with no stop label to be false")
	}
}

func TestHasReviewLabel(t *testing.T) {
	labelsWithOverseerReview := []*githubv39.Label{{Name: stringPtr("overseer/review")}}
	labelsWithCustomReview := []*githubv39.Label{{Name: stringPtr("mybot/review")}}
	labelsWithoutReview := []*githubv39.Label{{Name: stringPtr("bug")}}

	if !hasReviewLabel(labelsWithOverseerReview, "") {
		t.Errorf("expected hasReviewLabel with overseer/review to be true")
	}
	if !hasReviewLabel(labelsWithCustomReview, "mybot") {
		t.Errorf("expected hasReviewLabel with mybot/review and triggerLabel=mybot to be true")
	}
	if hasReviewLabel(labelsWithoutReview, "mybot") {
		t.Errorf("expected hasReviewLabel with no review label to be false")
	}
}

func TestGetStopLabel(t *testing.T) {
	if getStopLabel("") != "overseer/stop" {
		t.Errorf("getStopLabel(\"\") = %q, want 'overseer/stop'", getStopLabel(""))
	}
	if getStopLabel("mybot") != "mybot/stop" {
		t.Errorf("getStopLabel(\"mybot\") = %q, want 'mybot/stop'", getStopLabel("mybot"))
	}
}

func TestAssignedBotUser(t *testing.T) {
	issue := &githubv39.Issue{
		Assignees: []*githubv39.User{
			{Login: stringPtr("human-user")},
			{Login: stringPtr("bot-1")},
		},
	}
	botUsers := []string{"bot-1", "bot-2"}
	got := assignedBotUser(issue, botUsers)
	if got != "bot-1" {
		t.Errorf("assignedBotUser = %q, want 'bot-1'", got)
	}

	gotNone := assignedBotUser(issue, []string{"other-bot"})
	if gotNone != "" {
		t.Errorf("assignedBotUser = %q, want empty", gotNone)
	}
}

func TestGetReadyForHumanLabel(t *testing.T) {
	if getReadyForHumanLabel("") != "overseer/ready-for-human" {
		t.Errorf("getReadyForHumanLabel(\"\") = %q, want 'overseer/ready-for-human'", getReadyForHumanLabel(""))
	}
	if getReadyForHumanLabel("overseer") != "overseer/ready-for-human" {
		t.Errorf("getReadyForHumanLabel(\"overseer\") = %q, want 'overseer/ready-for-human'", getReadyForHumanLabel("overseer"))
	}
	if getReadyForHumanLabel("mybot") != "mybot/ready-for-human" {
		t.Errorf("getReadyForHumanLabel(\"mybot\") = %q, want 'mybot/ready-for-human'", getReadyForHumanLabel("mybot"))
	}
}

func TestHasReadyForHumanLabel(t *testing.T) {
	labelsWithOverseer := []*githubv39.Label{{Name: stringPtr("overseer/ready-for-human")}}
	labelsWithCustom := []*githubv39.Label{{Name: stringPtr("mybot/ready-for-human")}}
	labelsWithout := []*githubv39.Label{{Name: stringPtr("bug")}}

	if !hasReadyForHumanLabel(labelsWithOverseer, "") {
		t.Errorf("expected hasReadyForHumanLabel with overseer/ready-for-human to be true")
	}
	if !hasReadyForHumanLabel(labelsWithCustom, "mybot") {
		t.Errorf("expected hasReadyForHumanLabel with mybot/ready-for-human and triggerLabel=mybot to be true")
	}
	if !hasReadyForHumanLabel(labelsWithOverseer, "mybot") {
		t.Errorf("expected hasReadyForHumanLabel with fallback overseer/ready-for-human to be true")
	}
	if hasReadyForHumanLabel(labelsWithout, "mybot") {
		t.Errorf("expected hasReadyForHumanLabel with no ready-for-human label to be false")
	}
}

func TestHasCompletedBotReviewOnHead(t *testing.T) {
	cfg := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"reviewer": {Users: []string{"custom-reviewbot"}},
		},
	}
	now := time.Now()
	commitTime := now.Add(-10 * time.Minute)
	headSHA := "abc1234"

	// 1. Review after last commit with COMMENTED
	reviews1 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			SubmittedAt: &time.Time{},
			State:       stringPtr("COMMENTED"),
		},
	}
	*reviews1[0].SubmittedAt = now.Add(-5 * time.Minute)
	if !hasCompletedBotReviewOnHead(reviews1, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with recent review to be true")
	}

	// 2. Review matching headSHA with APPROVED
	reviews2 := []*githubv39.PullRequestReview{
		{
			ID:          githubv39.Int64(2),
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("APPROVED"),
		},
	}
	*reviews2[0].SubmittedAt = now.Add(-15 * time.Minute)
	if !hasCompletedBotReviewOnHead(reviews2, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with matching headSHA to be true")
	}

	// 3. Review with CHANGES_REQUESTED
	reviews3 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("CHANGES_REQUESTED"),
		},
	}
	*reviews3[0].SubmittedAt = now.Add(-5 * time.Minute)
	if hasCompletedBotReviewOnHead(reviews3, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with CHANGES_REQUESTED to be false")
	}

	// 4. Review by non-reviewer bot
	reviews4 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("coder-bot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("APPROVED"),
		},
	}
	*reviews4[0].SubmittedAt = now.Add(-5 * time.Minute)
	if hasCompletedBotReviewOnHead(reviews4, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with non-reviewer bot to be false")
	}

	// 5. Review before last commit and differing SHA
	reviews5 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			CommitID:    stringPtr("oldsha123"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("APPROVED"),
		},
	}
	*reviews5[0].SubmittedAt = now.Add(-20 * time.Minute)
	if hasCompletedBotReviewOnHead(reviews5, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with stale review on old SHA to be false")
	}

	// 6. Multiple reviews: first CHANGES_REQUESTED, later COMMENTED/APPROVED
	reviews6 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("CHANGES_REQUESTED"),
		},
		{
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("APPROVED"),
		},
	}
	*reviews6[0].SubmittedAt = now.Add(-10 * time.Minute)
	*reviews6[1].SubmittedAt = now.Add(-2 * time.Minute)
	if !hasCompletedBotReviewOnHead(reviews6, nil, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with latest APPROVED to be true")
	}

	// 7. Review with inline comments in revCommentsMap should be false
	reviews7 := []*githubv39.PullRequestReview{
		{
			ID:          githubv39.Int64(7),
			User:        &githubv39.User{Login: stringPtr("custom-reviewbot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("COMMENTED"),
		},
	}
	*reviews7[0].SubmittedAt = now.Add(-5 * time.Minute)
	revCommentsMap := map[int64][]*githubv39.PullRequestComment{
		7: {
			{
				ID:   githubv39.Int64(100),
				Body: stringPtr("Please fix this inline comment"),
			},
		},
	}
	if hasCompletedBotReviewOnHead(reviews7, revCommentsMap, headSHA, commitTime, cfg) {
		t.Errorf("expected hasCompletedBotReviewOnHead with inline comments to be false")
	}
}

func TestReconcileReadyForHumanLabel(t *testing.T) {
	type apiCall struct {
		method string
		path   string
		body   string
	}

	tests := []struct {
		name           string
		triggerLabel   string
		isReady        bool
		existingLabels []string
		dryRun         bool
		nilClient      bool
		expectedCalls  []apiCall
	}{
		{
			name:           "Ready without label adds overseer/ready-for-human",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"bug"},
			expectedCalls: []apiCall{
				{
					method: "POST",
					path:   "/repos/test-owner/test-repo/issues/100/labels",
					body:   `["overseer/ready-for-human"]`,
				},
			},
		},
		{
			name:           "Not ready with label removes overseer/ready-for-human",
			triggerLabel:   "",
			isReady:        false,
			existingLabels: []string{"bug", "overseer/ready-for-human"},
			expectedCalls: []apiCall{
				{
					method: "DELETE",
					path:   "/repos/test-owner/test-repo/issues/100/labels/overseer/ready-for-human",
				},
			},
		},
		{
			name:           "Custom trigger label adds custom/ready-for-human",
			triggerLabel:   "mybot",
			isReady:        true,
			existingLabels: []string{"bug"},
			expectedCalls: []apiCall{
				{
					method: "POST",
					path:   "/repos/test-owner/test-repo/issues/100/labels",
					body:   `["mybot/ready-for-human"]`,
				},
			},
		},
		{
			name:           "Custom trigger label removes custom/ready-for-human",
			triggerLabel:   "mybot",
			isReady:        false,
			existingLabels: []string{"mybot/ready-for-human"},
			expectedCalls: []apiCall{
				{
					method: "DELETE",
					path:   "/repos/test-owner/test-repo/issues/100/labels/mybot/ready-for-human",
				},
			},
		},
		{
			name:           "Idempotent: Ready and already has label makes 0 API calls",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"overseer/ready-for-human"},
			expectedCalls:  nil,
		},
		{
			name:           "Idempotent: Not ready and already without label makes 0 API calls",
			triggerLabel:   "",
			isReady:        false,
			existingLabels: []string{"bug"},
			expectedCalls:  nil,
		},
		{
			name:           "Dry-run mode makes 0 API calls",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"bug"},
			dryRun:         true,
			expectedCalls:  nil,
		},
		{
			name:           "Nil GitHub client does not panic and makes 0 calls",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"bug"},
			nilClient:      true,
			expectedCalls:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var recordedCalls []apiCall
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bodyBytes, _ := io.ReadAll(r.Body)
				recordedCalls = append(recordedCalls, apiCall{
					method: r.Method,
					path:   r.URL.Path,
					body:   strings.TrimSpace(string(bodyBytes)),
				})
				if r.Method == "POST" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[{"name":"test"}]`))
				} else if r.Method == "DELETE" {
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			var ghClient *githubv39.Client
			if !tc.nilClient {
				ghClient = githubv39.NewClient(nil)
				ghClient.BaseURL, _ = url.Parse(server.URL + "/")
			}

			w := &Watcher{
				Flags: Flags{
					DryRun: tc.dryRun,
					Repo: RepoFlag{
						Owner: "test-owner",
						Repo:  "test-repo",
					},
				},
				ghClient:     ghClient,
				triggerLabel: tc.triggerLabel,
			}

			prNum := 100
			var labels []*githubv39.Label
			for _, l := range tc.existingLabels {
				labels = append(labels, &githubv39.Label{Name: stringPtr(l)})
			}
			prIssue := &githubv39.Issue{
				Number: &prNum,
				Labels: labels,
			}

			w.reconcileReadyForHumanLabel(context.Background(), prNum, prIssue, tc.isReady, "sha123")

			if len(recordedCalls) != len(tc.expectedCalls) {
				t.Fatalf("recorded %d API calls (%v); want %d (%v)", len(recordedCalls), recordedCalls, len(tc.expectedCalls), tc.expectedCalls)
			}
			for i, exp := range tc.expectedCalls {
				got := recordedCalls[i]
				if got.method != exp.method {
					t.Errorf("call [%d] method = %s; want %s", i, got.method, exp.method)
				}
				if got.path != exp.path {
					t.Errorf("call [%d] path = %s; want %s", i, got.path, exp.path)
				}
				if exp.body != "" {
					var gotJSON, expJSON interface{}
					_ = json.Unmarshal([]byte(got.body), &gotJSON)
					_ = json.Unmarshal([]byte(exp.body), &expJSON)
					if got.body != exp.body {
						t.Errorf("call [%d] body = %s; want %s", i, got.body, exp.body)
					}
				}
			}
		})
	}
}
