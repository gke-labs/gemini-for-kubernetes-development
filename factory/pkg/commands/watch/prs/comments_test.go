package prs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestEvaluateComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	u, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = u

	baseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	lastCommitTime := baseTime
	lastCommentAddressedTime := baseTime.Add(10 * time.Minute)
	pr := &githubv39.PullRequest{
		User: &githubv39.User{Login: stringPtr("pool-bot")},
	}

	tests := []struct {
		name               string
		comments           []*githubv39.IssueComment
		reviews            []*githubv39.PullRequestReview
		lastCommitTime     time.Time
		lastAddressedTime  time.Time
		allBotUsers        []string
		wantHasNewComments bool
	}{
		{
			name: "human comment after lastCommentAddressedTime triggers hasNewComments",
			comments: []*githubv39.IssueComment{
				{
					ID:        int64Ptr(1),
					User:      &githubv39.User{Login: stringPtr("alice")},
					Body:      stringPtr("Please fix this"),
					CreatedAt: timePtr(lastCommentAddressedTime.Add(5 * time.Minute)),
				},
			},
			lastCommitTime:     lastCommitTime,
			lastAddressedTime:  lastCommentAddressedTime,
			allBotUsers:        []string{"pool-bot"},
			wantHasNewComments: true,
		},
		{
			name: "human comment before lastCommentAddressedTime is ignored",
			comments: []*githubv39.IssueComment{
				{
					ID:        int64Ptr(1),
					User:      &githubv39.User{Login: stringPtr("alice")},
					Body:      stringPtr("Please fix this"),
					CreatedAt: timePtr(lastCommentAddressedTime.Add(-5 * time.Minute)),
				},
			},
			lastCommitTime:     lastCommitTime,
			lastAddressedTime:  lastCommentAddressedTime,
			allBotUsers:        []string{"pool-bot"},
			wantHasNewComments: false,
		},
		{
			name: "bot review after lastCommentAddressedTime triggers hasNewComments even if same SHA was previously addressed",
			reviews: []*githubv39.PullRequestReview{
				{
					ID:          int64Ptr(10),
					User:        &githubv39.User{Login: stringPtr("gemini-code-assist[bot]")},
					State:       stringPtr("COMMENTED"),
					Body:        stringPtr("Found an issue"),
					SubmittedAt: timePtr(lastCommentAddressedTime.Add(5 * time.Minute)),
				},
			},
			lastCommitTime:     lastCommitTime,
			lastAddressedTime:  lastCommentAddressedTime,
			allBotUsers:        []string{"pool-bot"},
			wantHasNewComments: true,
		},
		{
			name: "unrelated bot comment after human comment does not suppress human comment",
			comments: []*githubv39.IssueComment{
				{
					ID:        int64Ptr(1),
					User:      &githubv39.User{Login: stringPtr("alice")},
					Body:      stringPtr("Please fix this"),
					CreatedAt: timePtr(lastCommentAddressedTime.Add(5 * time.Minute)),
				},
				{
					ID:        int64Ptr(2),
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started addressing PR feedback"),
					CreatedAt: timePtr(lastCommentAddressedTime.Add(6 * time.Minute)),
				},
			},
			lastCommitTime:     lastCommitTime,
			lastAddressedTime:  lastCommentAddressedTime,
			allBotUsers:        []string{"pool-bot"},
			wantHasNewComments: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestScanner(t, t.TempDir(), testOpts{
				GitHub:         ghClient,
				Kube:           newTestKubeClient(),
				BotUsers:       tc.allBotUsers,
				ReviewerLogins: []string{"gemini-code-assist[bot]"},
				TriggerLabel:   "factory",
			})
			res := s.evaluateComments(
				context.Background(),
				pr,
				&prHistory{
					comments: tc.comments,
					reviews:  tc.reviews,
				},
				tc.lastCommitTime,
				tc.lastAddressedTime,
			)
			if res.hasNewComments != tc.wantHasNewComments {
				t.Errorf("hasNewComments = %v, want %v", res.hasNewComments, tc.wantHasNewComments)
			}
		})
	}
}
