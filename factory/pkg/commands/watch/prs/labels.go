package prs

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// fetchReferencedIssuesHierarchy fetches referenced issues and their parent/workflow issues recursively.
func (s *Scanner) fetchReferencedIssuesHierarchy(ctx context.Context, pr *githubv39.PullRequest) []*githubv39.Issue {
	if s.gh == nil || pr == nil {
		return nil
	}

	var result []*githubv39.Issue
	visited := make(map[int]bool)
	var queue []int

	for refNum := range common.GetReferencedIssues(pr) {
		if !visited[refNum] {
			visited[refNum] = true
			queue = append(queue, refNum)
		}
	}

	maxIssues := 10
	for len(queue) > 0 && len(result) < maxIssues {
		issueNum := queue[0]
		queue = queue[1:]

		issue, err := s.gh.GetIssue(ctx, issueNum)
		if err != nil {
			klog.Warningf("Failed to fetch referenced issue #%d for PR #%d: %v", issueNum, pr.GetNumber(), err)
			continue
		}
		result = append(result, issue)

		for parentNum := range common.GetParentIssuesFromIssue(issue) {
			if !visited[parentNum] {
				visited[parentNum] = true
				queue = append(queue, parentNum)
			}
		}
	}

	return result
}

// syncReferencedIssueLabels copies the labels of the issues a pull request
// closes onto the pull request itself.
//
// Labels are how the repository directs the watcher - stop, review, priority -
// and they are naturally put on the issue, not on the fix. Inheriting them is
// what makes 'overseer/stop' on an issue actually stop work on its pull
// request, which is why the caller re-checks the stop label immediately after
// calling this.
func (s *Scanner) syncReferencedIssueLabels(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue, refIssues []*githubv39.Issue) {
	if s.gh == nil || pr == nil || prIssue == nil || len(refIssues) == 0 {
		return
	}

	allMissingLabels := getMissingLabelsForPR(prIssue.Labels, refIssues)

	if len(allMissingLabels) > 0 {
		klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, pr.GetNumber())
		if err := s.gh.AddLabels(ctx, pr.GetNumber(), allMissingLabels); err != nil {
			klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, pr.GetNumber(), err)
		} else {
			for _, labelName := range allMissingLabels {
				l := labelName
				prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: &l})
			}
		}
	}
}

// syncReferencedIssueAssignees copies the human assignees from the referenced hierarchy to the PR.
func (s *Scanner) syncReferencedIssueAssignees(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue, refIssues []*githubv39.Issue) {
	if s.gh == nil || pr == nil || prIssue == nil || len(refIssues) == 0 {
		return
	}

	missingAssignees := getMissingHumanAssigneesForPR(prIssue.Assignees, refIssues, s.cfg.BotUsers, s.cfg.GitHubLogin)
	if len(missingAssignees) == 0 {
		return
	}

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would add human assignees %v to PR #%d\n", missingAssignees, pr.GetNumber())
		for _, name := range missingAssignees {
			login := name
			prIssue.Assignees = append(prIssue.Assignees, &githubv39.User{Login: &login})
		}
		return
	}

	klog.Infof("Adding inherited human assignees %v to PR #%d", missingAssignees, pr.GetNumber())
	if err := s.gh.AddAssignees(ctx, pr.GetNumber(), missingAssignees); err != nil {
		klog.Errorf("Failed to add human assignees %v to PR #%d: %v", missingAssignees, pr.GetNumber(), err)
	} else {
		for _, name := range missingAssignees {
			login := name
			prIssue.Assignees = append(prIssue.Assignees, &githubv39.User{Login: &login})
		}
	}
}

func isHumanUser(user *githubv39.User, botUsers []string, githubLogin string) bool {
	if user == nil || user.GetLogin() == "" {
		return false
	}
	if strings.EqualFold(user.GetType(), "Bot") {
		return false
	}
	loginLower := strings.ToLower(user.GetLogin())
	if strings.HasSuffix(loginLower, "[bot]") {
		return false
	}
	if githubLogin != "" && strings.EqualFold(user.GetLogin(), githubLogin) {
		return false
	}
	for _, bot := range botUsers {
		if strings.EqualFold(user.GetLogin(), bot) {
			return false
		}
	}
	return true
}

