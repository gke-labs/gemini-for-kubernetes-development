package watch

import (
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

func TestShouldUnassignStaleBot(t *testing.T) {
	tests := []struct {
		name          string
		lastSHA       string
		unassignedSHA string
		headSHA       string
		assignedBot   string
		expected      bool
	}{
		{
			name:          "Should unassign when new commit and not yet unassigned",
			lastSHA:       "old-sha",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      true,
		},
		{
			name:          "Should not unassign when lastSHA is empty",
			lastSHA:       "",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should not unassign when lastSHA matches headSHA",
			lastSHA:       "same-sha",
			unassignedSHA: "",
			headSHA:       "same-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should not unassign when assignedBot is empty",
			lastSHA:       "old-sha",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "",
			expected:      false,
		},
		{
			name:          "Should not unassign when already unassigned for this headSHA",
			lastSHA:       "old-sha",
			unassignedSHA: "new-sha",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should unassign if previously unassigned for a different SHA",
			lastSHA:       "old-sha",
			unassignedSHA: "old-sha2",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldUnassignStaleBot(tc.lastSHA, tc.unassignedSHA, tc.headSHA, tc.assignedBot)
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
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
