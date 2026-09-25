package prs

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

	githubv39 "github.com/google/go-github/v39/github"
)

func TestGetMissingLabelsForPR(t *testing.T) {
	tests := []struct {
		name         string
		triggerLabel string
		prLabels     []string
		refIssues    [][]string
		expected     []string
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
		{
			name:         "Review label is synced before ready-for-human is present",
			triggerLabel: "factory",
			prLabels:     []string{"factory"},
			refIssues:    [][]string{{"overseer/review", "factory/review", "bug"}},
			expected:     []string{"factory", "overseer/review", "factory/review", "bug"},
		},
		{
			name:         "Review label is skipped once ready-for-human is present",
			triggerLabel: "factory",
			prLabels:     []string{"factory", "factory/ready-for-human"},
			refIssues:    [][]string{{"overseer/review", "factory/review", "bug"}},
			expected:     []string{"factory", "factory/ready-for-human", "bug"},
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

			got := getMissingLabelsForPR(prLabels, refIssues, tc.triggerLabel)

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

func TestGetReadyForHumanLabel(t *testing.T) {
	if readyForHumanLabel("") != "overseer/ready-for-human" {
		t.Errorf("readyForHumanLabel(\"\") = %q, want 'overseer/ready-for-human'", readyForHumanLabel(""))
	}
	if readyForHumanLabel("overseer") != "overseer/ready-for-human" {
		t.Errorf("readyForHumanLabel(\"overseer\") = %q, want 'overseer/ready-for-human'", readyForHumanLabel("overseer"))
	}
	if readyForHumanLabel("mybot") != "mybot/ready-for-human" {
		t.Errorf("readyForHumanLabel(\"mybot\") = %q, want 'mybot/ready-for-human'", readyForHumanLabel("mybot"))
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
	s := &Scanner{cfg: Config{ReviewerLogins: []string{"custom-reviewbot"}}}
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
	if !s.hasCompletedBotReviewOnHead(reviews1, headSHA, commitTime) {
		t.Errorf("expected hasCompletedBotReviewOnHead with recent review to be true")
	}

	// 2. Review matching headSHA with APPROVED
	reviews2 := []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("reviewbot-robot")},
			CommitID:    stringPtr("abc1234"),
			SubmittedAt: &time.Time{},
			State:       stringPtr("APPROVED"),
		},
	}
	*reviews2[0].SubmittedAt = now.Add(-15 * time.Minute)
	if !s.hasCompletedBotReviewOnHead(reviews2, headSHA, commitTime) {
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
	if s.hasCompletedBotReviewOnHead(reviews3, headSHA, commitTime) {
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
	if s.hasCompletedBotReviewOnHead(reviews4, headSHA, commitTime) {
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
	if s.hasCompletedBotReviewOnHead(reviews5, headSHA, commitTime) {
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
	if !s.hasCompletedBotReviewOnHead(reviews6, headSHA, commitTime) {
		t.Errorf("expected hasCompletedBotReviewOnHead with latest APPROVED to be true")
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
			name:           "Ready with overseer/review adds ready-for-human and removes overseer/review",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"bug", "overseer/review"},
			expectedCalls: []apiCall{
				{
					method: "POST",
					path:   "/repos/test-owner/test-repo/issues/100/labels",
					body:   `["overseer/ready-for-human"]`,
				},
				{
					method: "DELETE",
					path:   "/repos/test-owner/test-repo/issues/100/labels/overseer/review",
				},
			},
		},
		{
			name:           "Ready when already ready-for-human and overseer/review re-added removes overseer/review",
			triggerLabel:   "",
			isReady:        true,
			existingLabels: []string{"overseer/ready-for-human", "overseer/review"},
			expectedCalls: []apiCall{
				{
					method: "DELETE",
					path:   "/repos/test-owner/test-repo/issues/100/labels/overseer/review",
				},
			},
		},
		{
			name:           "Not ready with label never removes overseer/ready-for-human",
			triggerLabel:   "",
			isReady:        false,
			existingLabels: []string{"bug", "overseer/ready-for-human"},
			expectedCalls:  nil,
		},
		{
			name:           "Custom trigger label adds custom/ready-for-human and removes custom/review",
			triggerLabel:   "mybot",
			isReady:        true,
			existingLabels: []string{"bug", "mybot/review"},
			expectedCalls: []apiCall{
				{
					method: "POST",
					path:   "/repos/test-owner/test-repo/issues/100/labels",
					body:   `["mybot/ready-for-human"]`,
				},
				{
					method: "DELETE",
					path:   "/repos/test-owner/test-repo/issues/100/labels/mybot/review",
				},
			},
		},
		{
			name:           "Custom trigger label never removes custom/ready-for-human when not ready",
			triggerLabel:   "mybot",
			isReady:        false,
			existingLabels: []string{"mybot/ready-for-human"},
			expectedCalls:  nil,
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
			existingLabels: []string{"bug", "overseer/review"},
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

			s, _ := newTestScanner(t, t.TempDir(), testOpts{
				GitHub:       ghClient,
				TriggerLabel: tc.triggerLabel,
			})
			s.cfg.DryRun = tc.dryRun

			prNum := 100
			var labels []*githubv39.Label
			for _, l := range tc.existingLabels {
				labels = append(labels, &githubv39.Label{Name: stringPtr(l)})
			}
			prIssue := &githubv39.Issue{
				Number: &prNum,
				Labels: labels,
			}
			mergeable := true
			now := time.Now()
			pc := &prContext{
				pr: &githubv39.PullRequest{
					Number:    &prNum,
					Mergeable: &mergeable,
					State:     stringPtr("open"),
				},
				prIssue:   prIssue,
				headSHA:   "sha123",
				refIssues: &refIssues{loaded: true},
			}
			history := &prHistory{
				reviews: []*githubv39.PullRequestReview{
					{
						User:        &githubv39.User{Login: stringPtr("reviewbot")},
						CommitID:    stringPtr("sha123"),
						State:       stringPtr("APPROVED"),
						SubmittedAt: &now,
					},
				},
			}

			s.reconcileReadiness(context.Background(), pc, prCheckAnalysis{}, prCommentAnalysis{}, history, !tc.isReady, "")

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

func TestGetMissingHumanAssigneesForPR(t *testing.T) {
	s := &Scanner{
		cfg: Config{
			BotUsers:       []string{"ada-coder", "overseer-watcher"},
			ReviewerLogins: []string{"custom-reviewer"},
			GitHubLogin:    "overseer-watcher",
		},
	}

	prAssignees := []*githubv39.User{
		{Login: stringPtr("ada-coder")},
		{Login: stringPtr("alice")},
	}

	refIssues := []*githubv39.Issue{
		{
			Assignees: []*githubv39.User{
				{Login: stringPtr("ada-coder")},
				{Login: stringPtr("overseer-watcher")},
				{Login: stringPtr("custom-reviewer")},
				{Login: stringPtr("reviewbot-robot")},
				{Login: stringPtr("dependabot[bot]"), Type: stringPtr("Bot")},
				{Login: stringPtr("Alice")}, // already assigned to PR (case-insensitive)
				{Login: stringPtr("bob")},
			},
		},
		{
			Assignees: []*githubv39.User{
				{Login: stringPtr("bob")}, // duplicate across referenced issues
				{Login: stringPtr("carol")},
			},
		},
	}

	got := s.getMissingHumanAssigneesForPR(prAssignees, refIssues)
	want := []string{"bob", "carol"}

	if len(got) != len(want) {
		t.Fatalf("getMissingHumanAssigneesForPR() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("getMissingHumanAssigneesForPR()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
