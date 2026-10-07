package prs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
					User:              &githubv39.User{Login: stringPtr("real-human"), Type: stringPtr("User")},
					AuthorAssociation: stringPtr("COLLABORATOR"),
					Body:              stringPtr("Can you look into this?"),
					CreatedAt:         timePtr(time.Now().Add(-2 * time.Hour)),
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
		{
			name: "Untrusted comments should not reset the circuit breaker",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-3 * time.Hour)),
				},
				{
					User:              &githubv39.User{Login: stringPtr("stranger"), Type: stringPtr("User")},
					AuthorAssociation: stringPtr("NONE"),
					Body:              stringPtr("Try again!"),
					CreatedAt:         timePtr(time.Now().Add(-2 * time.Hour)),
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lastCommitTime := time.Now().Add(-24 * time.Hour)
			count := getInvestigationCount(tc.comments, lastCommitTime, tc.allBotUsers, tc.githubLogin, tc.allowlist, nil, "factory")
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
					ID:                int64Ptr(1),
					User:              &githubv39.User{Login: stringPtr("alice")},
					AuthorAssociation: stringPtr("MEMBER"),
					Body:              stringPtr("Please fix this"),
					CreatedAt:         timePtr(lastCommentAddressedTime.Add(5 * time.Minute)),
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
					ID:                int64Ptr(1),
					User:              &githubv39.User{Login: stringPtr("alice")},
					AuthorAssociation: stringPtr("MEMBER"),
					Body:              stringPtr("Please fix this"),
					CreatedAt:         timePtr(lastCommentAddressedTime.Add(-5 * time.Minute)),
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
					ID:                int64Ptr(1),
					User:              &githubv39.User{Login: stringPtr("alice")},
					AuthorAssociation: stringPtr("MEMBER"),
					Body:              stringPtr("Please fix this"),
					CreatedAt:         timePtr(lastCommentAddressedTime.Add(5 * time.Minute)),
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

// TestEvaluateComments_CollectsReviewNodeIDs covers the reviews that get
// acknowledged when the task is queued: only the ones whose body counts as
// feedback, and only by node ID, the handle reactions on reviews need.
func TestEvaluateComments_CollectsReviewNodeIDs(t *testing.T) {
	baseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	after := timePtr(baseTime.Add(5 * time.Minute))
	pr := &githubv39.PullRequest{User: &githubv39.User{Login: stringPtr("pool-bot")}}

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"pool-bot"},
		TriggerLabel: "factory",
	})
	res := s.evaluateComments(context.Background(), pr, &prHistory{
		reviews: []*githubv39.PullRequestReview{
			{ID: int64Ptr(1), NodeID: stringPtr("PRR_changes"), User: &githubv39.User{Login: stringPtr("alice")}, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("CHANGES_REQUESTED"), Body: stringPtr("Please rework"), SubmittedAt: after},
			{ID: int64Ptr(2), NodeID: stringPtr("PRR_approved"), User: &githubv39.User{Login: stringPtr("alice")}, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("APPROVED"), Body: stringPtr("LGTM"), SubmittedAt: after},
			{ID: int64Ptr(3), NodeID: stringPtr("PRR_empty"), User: &githubv39.User{Login: stringPtr("alice")}, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("COMMENTED"), Body: stringPtr(""), SubmittedAt: after},
			{ID: int64Ptr(4), NodeID: stringPtr("PRR_old"), User: &githubv39.User{Login: stringPtr("alice")}, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("COMMENTED"), Body: stringPtr("Stale"), SubmittedAt: timePtr(baseTime.Add(-time.Minute))},
		},
	}, baseTime, baseTime)

	if len(res.unackReviewNodeIDs) != 1 || res.unackReviewNodeIDs[0] != "PRR_changes" {
		t.Errorf("unackReviewNodeIDs = %v, want [PRR_changes]", res.unackReviewNodeIDs)
	}
}

