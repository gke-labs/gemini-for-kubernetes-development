package prs

import (
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// isPRApprovedOrLGTM reports whether the pull request has already been signed
// off, in which case there is nothing for an automated review to add.
//
// A requested change anywhere outrules an approval: the review that asked for
// something is the one still outstanding, regardless of who approved alongside it.
func isPRApprovedOrLGTM(pr *githubv39.PullRequest, prIssue *githubv39.Issue, reviews []*githubv39.PullRequestReview) bool {
	// 1. Check labels
	for _, label := range prIssue.Labels {
		if strings.EqualFold(label.GetName(), "lgtm") || strings.EqualFold(label.GetName(), "approved") {
			return true
		}
	}

	// 2. Check reviews
	hasApproved := false
	hasChangesRequested := false
	latestReviews := make(map[string]string)
	for _, r := range reviews {
		if r.GetUser() != nil && r.GetState() != "" {
			latestReviews[r.GetUser().GetLogin()] = r.GetState()
		}
	}
	for _, state := range latestReviews {
		if state == "APPROVED" {
			hasApproved = true
		} else if state == "CHANGES_REQUESTED" {
			hasChangesRequested = true
		}
	}

	return hasApproved && !hasChangesRequested
}

// isReviewLabel reports whether labelName is a review opt-in label.
func isReviewLabel(labelName, triggerLabel string) bool {
	if strings.EqualFold(labelName, "overseer/review") {
		return true
	}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		if strings.EqualFold(labelName, triggerLabel+"/review") {
			return true
		}
	}
	return false
}

// getReviewLabels returns the review opt-in labels currently present in labels.
func getReviewLabels(labels []*githubv39.Label, triggerLabel string) []string {
	var out []string
	for _, label := range labels {
		name := label.GetName()
		if isReviewLabel(name, triggerLabel) {
			out = append(out, name)
		}
	}
	return out
}

// hasReviewLabel reports whether a set of labels opts the change into automated
// review.
func hasReviewLabel(labels []*githubv39.Label, triggerLabel string) bool {
	return len(getReviewLabels(labels, triggerLabel)) > 0
}

// readyForHumanLabel returns the label marking a pull request as done with
// automation and waiting on a person.
func readyForHumanLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		return triggerLabel + "/ready-for-human"
	}
	return "overseer/ready-for-human"
}

// isReadyForHumanLabel reports whether labelName is a ready-for-human label.
func isReadyForHumanLabel(labelName, triggerLabel string) bool {
	if strings.EqualFold(labelName, "overseer/ready-for-human") {
		return true
	}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		if strings.EqualFold(labelName, triggerLabel+"/ready-for-human") {
			return true
		}
	}
	return false
}

// hasReadyForHumanLabel reports whether the pull request already carries the
// ready-for-human label under either spelling.
func hasReadyForHumanLabel(labels []*githubv39.Label, triggerLabel string) bool {
	for _, label := range labels {
		if isReadyForHumanLabel(label.GetName(), triggerLabel) {
			return true
		}
	}
	return false
}

// automatedReviewOfHead is the latest automated review of the current head
// (submitted after its last commit, or of its commit), or nil: the one
// definition of "a bot reviewed it", which decides both whether to queue a
// review and whether review lets the pull request be ready.
//
// An automated review is one by the accounts pr-review tasks run as: the
// reviewer role, or the watcher's own account when no reviewer role is
// configured (the task then runs as the default user), and the 'reviewbot'
// name fallback of IsReviewerBot. Another bot's review (a code-assist app's,
// say) is not ours, and neither stops our review nor stands in for it.
//
// Only the most recent counts: a reviewer that asked for changes and then
// approved after a fixup has been satisfied, and treating the earlier verdict
// as still standing would hold the pull request back forever.
func (s *Scanner) automatedReviewOfHead(reviews []*githubv39.PullRequestReview, headSHA string, lastCommitTime time.Time) *githubv39.PullRequestReview {
	reviewers := s.cfg.ReviewerLogins
	if len(reviewers) == 0 {
		reviewers = []string{s.cfg.GitHubLogin}
	}
	var latest *githubv39.PullRequestReview
	for _, r := range reviews {
		if conventions.IsReviewerBot(r.GetUser(), reviewers) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			if latest == nil || r.GetSubmittedAt().After(latest.GetSubmittedAt()) {
				latest = r
			}
		}
	}
	return latest
}
