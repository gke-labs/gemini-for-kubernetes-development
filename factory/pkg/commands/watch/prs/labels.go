package prs

import (
	"context"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// syncReferencedIssueLabels copies the labels of the issues a pull request
// closes onto the pull request itself.
//
// Labels are how the repository directs the watcher - stop, review, priority -
// and they are naturally put on the issue, not on the fix. Inheriting them is
// what makes 'overseer/stop' on an issue actually stop work on its pull
// request, which is why the caller re-checks the stop label immediately after
// calling this.
func (s *Scanner) syncReferencedIssueLabels(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue, refs *refIssues) {
	allMissingLabels := getMissingLabelsForPR(prIssue.Labels, refs.all(ctx), s.cfg.TriggerLabel)

	if len(allMissingLabels) > 0 {
		klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, pr.GetNumber())
		if err := s.gh.AddLabels(ctx, pr.GetNumber(), allMissingLabels); err != nil {
			klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, pr.GetNumber(), err)
		} else {
			for _, labelName := range allMissingLabels {
				prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: githubv39.String(labelName)})
			}
		}
	}
}

// getMissingLabelsForPR returns the labels present on the referenced issues but
// not yet on the pull request. Once the pull request carries ready-for-human,
// review labels on the parent issues are not re-copied onto the pull request.
func getMissingLabelsForPR(prLabels []*githubv39.Label, refIssues []*githubv39.Issue, triggerLabel string) []string {
	prLabelsSet := make(map[string]bool)
	for _, label := range prLabels {
		if label.GetName() != "" {
			prLabelsSet[label.GetName()] = true
		}
	}

	skipReview := hasReadyForHumanLabel(prLabels, triggerLabel)

	var allMissingLabels []string
	missingLabelsSet := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}

		for _, label := range refIssue.Labels {
			labelName := label.GetName()
			if labelName == "" {
				continue
			}
			if skipReview && isReviewLabel(labelName, triggerLabel) {
				continue
			}
			if !prLabelsSet[labelName] && !missingLabelsSet[labelName] {
				missingLabelsSet[labelName] = true
				allMissingLabels = append(allMissingLabels, labelName)
			}
		}
	}

	return allMissingLabels
}

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

// shouldAutoReviewPR reports whether the pull request has been opted into
// automated review, either directly or (before ready-for-human is set) through
// an issue it closes.
//
// Review is opt-in rather than universal because it costs an agent run per
// commit; the label is how a repository says a change is worth that. Once a
// pull request has settled and been marked ready-for-human, its review label is
// removed and only a review label explicitly re-added to the pull request
// itself triggers another automated review.
func (s *Scanner) shouldAutoReviewPR(ctx context.Context, prIssue *githubv39.Issue, refs *refIssues) bool {
	if hasReviewLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		return true
	}
	if hasReadyForHumanLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		return false
	}
	for _, refIssue := range refs.all(ctx) {
		if hasReviewLabel(refIssue.Labels, s.cfg.TriggerLabel) {
			return true
		}
	}
	return false
}

// readyForHumanLabel returns the label marking a pull request as done with
// automation and waiting on a person.
func readyForHumanLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		return triggerLabel + "/ready-for-human"
	}
	return "overseer/ready-for-human"
}

// hasReadyForHumanLabel reports whether the pull request already carries the
// ready-for-human label under either spelling.
func hasReadyForHumanLabel(labels []*githubv39.Label, triggerLabel string) bool {
	readyLabels := []string{"overseer/ready-for-human"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		readyLabels = append(readyLabels, triggerLabel+"/ready-for-human")
	}
	for _, label := range labels {
		for _, ready := range readyLabels {
			if strings.EqualFold(label.GetName(), ready) {
				return true
			}
		}
	}
	return false
}

// hasCompletedBotReviewOnHead reports whether an automated review of the
// current head has finished without asking for changes.
//
// Only the most recent qualifying review counts: a reviewer that asked for
// changes and then approved after a fixup has been satisfied, and treating the
// earlier verdict as still standing would hold the pull request back forever.
func (s *Scanner) hasCompletedBotReviewOnHead(reviews []*githubv39.PullRequestReview, headSHA string, lastCommitTime time.Time) bool {
	var latestReview *githubv39.PullRequestReview
	for _, r := range reviews {
		if conventions.IsReviewerBot(r.GetUser(), s.cfg.ReviewerLogins) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			if latestReview == nil || r.GetSubmittedAt().After(latestReview.GetSubmittedAt()) {
				latestReview = r
			}
		}
	}
	if latestReview == nil {
		return false
	}
	return latestReview.GetState() != "CHANGES_REQUESTED"
}

// isHumanUser reports whether u is a human account rather than one of the
// configured or recognised bot accounts.
func (s *Scanner) isHumanUser(u *githubv39.User) bool {
	if u == nil || u.GetLogin() == "" {
		return false
	}
	login := u.GetLogin()
	for _, bot := range s.cfg.BotUsers {
		if strings.EqualFold(login, bot) {
			return false
		}
	}
	if conventions.IsReviewerBot(u, s.cfg.ReviewerLogins) {
		return false
	}
	if conventions.ShouldIgnoreUser(u, s.cfg.GitHubLogin, nil) {
		return false
	}
	return true
}

// getMissingHumanAssigneesForPR returns the human assignees on the referenced
// parent issues that are not yet assigned to the pull request.
func (s *Scanner) getMissingHumanAssigneesForPR(prAssignees []*githubv39.User, refIssues []*githubv39.Issue) []string {
	existing := make(map[string]bool, len(prAssignees))
	for _, u := range prAssignees {
		if login := u.GetLogin(); login != "" {
			existing[strings.ToLower(login)] = true
		}
	}

	var missing []string
	seen := make(map[string]bool)
	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}
		for _, u := range refIssue.Assignees {
			if !s.isHumanUser(u) {
				continue
			}
			login := u.GetLogin()
			key := strings.ToLower(login)
			if !existing[key] && !seen[key] {
				seen[key] = true
				missing = append(missing, login)
			}
		}
	}
	return missing
}