// TestEvaluateComments_InlineCommentTimedByReview covers a reviewer who starts
// drafting before a push and submits after it. The inline comments were not
// visible when the push happened, so it cannot have answered them: they are
// timed from the review's submission, not from their drafts.
func TestEvaluateComments_InlineCommentTimedByReview(t *testing.T) {
	lastCommit := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	drafted := timePtr(lastCommit.Add(-time.Minute))
	submitted := lastCommit.Add(time.Minute)
	alice := &githubv39.User{Login: stringPtr("alice")}
	pr := &githubv39.PullRequest{User: &githubv39.User{Login: stringPtr("pool-bot")}}

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"pool-bot"},
		TriggerLabel: "factory",
	})
	res := s.evaluateComments(context.Background(), pr, &prHistory{
		reviews: []*githubv39.PullRequestReview{
			// An empty body: the inline comments are the whole review.
			{ID: int64Ptr(1), NodeID: stringPtr("PRR_1"), User: alice, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("COMMENTED"), Body: stringPtr(""), SubmittedAt: timePtr(submitted)},
		},
		revCommentsMap: map[int64][]*githubv39.PullRequestComment{
			1: {{ID: int64Ptr(100), PullRequestReviewID: int64Ptr(1), User: alice, AuthorAssociation: stringPtr("MEMBER"), Body: stringPtr("Rename this"), CreatedAt: drafted}},
		},
	}, lastCommit, time.Time{})

	if len(res.unackPRCommentIDs) != 1 || res.unackPRCommentIDs[0] != 100 {
		t.Fatalf("unackPRCommentIDs = %v, want [100]", res.unackPRCommentIDs)
	}
	// The trigger time becomes the resolver's Since cut-off, so it must be
	// the same time the resolver gives the comment.
	if !res.oldestCommentTime.Equal(submitted) {
		t.Errorf("oldestCommentTime = %v, want review submission %v", res.oldestCommentTime, submitted)
	}
}

// TestEvaluateComments_ReviewReactionGate covers the reaction gate on review
// bodies and inline review comments: feedback the watcher already marked is
// not picked up again, unless a human asked for another pass with 'rocket'.
func TestEvaluateComments_ReviewReactionGate(t *testing.T) {
	const self = "factory-bot"
	type gqlReaction struct {
		Content string            `json:"content"`
		User    map[string]string `json:"user"`
	}
	gql := func(content, login string) gqlReaction {
		return gqlReaction{Content: content, User: map[string]string{"login": login}}
	}
	rest := func(content, login string) *githubv39.Reaction {
		return &githubv39.Reaction{Content: stringPtr(content), User: &githubv39.User{Login: stringPtr(login)}}
	}

	reviewReactions := map[string][]gqlReaction{
		"PRR_acked":    {gql("EYES", self)},
		"PRR_resolved": {gql("EYES", self), gql("THUMBS_UP", self)},
		"PRR_redo":     {gql("EYES", self), gql("CONFUSED", self), gql("ROCKET", "alice")},
	}
	commentReactions := map[string][]*githubv39.Reaction{
		"101": {rest("eyes", self)},
		"102": {rest("eyes", self), rest("rocket", "alice")},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const commentPrefix = "/repos/test-owner/test-repo/pulls/comments/"
		switch {
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var req struct {
				Variables map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			nodes := reviewReactions[req.Variables["id"]]
			if nodes == nil {
				nodes = []gqlReaction{}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"node": map[string]interface{}{"reactions": map[string]interface{}{"nodes": nodes}}},
			})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, commentPrefix):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, commentPrefix), "/reactions")
			reactions := commentReactions[id]
			if reactions == nil {
				reactions = []*githubv39.Reaction{}
			}
			_ = json.NewEncoder(w).Encode(reactions)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	baseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	after := timePtr(baseTime.Add(5 * time.Minute))
	alice := &githubv39.User{Login: stringPtr("alice")}
	review := func(id int64, nodeID string) *githubv39.PullRequestReview {
		return &githubv39.PullRequestReview{ID: int64Ptr(id), NodeID: stringPtr(nodeID), User: alice, AuthorAssociation: stringPtr("MEMBER"), State: stringPtr("COMMENTED"), Body: stringPtr("Please fix"), SubmittedAt: after}
	}
	inline := func(id int64) *githubv39.PullRequestComment {
		return &githubv39.PullRequestComment{ID: int64Ptr(id), User: alice, AuthorAssociation: stringPtr("MEMBER"), Body: stringPtr("Nit"), CreatedAt: after}
	}

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       ghClient,
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"pool-bot"},
		GitHubLogin:  self,
		TriggerLabel: "factory",
	})
	res := s.evaluateComments(context.Background(),
		&githubv39.PullRequest{User: &githubv39.User{Login: stringPtr("pool-bot")}},
		&prHistory{
			reviews: []*githubv39.PullRequestReview{
				review(1, "PRR_new"),
				review(2, "PRR_acked"),
				review(3, "PRR_resolved"),
				review(4, "PRR_redo"),
			},
			revCommentsMap: map[int64][]*githubv39.PullRequestComment{
				1: {inline(100), inline(101), inline(102)},
			},
		},
		baseTime, baseTime,
	)

	if !res.hasNewComments {
		t.Error("hasNewComments = false, want true")
	}
	if want := []string{"PRR_new", "PRR_redo"}; !equalStrings(res.unackReviewNodeIDs, want) {
		t.Errorf("unackReviewNodeIDs = %v, want %v", res.unackReviewNodeIDs, want)
	}
	if len(res.unackPRCommentIDs) != 2 || res.unackPRCommentIDs[0] != 100 || res.unackPRCommentIDs[1] != 102 {
		t.Errorf("unackPRCommentIDs = %v, want [100 102]", res.unackPRCommentIDs)
	}
}

