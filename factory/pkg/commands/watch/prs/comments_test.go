package prs

import (
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

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
			count := getInvestigationCount(tc.comments, lastCommitTime, tc.allBotUsers, tc.githubLogin, tc.allowlist, "factory")
			if count != tc.expectedCount {
				t.Errorf("expected count %d, got %d", tc.expectedCount, count)
			}
		})
	}
}