func getMissingHumanAssigneesForPR(prAssignees []*githubv39.User, refIssues []*githubv39.Issue, botUsers []string, githubLogin string) []string {
	prAssigneesSet := make(map[string]bool)
	for _, user := range prAssignees {
		if user.GetLogin() != "" {
			prAssigneesSet[strings.ToLower(user.GetLogin())] = true
		}
	}

	var missingAssignees []string
	seen := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}
		for _, user := range refIssue.Assignees {
			if !isHumanUser(user, botUsers, githubLogin) {
				continue
			}
			login := user.GetLogin()
			loginLower := strings.ToLower(login)
			if !prAssigneesSet[loginLower] && !seen[loginLower] {
				seen[loginLower] = true
				missingAssignees = append(missingAssignees, login)
			}
		}
	}

	return missingAssignees
}

// getMissingLabelsForPR returns the labels present on the referenced issues but
// not yet on the pull request.
func getMissingLabelsForPR(prLabels []*githubv39.Label, refIssues []*githubv39.Issue) []string {
	prLabelsSet := make(map[string]bool)
	for _, label := range prLabels {
		if label.GetName() != "" {
			prLabelsSet[label.GetName()] = true
		}
	}

	var allMissingLabels []string
	missingLabelsSet := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}

		for _, label := range refIssue.Labels {
			labelName := label.GetName()
			if labelName != "" && !prLabelsSet[labelName] && !missingLabelsSet[labelName] {
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

// hasReviewLabel reports whether a set of labels opts the change into automated
// review.
func hasReviewLabel(labels []*githubv39.Label, triggerLabel string) bool {
	reviewLabels := []string{"overseer/review"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		reviewLabels = append(reviewLabels, triggerLabel+"/review")
	}
	for _, label := range labels {
		for _, rev := range reviewLabels {
			if strings.EqualFold(label.GetName(), rev) {
				return true
			}
		}
	}
	return false
}

// shouldAutoReviewPR reports whether the pull request has been opted into
// automated review, either directly or through an issue it closes.
//
// Review is opt-in rather than universal because it costs an agent run per
// commit; the label is how a repository says a change is worth that.
func (s *Scanner) shouldAutoReviewPR(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue) bool {
	if hasReviewLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		return true
	}
	for refIssueNum := range common.GetReferencedIssues(pr) {
		refIssue, err := s.gh.GetIssue(ctx, refIssueNum)
		if err == nil && hasReviewLabel(refIssue.Labels, s.cfg.TriggerLabel) {
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

// reconcileReadyForHumanLabel brings the ready-for-human label in line with
// whether the pull request is actually waiting on a person.
//
// It runs in both directions. Removing the label matters as much as adding it:
// a pull request that was ready and then had CI break or a review land must
// stop advertising itself as done, or a human will review a change the watcher
// is about to push over.
func (s *Scanner) reconcileReadyForHumanLabel(ctx context.Context, num int, prIssue *githubv39.Issue, isReady bool, headSHA string) {
	if !s.gh.Ready() || prIssue == nil {
		return
	}
	readyLabel := readyForHumanLabel(s.cfg.TriggerLabel)
	hasLabel := hasReadyForHumanLabel(prIssue.Labels, s.cfg.TriggerLabel)

	if isReady && !hasLabel {
		if s.cfg.DryRun {
			fmt.Printf("[DRYRUN] Would add label '%s' to PR #%d (passed review on SHA %s)\n", readyLabel, num, headSHA)
		} else {
			klog.Infof("PR #%d passed automated review on SHA %s. Adding label '%s'.", num, headSHA, readyLabel)
			if err := s.gh.AddLabels(ctx, num, []string{readyLabel}); err != nil {
				klog.Errorf("Failed to add label '%s' to PR #%d: %v", readyLabel, num, err)
			}
		}
	} else if !isReady && hasLabel {
		if s.cfg.DryRun {
			fmt.Printf("[DRYRUN] Would remove label '%s' from PR #%d\n", readyLabel, num)
		} else {
			klog.Infof("PR #%d is no longer ready for human review. Removing label '%s'.", num, readyLabel)
			if err := s.gh.RemoveLabel(ctx, num, readyLabel); err != nil {
				klog.Errorf("Failed to remove label '%s' from PR #%d: %v", readyLabel, num, err)
			}
		}
	}
}