// TestEvaluateComments_Trust covers the trust gate: feedback from an account
// without write access is never picked up, unless an operator listed the
// login as an allowlisted user.
func TestEvaluateComments_Trust(t *testing.T) {
	baseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	after := timePtr(baseTime.Add(5 * time.Minute))
	pr := &githubv39.PullRequest{User: &githubv39.User{Login: stringPtr("pool-bot")}}
	comment := func(login, association string) *githubv39.IssueComment {
		return &githubv39.IssueComment{
			ID:                int64Ptr(1),
			User:              &githubv39.User{Login: stringPtr(login), Type: stringPtr("User")},
			AuthorAssociation: stringPtr(association),
			Body:              stringPtr("Please fix this"),
			CreatedAt:         after,
		}
	}
	review := func(login, association string) *githubv39.PullRequestReview {
		return &githubv39.PullRequestReview{
			ID: int64Ptr(2), NodeID: stringPtr("PRR_2"),
			User:              &githubv39.User{Login: stringPtr(login), Type: stringPtr("User")},
			AuthorAssociation: stringPtr(association),
			State:             stringPtr("CHANGES_REQUESTED"), Body: stringPtr("Please rework"), SubmittedAt: after,
		}
	}
	inline := func(login, association string) map[int64][]*githubv39.PullRequestComment {
		return map[int64][]*githubv39.PullRequestComment{2: {{
			ID: int64Ptr(3), PullRequestReviewID: int64Ptr(2),
			User:              &githubv39.User{Login: stringPtr(login), Type: stringPtr("User")},
			AuthorAssociation: stringPtr(association),
			Body:              stringPtr("Nit"), CreatedAt: after,
		}}}
	}

	for _, tc := range []struct {
		name        string
		login       string
		association string
		want        bool
	}{
		{"stranger is ignored", "stranger", "NONE", false},
		{"contributor is ignored", "stranger", "CONTRIBUTOR", false},
		{"collaborator is acted on", "maintainer", "COLLABORATOR", true},
		{"allowlisted user is acted on", "Private-Member", "NONE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No GitHub client: a request for reactions would mean an
			// untrusted comment got past the trust gate.
			s, _ := newTestScanner(t, t.TempDir(), testOpts{
				Kube:             newTestKubeClient(),
				BotUsers:         []string{"pool-bot"},
				TriggerLabel:     "factory",
				AllowlistedUsers: []string{"private-member"},
			})
			if !tc.want {
				res := s.evaluateComments(context.Background(), pr, &prHistory{
					comments:       []*githubv39.IssueComment{comment(tc.login, tc.association)},
					reviews:        []*githubv39.PullRequestReview{review(tc.login, tc.association)},
					revCommentsMap: inline(tc.login, tc.association),
				}, baseTime, baseTime)
				if res.hasNewComments {
					t.Errorf("hasNewComments = true, want untrusted feedback ignored: %+v", res)
				}
				return
			}
			// Trusted feedback goes on to the reaction read; serve none.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/graphql" {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"data": map[string]interface{}{"node": map[string]interface{}{"reactions": map[string]interface{}{"nodes": []interface{}{}}}},
					})
					return
				}
				_ = json.NewEncoder(w).Encode([]interface{}{})
			}))
			defer server.Close()
			gh := githubv39.NewClient(nil)
			gh.BaseURL, _ = url.Parse(server.URL + "/")
			s, _ = newTestScanner(t, t.TempDir(), testOpts{
				GitHub:           gh,
				Kube:             newTestKubeClient(),
				BotUsers:         []string{"pool-bot"},
				TriggerLabel:     "factory",
				AllowlistedUsers: []string{"private-member"},
			})
			res := s.evaluateComments(context.Background(), pr, &prHistory{
				comments:       []*githubv39.IssueComment{comment(tc.login, tc.association)},
				reviews:        []*githubv39.PullRequestReview{review(tc.login, tc.association)},
				revCommentsMap: inline(tc.login, tc.association),
			}, baseTime, baseTime)
			if len(res.unackCommentIDs) != 1 || len(res.unackReviewNodeIDs) != 1 || len(res.unackPRCommentIDs) != 1 {
				t.Errorf("picked up comments=%v reviews=%v inline=%v, want one of each", res.unackCommentIDs, res.unackReviewNodeIDs, res.unackPRCommentIDs)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
