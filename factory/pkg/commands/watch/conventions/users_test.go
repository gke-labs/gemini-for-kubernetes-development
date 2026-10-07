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

func TestIsFeedbackAuthor(t *testing.T) {
	selfLogin := "factory-bot"
	allowlistedBots := []string{"trusted-bot"}
	reviewerLogins := []string{"gemini-code-assist[bot]"}
	// As config.FactoryConfig.TrustedLogins builds it: allowlisted users,
	// then the allowlisted bots and reviewer accounts.
	trustedLogins := []string{"private-member", "trusted-bot", "gemini-code-assist[bot]"}

	tests := []struct {
		name        string
		user        *githubv39.User
		association string
		expected    bool
	}{
		{"collaborator", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "COLLABORATOR", true},
		{"member", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "MEMBER", true},
		{"owner", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "OWNER", true},
		// Without write access, a human's feedback is not acted on.
		{"stranger", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "NONE", false},
		{"contributor", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "CONTRIBUTOR", false},
		{"no association", &githubv39.User{Login: stringPtr("human-dev"), Type: stringPtr("User")}, "", false},
		// A configured allowlisted user counts whatever GitHub reports.
		{"allowlisted user", &githubv39.User{Login: stringPtr("Private-Member"), Type: stringPtr("User")}, "NONE", true},
		{"self", &githubv39.User{Login: stringPtr("factory-bot")}, "COLLABORATOR", false},
		{"allowlisted bot", &githubv39.User{Login: stringPtr("trusted-bot"), Type: stringPtr("Bot")}, "NONE", true},
		{"unallowlisted bot", &githubv39.User{Login: stringPtr("untrusted-bot"), Type: stringPtr("Bot")}, "NONE", false},
		// A review bot counts even though it is an automated account that was
		// not allowlisted.
		{"reviewer bot", &githubv39.User{Login: stringPtr("gemini-code-assist[bot]"), Type: stringPtr("Bot")}, "NONE", true},
		// The 'reviewbot' name fallback makes an account a reviewer, but
		// grants no trust on its own.
		{"reviewbot lookalike", &githubv39.User{Login: stringPtr("other-reviewbot"), Type: stringPtr("User")}, "NONE", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsFeedbackAuthor(tc.user, tc.association, selfLogin, allowlistedBots, reviewerLogins, trustedLogins)
			if got != tc.expected {
				t.Errorf("IsFeedbackAuthor(%v, %q) = %v, want %v", tc.user.GetLogin(), tc.association, got, tc.expected)
			}
		})
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
