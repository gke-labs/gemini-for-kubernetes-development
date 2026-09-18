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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
)

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
			count := getInvestigationCount(tc.comments, lastCommitTime, tc.allBotUsers, tc.githubLogin, tc.allowlist, "factory")
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
	got := getLastPRActivityTime(pr, nil, nil, nil, githubLogin, bots, "factory")
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
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
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
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
	got = getLastPRActivityTime(pr, nil, reviews, nil, githubLogin, bots, "factory")
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
	got = getLastPRActivityTime(pr, nil, reviews, revComments, githubLogin, bots, "factory")
	if !got.Equal(humanReviewCommentTime) {
		t.Errorf("Case 6 failed: expected %v, got %v", humanReviewCommentTime, got)
	}

	// Case 7: Human comment with /overseer-ignore (ignored)
	ignoreTime := baseTime.Add(7 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &ignoreTime,
			Body:      stringPtr("/overseer-ignore: This is side-channel conversation"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 7 failed: expected /overseer-ignore comment to be ignored and return %v, got %v", baseTime, got)
	}

	// Case 8: Human comment with /factory-ignore (ignored because triggerLabel is factory)
	factoryIgnoreTime := baseTime.Add(8 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &factoryIgnoreTime,
			Body:      stringPtr("/factory-ignore: This is side-channel conversation with custom prefix"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots, "factory")
	if !got.Equal(baseTime) {
		t.Errorf("Case 8 failed: expected /factory-ignore comment to be ignored when triggerLabel is 'factory' and return %v, got %v", baseTime, got)
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

func TestIsReviewLabel(t *testing.T) {
	tests := []struct {
		name         string
		label        string
		triggerLabel string
		want         bool
	}{
		{name: "Default spelling", label: "overseer/review", triggerLabel: "", want: true},
		{name: "Default spelling under a custom trigger", label: "overseer/review", triggerLabel: "mybot", want: true},
		{name: "Custom spelling", label: "mybot/review", triggerLabel: "mybot", want: true},
		{name: "Custom spelling without the matching trigger", label: "mybot/review", triggerLabel: "", want: false},
		{name: "Case insensitive", label: "Overseer/Review", triggerLabel: "", want: true},
		{name: "Unrelated label", label: "needs-review", triggerLabel: "", want: false},
		{name: "Empty label", label: "", triggerLabel: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReviewLabel(tc.label, tc.triggerLabel); got != tc.want {
				t.Errorf("isReviewLabel(%q, %q) = %v; want %v", tc.label, tc.triggerLabel, got, tc.want)
			}
		})
	}
}

// TestNonStickyLabels pins the membership of the non-sticky set. Enrolling a
// label changes how the watcher and a human share control of it, so it should
// be a deliberate decision rather than something that drifts in.
func TestNonStickyLabels(t *testing.T) {
	if got, want := nonStickyLabels(""), []string{"overseer/review"}; !sameStrings(got, want) {
		t.Errorf("nonStickyLabels(%q) = %v; want %v", "", got, want)
	}
	if got, want := nonStickyLabels("mybot"), []string{"overseer/review", "mybot/review"}; !sameStrings(got, want) {
		t.Errorf("nonStickyLabels(%q) = %v; want %v", "mybot", got, want)
	}

	// The labels that direct the watcher from the parent issue stay sticky:
	// unlike review, they have no per-PR meaning a human could be asserting.
	for _, label := range []string{"overseer/stop", "mybot/stop", "overseer/ready-for-human", "priority/high", "mybot"} {
		if isNonStickyLabel(label, "mybot") {
			t.Errorf("isNonStickyLabel(%q, \"mybot\") = true; want false", label)
		}
	}
}

// TestSyncReferencedIssueLabels_NonStickyLabels pins the handoff a non-sticky
// label is meant to be: inherited onto a pull request that has never carried
// it, and never re-applied once someone has taken it off. Labels outside the
// non-sticky set must keep following the parent issue as before.
func TestSyncReferencedIssueLabels_NonStickyLabels(t *testing.T) {
	const (
		prNum     = 100
		parentNum = 42
	)

	tests := []struct {
		name string
		// parentLabels are the labels on the issue the pull request closes.
		parentLabels []string
		// prLabels are the labels already on the pull request.
		prLabels []string
		// removedLabels are the labels an 'unlabeled' event exists for on the
		// pull request, i.e. the ones somebody has taken off it.
		removedLabels []string
		// failEvents makes the events endpoint return an error.
		failEvents bool
		// wantAdded is the label set expected in the POST, or nil for no POST.
		wantAdded []string
		// wantEventsFetched is whether the label history had to be consulted.
		wantEventsFetched bool
	}{
		{
			name:              "Fresh PR inherits the review label from its parent issue",
			parentLabels:      []string{"factory", "factory/review", "priority/high"},
			wantAdded:         []string{"factory", "factory/review", "priority/high"},
			wantEventsFetched: true,
		},
		{
			name:              "Review label removed from the PR is not re-added",
			parentLabels:      []string{"factory", "factory/review", "priority/high"},
			removedLabels:     []string{"factory/review"},
			wantAdded:         []string{"factory", "priority/high"},
			wantEventsFetched: true,
		},
		{
			name:              "Removal is honoured for the default spelling too",
			parentLabels:      []string{"overseer/review"},
			removedLabels:     []string{"overseer/review"},
			wantAdded:         nil,
			wantEventsFetched: true,
		},
		{
			name:              "Removing an unrelated label does not block the review label",
			parentLabels:      []string{"factory/review"},
			removedLabels:     []string{"priority/high"},
			wantAdded:         []string{"factory/review"},
			wantEventsFetched: true,
		},
		{
			// Only the labels in the non-sticky set get the handoff treatment.
			// Everything else is still reconciled from the parent issue, so a
			// removed 'overseer/stop' comes back as it always did.
			name:          "Sticky labels are still re-added after removal",
			parentLabels:  []string{"overseer/stop", "priority/high"},
			removedLabels: []string{"overseer/stop", "priority/high"},
			wantAdded:     []string{"overseer/stop", "priority/high"},
		},
		{
			name:              "Unreadable label history holds the review label back but not the rest",
			parentLabels:      []string{"factory", "factory/review"},
			failEvents:        true,
			wantAdded:         []string{"factory"},
			wantEventsFetched: true,
		},
		{
			name:         "No non-sticky label to inherit means no label history lookup",
			parentLabels: []string{"factory", "priority/high"},
			wantAdded:    []string{"factory", "priority/high"},
		},
		{
			name:         "Review label already on the PR is left alone",
			parentLabels: []string{"factory/review"},
			prLabels:     []string{"factory/review"},
			wantAdded:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var addedLabels []string
			eventsFetched := false

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/42":
					var labels []*githubv39.Label
					for _, name := range tc.parentLabels {
						labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
					}
					_ = json.NewEncoder(w).Encode(&githubv39.Issue{
						Number: githubv39.Int(parentNum),
						Labels: labels,
					})

				case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/100/events":
					eventsFetched = true
					if tc.failEvents {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					events := []*githubv39.IssueEvent{
						{Event: githubv39.String("labeled"), Label: &githubv39.Label{Name: stringPtr("factory")}},
					}
					for _, name := range tc.removedLabels {
						events = append(events, &githubv39.IssueEvent{
							Event: githubv39.String("unlabeled"),
							Label: &githubv39.Label{Name: stringPtr(name)},
						})
					}
					_ = json.NewEncoder(w).Encode(events)

				case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/100/labels":
					body, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(body, &addedLabels)
					_, _ = w.Write([]byte(`[]`))

				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			ghClient := githubv39.NewClient(nil)
			ghClient.BaseURL, _ = url.Parse(server.URL + "/")

			s := &Scanner{
				cfg:   Config{TriggerLabel: "factory"},
				gh:    github.ForRepo(ghClient, "test-owner", "test-repo"),
				state: newStateStore(nil),
			}

			pr := &githubv39.PullRequest{
				Number: githubv39.Int(prNum),
				Body:   stringPtr("Fixes #42"),
			}
			var prLabels []*githubv39.Label
			for _, name := range tc.prLabels {
				prLabels = append(prLabels, &githubv39.Label{Name: stringPtr(name)})
			}
			prIssue := &githubv39.Issue{Number: githubv39.Int(prNum), Labels: prLabels}

			s.syncReferencedIssueLabels(context.Background(), pr, prIssue)

			if !sameStrings(addedLabels, tc.wantAdded) {
				t.Errorf("added labels = %v; want %v", addedLabels, tc.wantAdded)
			}
			if eventsFetched != tc.wantEventsFetched {
				t.Errorf("label history fetched = %v; want %v", eventsFetched, tc.wantEventsFetched)
			}

			// Whatever was added must also be visible on the in-memory issue:
			// the rest of the evaluation reads it rather than re-fetching.
			for _, name := range tc.wantAdded {
				if !hasLabelNamed(prIssue.Labels, name) {
					t.Errorf("label %q was added on GitHub but is missing from the in-memory PR issue", name)
				}
			}
		})
	}
}

// TestSyncReferencedIssueLabels_ReclaimedLabelIsRememberedOnce checks that a
// reclaimed label does not cost a label-history lookup on every later cycle.
// Without the memo this is the one case that would pay for the lookup forever.
func TestSyncReferencedIssueLabels_ReclaimedLabelIsRememberedOnce(t *testing.T) {
	eventsFetches := 0
	labelPosts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/42":
			_ = json.NewEncoder(w).Encode(&githubv39.Issue{
				Number: githubv39.Int(42),
				Labels: []*githubv39.Label{{Name: stringPtr("factory/review")}},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/100/events":
			eventsFetches++
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueEvent{
				{Event: githubv39.String("unlabeled"), Label: &githubv39.Label{Name: stringPtr("factory/review")}},
			})

		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/100/labels":
			labelPosts++
			_, _ = w.Write([]byte(`[]`))

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s := &Scanner{
		cfg:   Config{TriggerLabel: "factory"},
		gh:    github.ForRepo(ghClient, "test-owner", "test-repo"),
		state: newStateStore(nil),
	}

	pr := &githubv39.PullRequest{Number: githubv39.Int(100), Body: stringPtr("Fixes #42")}
	for i := 0; i < 3; i++ {
		prIssue := &githubv39.Issue{Number: githubv39.Int(100)}
		s.syncReferencedIssueLabels(context.Background(), pr, prIssue)
	}

	if eventsFetches != 1 {
		t.Errorf("fetched the label history %d times across 3 cycles; want 1", eventsFetches)
	}
	if labelPosts != 0 {
		t.Errorf("posted labels %d times; want 0, the only label to inherit was reclaimed", labelPosts)
	}
}

// TestShouldAutoReviewPR_IgnoresParentIssue pins the other half of the handoff:
// with the label off the pull request, the parent issue must not put it back.
func TestShouldAutoReviewPR_IgnoresParentIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub request %s %s: the review opt-in must be answered from the PR's own labels", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s := &Scanner{
		cfg: Config{TriggerLabel: "factory"},
		gh:  github.ForRepo(ghClient, "test-owner", "test-repo"),
	}

	withLabel := &githubv39.Issue{
		Number: githubv39.Int(100),
		Labels: []*githubv39.Label{{Name: stringPtr("factory/review")}},
	}
	if !s.shouldAutoReviewPR(withLabel) {
		t.Error("shouldAutoReviewPR() = false for a PR carrying the review label; want true")
	}

	withoutLabel := &githubv39.Issue{
		Number: githubv39.Int(100),
		Labels: []*githubv39.Label{{Name: stringPtr("factory")}},
	}
	if s.shouldAutoReviewPR(withoutLabel) {
		t.Error("shouldAutoReviewPR() = true for a PR whose review label was removed; want false")
	}
}

// sameStrings compares two label lists as sets, since neither the order the
// labels are inherited in nor the order GitHub returns them is meaningful.
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, s := range want {
		counts[s]++
	}
	for _, s := range got {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// hasLabelNamed reports whether labels contains one named name.
func hasLabelNamed(labels []*githubv39.Label, name string) bool {
	for _, label := range labels {
		if label.GetName() == name {
			return true
		}
	}
	return false
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

			s := &Scanner{
				cfg: Config{
					TriggerLabel: tc.triggerLabel,
					DryRun:       tc.dryRun,
				},
				gh: github.ForRepo(ghClient, "test-owner", "test-repo"),
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

			s.reconcileReadyForHumanLabel(context.Background(), prNum, prIssue, tc.isReady, "sha123")

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
