package conventions

import (
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string { return &s }

func TestHasIgnorePrefix(t *testing.T) {
	tests := []struct {
		body         string
		triggerLabel string
		expected     bool
	}{
		{
			body:         "/overseer-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /OVERSEER-IGNORE: some message  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/factory-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /FACTORY-IGNORE: custom prefix  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/other-ignore",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "overseer",
			expected:     true,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "",
			expected:     true,
		},
		{
			body:         "just a regular comment",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "line 1\n/overseer-ignore\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  /FACTORY-IGNORE: some message\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  some comment containing /overseer-ignore but not at start",
			triggerLabel: "factory",
			expected:     false,
		},
	}

	for _, tc := range tests {
		got := HasIgnorePrefix(tc.body, tc.triggerLabel)
		if got != tc.expected {
			t.Errorf("HasIgnorePrefix(%q, %q) = %v; expected %v", tc.body, tc.triggerLabel, got, tc.expected)
		}
	}
}

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
