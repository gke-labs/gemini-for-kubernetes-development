package prs

import (
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

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

// One definition of "a bot reviewed it" decides both whether a review is
// queued and whether review lets the pull request be ready: the reviewer
// role's review of the head (the watcher's own account when no reviewer role
// is configured). Another bot's review is neither ours nor a reason to skip
// ours.
func TestAutomatedReviewOfHead(t *testing.T) {
	now := time.Now()
	commitTime := now.Add(-10 * time.Minute)
	head := "abc1234"
	review := func(login, state string) *githubv39.PullRequestReview {
		at := now.Add(-5 * time.Minute)
		return &githubv39.PullRequestReview{User: &githubv39.User{Login: stringPtr(login)}, CommitID: stringPtr(head), SubmittedAt: &at, State: stringPtr(state)}
	}
	withRole := &Scanner{cfg: Config{ReviewerLogins: []string{"reviewbot-robot"}, GitHubLogin: "argus-watcher-bot", AllowlistedBots: []string{"reviewbot-robot"}}}
	noRole := &Scanner{cfg: Config{GitHubLogin: "argus-watcher-bot"}}

	for _, tc := range []struct {
		name       string
		s          *Scanner
		reviews    []*githubv39.PullRequestReview
		wantReview bool // a review is queued when false
		wantReady  bool
	}{
		{"reviewer role reviewed head", withRole, []*githubv39.PullRequestReview{review("reviewbot-robot", "COMMENTED")}, true, true},
		{"reviewer role asked for changes", withRole, []*githubv39.PullRequestReview{review("reviewbot-robot", "CHANGES_REQUESTED")}, true, false},
		{"another bot reviewed head", withRole, []*githubv39.PullRequestReview{review("gemini-code-assist[bot]", "COMMENTED")}, false, false},
		{"the watcher reviewed head, with a reviewer role", withRole, []*githubv39.PullRequestReview{review("argus-watcher-bot", "COMMENTED")}, false, false},
		{"no reviewer role: the watcher's review is ours", noRole, []*githubv39.PullRequestReview{review("argus-watcher-bot", "COMMENTED")}, true, true},
		{"no review", withRole, nil, false, false},
	} {
		got := tc.s.automatedReviewOfHead(tc.reviews, head, commitTime) != nil
		if got != tc.wantReview {
			t.Errorf("%s: automated review found = %v, want %v", tc.name, got, tc.wantReview)
		}
	}
}
