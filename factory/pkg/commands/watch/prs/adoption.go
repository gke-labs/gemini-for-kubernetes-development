package prs

import (
	"context"
	"fmt"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// adoptOrphanedBotPRs gives the labels of their parent issues to bot pull
// requests that carry none of the watcher's own.
//
// The scan lists candidates by trigger label and by assignee, so a pull request
// with neither is invisible to it - and the label inheritance that would have
// fixed that runs inside the evaluation it never gets. The result is a pull
// request nothing looks at again: no CI investigation, no automated review, no
// ready-for-human. That is not hypothetical. It is what happens whenever a
// watch cycle recycles while a fix task is still running: the factory process
// that would have labelled the pull request is killed, while the task itself
// keeps going detached and opens one anyway.
//
// This runs from the sweep, over the open pull requests it has already listed,
// so finding the orphans costs nothing. It runs before the candidate listing so
// that a pull request adopted here is evaluated in the same cycle.
func (s *Scanner) adoptOrphanedBotPRs(ctx context.Context, prs []*githubv39.PullRequest) {
	for _, pr := range prs {
		if !s.isOrphanedBotPR(pr) {
			continue
		}
		s.adoptOrphanedBotPR(ctx, pr)
	}
}

// isOrphanedBotPR reports whether a pull request is one of ours that the scan
// cannot see.
//
// Authorship is the qualifying test rather than a convenience: the watcher can
// only work on a pull request whose branch it can push to, which is the same
// reason evaluate skips everything not written by the pool. Anything the scan
// would already have listed - labelled, or assigned to a bot - is left alone,
// so this only ever looks at pull requests nothing else will.
func (s *Scanner) isOrphanedBotPR(pr *githubv39.PullRequest) bool {
	if pr == nil {
		return false
	}
	num := pr.GetNumber()
	if num == 0 || (s.cfg.MinNumber > 0 && num < s.cfg.MinNumber) {
		return false
	}

	author := pr.GetUser().GetLogin()
	isBotPR := false
	for _, bot := range s.cfg.BotUsers {
		if strings.EqualFold(author, bot) {
			isBotPR = true
			break
		}
	}
	if !isBotPR {
		return false
	}

	if conventions.HasTriggerLabel(pr.Labels, s.cfg.TriggerLabel) {
		return false
	}
	return conventions.AssignedBotUserFrom(pr.Assignees, s.cfg.BotUsers) == ""
}

// adoptOrphanedBotPR inherits the labels of the issues an orphaned pull request
// closes, provided those issues say the work is the watcher's to pick up.
//
// The trigger label on a parent issue is what makes the adoption safe. A bot
// account opens pull requests the watcher has no business claiming - a
// dependency bump, a chore run from another pipeline - and from here they look
// exactly like a fix. Requiring the parent to be labelled means the watcher
// only adopts work the repository already pointed it at.
//
// A referenced number that cannot be read is skipped rather than fatal: the
// references are parsed out of the branch name and title as well as the body,
// so the set routinely contains pull request numbers and stale references that
// were never issues at all.
func (s *Scanner) adoptOrphanedBotPR(ctx context.Context, pr *githubv39.PullRequest) {
	num := pr.GetNumber()

	refIssueNums := common.GetReferencedIssues(pr)
	if len(refIssueNums) == 0 {
		return
	}

	var refIssues []*githubv39.Issue
	triggered := false
	for refIssueNum := range refIssueNums {
		refIssue, err := s.gh.GetIssue(ctx, refIssueNum)
		if err != nil {
			if !github.IsNotFound(err) {
				klog.Warningf("Failed to fetch referenced parent issue #%d while considering orphaned PR #%d: %v", refIssueNum, num, err)
			}
			continue
		}
		refIssues = append(refIssues, refIssue)
		if conventions.HasTriggerLabel(refIssue.Labels, s.cfg.TriggerLabel) {
			triggered = true
		}
	}
	if !triggered {
		return
	}

	missing := getMissingLabelsForPR(pr.Labels, refIssues, s.cfg.TriggerLabel)
	if len(missing) == 0 {
		return
	}

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would adopt unlabelled PR #%d by adding inherited labels %v\n", num, missing)
		return
	}

	klog.Infof("Adopting PR #%d: authored by the bot pool but carrying no trigger label. Adding inherited labels %v.", num, missing)
	if err := s.gh.AddLabels(ctx, num, missing); err != nil {
		klog.Errorf("Failed to add inherited labels %v to orphaned PR #%d: %v", missing, num, err)
	}
}
