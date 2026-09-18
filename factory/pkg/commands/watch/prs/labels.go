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

// syncReferencedIssueLabels copies the labels of the issues a pull request
// closes onto the pull request itself.
//
// Labels are how the repository directs the watcher - stop, review, priority -
// and they are naturally put on the issue, not on the fix. Inheriting them is
// what makes 'overseer/stop' on an issue actually stop work on its pull
// request, which is why the caller re-checks the stop label immediately after
// calling this.
//
// Non-sticky labels are the exception: they are handed over once rather than
// kept in sync. See nonStickyLabels.
func (s *Scanner) syncReferencedIssueLabels(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue) {
	num := pr.GetNumber()

	var refIssues []*githubv39.Issue
	for refIssueNum := range common.GetReferencedIssues(pr) {
		refIssue, err := s.gh.GetIssue(ctx, refIssueNum)
		if err != nil {
			klog.Warningf("Failed to fetch referenced parent issue #%d for PR #%d: %v", refIssueNum, num, err)
			continue
		}
		refIssues = append(refIssues, refIssue)
	}

	allMissingLabels := s.filterReclaimedLabels(ctx, num, getMissingLabelsForPR(prIssue.Labels, refIssues))
	if len(allMissingLabels) == 0 {
		return
	}

	klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, num)
	if err := s.gh.AddLabels(ctx, num, allMissingLabels); err != nil {
		klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, num, err)
		return
	}

	// Record the add on the in-memory issue as well. The rest of this
	// evaluation reads prIssue.Labels - the stop label re-check the caller does
	// next, and the review opt-in, which now consults only the pull request's
	// own labels - and without this it would be acting on the label set as it
	// was before the inheritance, a cycle behind.
	for _, name := range allMissingLabels {
		prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: githubv39.String(name)})
	}
}

// nonStickyLabels returns the labels that a pull request inherits from its
// parent issue only once, instead of having them kept in sync with it.
//
// Ordinary inherited labels are reconciled every cycle: the issue is the source
// of truth and the pull request follows it, so a label removed from the pull
// request comes back. A non-sticky label is one where that is the wrong
// behaviour, because its *absence* on a pull request is itself an instruction -
// a person overriding, for this one change, what the parent issue asks for.
// Putting it back would be the watcher arguing with them.
//
// Only the review opt-in qualifies today. Taking 'overseer/review' off a pull
// request is how a reviewer says they are handling this one themselves, and the
// alternative way to say it - unlabelling the parent issue - is too blunt,
// because it would also opt out every sibling pull request the issue spawns.
//
// Enrol another label here only when the same reasoning holds for it: that a
// human removing it from a single pull request means something the parent issue
// has no way to express. A label whose removal is merely housekeeping should
// stay sticky, so that the issue remains the one place to look.
func nonStickyLabels(triggerLabel string) []string {
	return reviewLabelNames(triggerLabel)
}

// isNonStickyLabel reports whether a label is inherited once rather than kept
// in sync. See nonStickyLabels.
func isNonStickyLabel(name string, triggerLabel string) bool {
	for _, nonSticky := range nonStickyLabels(triggerLabel) {
		if strings.EqualFold(name, nonSticky) {
			return true
		}
	}
	return false
}

// filterReclaimedLabels drops from labels the non-sticky ones that have already
// been removed from the pull request, leaving the rest untouched.
//
// Removals are read back from the pull request's own event log rather than only
// remembered locally, so a reclaimed label stays reclaimed across a restart and
// is honoured even by a watcher that was not running when it came off.
//
// When the log cannot be read the non-sticky labels are held back rather than
// applied. The cost of guessing wrong in that direction is a cycle of delay
// before a new pull request inherits them; guessing wrong in the other
// direction is the label fight this exists to prevent.
func (s *Scanner) filterReclaimedLabels(ctx context.Context, num int, labels []string) []string {
	// Answer from the memo where possible. Without it the event log of every
	// pull request holding a reclaimed label would be re-read on every cycle,
	// for the rest of that pull request's life, to be told the same thing.
	needsLookup := false
	for _, name := range labels {
		if isNonStickyLabel(name, s.cfg.TriggerLabel) && !s.state.isLabelReclaimed(num, name) {
			needsLookup = true
			break
		}
	}

	known := true
	if needsLookup {
		reclaimed, complete := s.reclaimedLabels(ctx, num)
		s.state.markLabelsReclaimed(num, reclaimed)
		known = complete
	}

	filtered := make([]string, 0, len(labels))
	for _, name := range labels {
		if !isNonStickyLabel(name, s.cfg.TriggerLabel) {
			filtered = append(filtered, name)
			continue
		}
		switch {
		case s.state.isLabelReclaimed(num, name):
			klog.V(2).Infof("Not inheriting %q onto PR #%d: it was already removed from the pull request", name, num)
		case !known:
			klog.Warningf("Not inheriting %q onto PR #%d: its label history could not be read", name, num)
		default:
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// reclaimedLabels returns the set of non-sticky labels that have been removed
// from a pull request at some point, lowercased for case-insensitive lookup.
//
// The boolean return reports whether the answer is trustworthy: it is false
// when the event log could not be fetched or was too long to read in full, in
// which case the absence of a removal is not evidence that none happened.
func (s *Scanner) reclaimedLabels(ctx context.Context, num int) (map[string]bool, bool) {
	reclaimed := make(map[string]bool)

	events, complete, err := s.gh.ListIssueEvents(ctx, num)
	if err != nil {
		klog.Warningf("Failed to list events for PR #%d: %v", num, err)
		return reclaimed, false
	}

	for _, event := range events {
		if event.GetEvent() != "unlabeled" {
			continue
		}
		name := event.GetLabel().GetName()
		if name != "" && isNonStickyLabel(name, s.cfg.TriggerLabel) {
			reclaimed[strings.ToLower(name)] = true
		}
	}
	return reclaimed, complete
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

// reviewLabelNames returns the labels that opt a change into automated review.
//
// 'overseer/review' is always accepted so that a repository which renames its
// trigger label does not silently lose the opt-in on changes already carrying
// the old spelling.
func reviewLabelNames(triggerLabel string) []string {
	reviewLabels := []string{"overseer/review"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		reviewLabels = append(reviewLabels, triggerLabel+"/review")
	}
	return reviewLabels
}

// isReviewLabel reports whether a label name is one of the review opt-in
// labels.
func isReviewLabel(name string, triggerLabel string) bool {
	for _, rev := range reviewLabelNames(triggerLabel) {
		if strings.EqualFold(name, rev) {
			return true
		}
	}
	return false
}

// hasReviewLabel reports whether a set of labels opts the change into automated
// review.
func hasReviewLabel(labels []*githubv39.Label, triggerLabel string) bool {
	for _, label := range labels {
		if isReviewLabel(label.GetName(), triggerLabel) {
			return true
		}
	}
	return false
}

// shouldAutoReviewPR reports whether the pull request has been opted into
// automated review.
//
// Review is opt-in rather than universal because it costs an agent run per
// commit; the label is how a repository says a change is worth that.
//
// Only the pull request's own labels are consulted. The label reaches a new
// pull request by being inherited from the issue it closes
// (syncReferencedIssueLabels), so consulting the parent here as well would
// be redundant in the ordinary case and wrong in the one that matters: it
// would make taking the label off the pull request have no effect, leaving a
// person who wants to review the change themselves with no way to say so short
// of opting the parent issue out entirely.
func (s *Scanner) shouldAutoReviewPR(prIssue *githubv39.Issue) bool {
	return hasReviewLabel(prIssue.Labels, s.cfg.TriggerLabel)
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
