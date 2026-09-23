package conventions

import (
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string { return &s }

func TestIsReviewerBot(t *testing.T) {
	loginReviewBot := "reviewbot-robot"
	userReviewBot := &githubv39.User{Login: &loginReviewBot}
	loginCoderBot := "neumann-coder-bot"
	userCoderBot := &githubv39.User{Login: &loginCoderBot}

	reviewerLogins := []string{"reviewbot-robot"}

	if !IsReviewerBot(userReviewBot, reviewerLogins) {
		t.Errorf("expected reviewbot-robot to be identified as reviewer bot")
	}
	if IsReviewerBot(userCoderBot, reviewerLogins) {
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
		got := ShouldIgnoreUser(tc.user, selfLogin, allowlistedBots)
		if got != tc.expected {
			t.Errorf("ShouldIgnoreUser(%v) = %v, want %v", tc.user, got, tc.expected)
		}
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
	got := AssignedBotUser(issue, botUsers)
	if got != "bot-1" {
		t.Errorf("assignedBotUser = %q, want 'bot-1'", got)
	}

	gotNone := AssignedBotUser(issue, []string{"other-bot"})
	if gotNone != "" {
		t.Errorf("assignedBotUser = %q, want empty", gotNone)
	}
}
