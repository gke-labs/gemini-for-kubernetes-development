package prs

import (
	"context"
	"fmt"
	"os"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// maxInvestigations is how many times CI failures on the same revision are
// investigated before the watcher gives up and hands the pull request back.
//
// Three attempts is the point past which the agent is demonstrably not making
// progress on its own, and continuing would burn agent runs in a loop. The
// counter resets on a new commit or a human comment, so this is a limit on
// futile repetition rather than on the pull request as a whole.
const maxInvestigations = 3

// investigationRetryAfter is how long after an investigation the same revision
// becomes worth investigating again.
//
// Re-running against an unchanged commit is normally pointless, but flaky
// infrastructure and transient failures do resolve themselves, and after a
// couple of hours a retry is cheaper than a stuck pull request.
const investigationRetryAfter = 2 * time.Hour

// prContext is the per-pull-request facts each handler needs, assembled once so
// that the handlers do not each re-derive them.
type prContext struct {
	pr                   *githubv39.PullRequest
	prIssue              *githubv39.Issue
	headSHA              string
	shortSHA             string
	lastCommitTime       time.Time
	taskAssignee         string
	isExplicitlyAssigned bool
	prURL                string
	// refIssues resolves the issues this pull request closes. It is shared
	// with the evaluation that built this context, so a handler reading it
	// spends nothing: by the time a handler runs, the issues have already been
	// fetched for the label sync.
	refIssues *refIssues
}

// handlePRIterate queues a rebase for a pull request that conflicts with its
// base branch.
//
// It is gated on the head SHA rather than on time: if the last rebase produced
// no new commit then it did not resolve anything, and running it again against
// the identical tree would produce the identical result.
func (s *Scanner) handlePRIterate(ctx context.Context, pc *prContext) {
	num := pc.prIssue.GetNumber()
	state := s.state.get(num)

	// A conflicted pull request is never ready for a human, whatever it looked
	// like before.
	s.reconcileReadyForHumanLabel(ctx, num, pc.prIssue, false, pc.headSHA)
	if state.lastIteratedSHA != "" && state.lastIteratedSHA == pc.headSHA {
		klog.Infof("Skipping PR #%d rebase/conflict resolution because an iterate task was already processed for head SHA %s.", num, pc.headSHA)
		return
	}

	filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
	if s.queue.TaskExists(filename) {
		return
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRIterate, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		klog.Infof("Skipping PR #%d rebase because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	baseRef := ""
	if pc.pr.GetBase() != nil {
		baseRef = pc.pr.GetBase().GetRef()
	}
	notes := fmt.Sprintf("PR #%d has merge conflicts with base branch '%s'; head commit %s committer date %s, PR updated at %s", num, baseRef, pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339), pc.pr.GetUpdatedAt().Format(time.RFC3339))

	task := s.newTask(taskOptions{
		Type:             api.TypePRIterate,
		PR:               pc.pr,
		PRIssue:          pc.prIssue,
		Phase:            api.PhaseRebase,
		Assignee:         pc.taskAssignee,
		CommitSHA:        pc.headSHA,
		TriggerEventTime: pc.lastCommitTime,
		TriggerReason:    api.TriggerReasonPRMergeConflict,
		TriggerNotes:     notes,
	})

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue rebase task for PR #%d: %s\n", num, pc.prURL)
		return
	}
	fmt.Printf("Queueing rebase task for PR #%d...\n", num)
	// The state is recorded before the enqueue rather than after, so that a
	// failure to write the task file does not leave the next cycle thinking the
	// work is still outstanding while the file is in fact being retried.
	state.lastIteratedSHA = pc.headSHA
	state.lastIteratedTime = time.Now()
	s.state.set(num, state)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue rebase task for PR #%d: %v", num, err)
	}
}

// canInvestigatePR reports whether CI failures on this pull request are worth
// another look.
//
// Hitting the retry limit counts as "yes" so that handlePRInvestigate is
// reached and can apply the stop label: the decision to give up has to be
// announced on the pull request, not made silently here.
func (s *Scanner) canInvestigatePR(
	num int,
	headSHA string,
	isExplicitlyAssigned bool,
	state prState,
	comments []*githubv39.IssueComment,
	lastCommitTime time.Time,
) bool {
	filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
	if s.queue.TaskExists(filename) {
		return false
	}
	if getInvestigationCount(comments, lastCommitTime, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TriggerLabel) >= maxInvestigations {
		return true
	}
	return state.lastInvestigatedSHA != headSHA ||
		s.lastInvestigationFailed(filename) ||
		isExplicitlyAssigned ||
		time.Since(state.lastInvestigatedTime) > investigationRetryAfter
}

// lastInvestigationFailed reports whether the previous investigation task ended
// in failure, which makes the same revision worth retrying: the agent never got
// to finish, so its verdict says nothing about the CI failure.
func (s *Scanner) lastInvestigationFailed(filename string) bool {
	last := s.queue.GetProcessedTask(filename)
	return last != nil && last.Status == api.StatusFailed
}

// handlePRInvestigate queues an investigation of the pull request's CI
// failures, or gives up and pauses the pull request once the retry limit has
// been reached.
func (s *Scanner) handlePRInvestigate(
	ctx context.Context,
	pc *prContext,
	checkAnalysis prCheckAnalysis,
	comments []*githubv39.IssueComment,
) {
	num := pc.prIssue.GetNumber()
	state := s.state.get(num)
	filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)

	if s.queue.TaskExists(filename) {
		return
	}

	investigationCount := getInvestigationCount(comments, pc.lastCommitTime, s.cfg.BotUsers, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TriggerLabel)
	if investigationCount >= maxInvestigations {
		stopLabel := conventions.StopLabel(s.cfg.TriggerLabel)
		if !s.cfg.DryRun {
			s.comment(ctx, num, fmt.Sprintf("🤖 AI Factory has attempted to investigate/fix CI check failures for this pull request %d times since the last commit or update without success. To prevent infinite loops, I am pausing automated investigation and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment).", maxInvestigations, stopLabel, stopLabel))
			if err := s.gh.AddLabels(ctx, num, []string{stopLabel}); err != nil {
				klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
			}
		}
		klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (%d attempts since last update) and applying stop label '%s'.", num, maxInvestigations, stopLabel)
		return
	}

	if state.lastInvestigatedSHA == pc.headSHA &&
		!s.lastInvestigationFailed(filename) &&
		!pc.isExplicitlyAssigned &&
		time.Since(state.lastInvestigatedTime) <= investigationRetryAfter {
		return
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRInvestigate, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	eventTime := checkAnalysis.earliestFailureTime
	if eventTime.IsZero() {
		eventTime = pc.lastCommitTime
	}
	failName := checkAnalysis.earliestFailureName
	if failName == "" {
		failName = "unknown check"
	}
	failConclusion := checkAnalysis.earliestFailureConclusion
	if failConclusion == "" {
		failConclusion = "failed"
	}
	notes := fmt.Sprintf("Earliest CI failure in '%s' (%s) at %s; total %d failed check(s) on commit %s", failName, failConclusion, eventTime.Format(time.RFC3339), checkAnalysis.failedCount, pc.shortSHA)

	task := s.newTask(taskOptions{
		Type:             api.TypePRInvestigate,
		PR:               pc.pr,
		PRIssue:          pc.prIssue,
		Phase:            api.PhaseInvestigate,
		Assignee:         pc.taskAssignee,
		CommitSHA:        pc.headSHA,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCheckFailed,
		TriggerNotes:     notes,
	})

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue investigate task for PR #%d: %s\n", num, pc.prURL)
		return
	}
	fmt.Printf("Queueing investigate task for PR #%d...\n", num)
	state.lastInvestigatedSHA = pc.headSHA
	state.lastInvestigatedTime = time.Now()
	s.state.set(num, state)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue investigate task for PR #%d: %v", num, err)
	}
}

// handlePRComments queues a task to address the outstanding review feedback.
func (s *Scanner) handlePRComments(ctx context.Context, pc *prContext, commentAnalysis prCommentAnalysis) {
	if os.Getenv("DRY_RUN") == "true" {
		return
	}
	num := pc.prIssue.GetNumber()
	state := s.state.get(num)
	filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)

	if s.queue.TaskExists(filename) {
		return
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRComments, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	commitInfo := ""
	if !pc.lastCommitTime.IsZero() {
		commitInfo = fmt.Sprintf(" since last commit %s (committer date %s)", pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339))
	}
	authorStr := ""
	if commentAnalysis.oldestCommentAuthor != "" {
		authorStr = fmt.Sprintf(" by %s", commentAnalysis.oldestCommentAuthor)
	}
	cType := commentAnalysis.oldestCommentType
	if cType == "" {
		cType = "comment"
	}
	notes := fmt.Sprintf("Oldest unaddressed %s%s added at %s (ID %d)%s", cType, authorStr, commentAnalysis.oldestCommentTime.Format(time.RFC3339), commentAnalysis.oldestCommentID, commitInfo)

	task := s.newTask(taskOptions{
		Type:             api.TypePRComments,
		PR:               pc.pr,
		PRIssue:          pc.prIssue,
		Phase:            api.PhaseComments,
		Assignee:         pc.taskAssignee,
		CommitSHA:        pc.headSHA,
		TriggerEventTime: commentAnalysis.oldestCommentTime,
		TriggerReason:    api.TriggerReasonPRCommentsAdded,
		TriggerNotes:     notes,
	})

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, pc.prURL)
		return
	}
	fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
	// The acknowledgement reactions are what tell the next cycle these comments
	// are already spoken for, and what tell the commenter they were seen.
	for _, cid := range commentAnalysis.unackCommentIDs {
		s.react(ctx, cid, conventions.ReactionAcknowledged)
	}
	for _, cid := range commentAnalysis.unackPRCommentIDs {
		s.reactToReviewComment(ctx, cid, conventions.ReactionAcknowledged)
	}
	state.lastCommentAddressedTime = time.Now()
	state.lastCommentAddressedSHA = pc.headSHA
	s.state.set(num, state)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
	}
}

// handlePRReview queues an automated review of a green, unreviewed pull request.
//
// Review instructions are collected from the pull request body and from the
// issues it closes, so that a repository can steer the review from wherever the
// requirement was written down.
func (s *Scanner) handlePRReview(ctx context.Context, pc *prContext, checkRuns []*githubv39.CheckRun) {
	if os.Getenv("DRY_RUN") == "true" {
		return
	}
	num := pc.prIssue.GetNumber()
	state := s.state.get(num)
	filename := fmt.Sprintf("task-pr-%d-review.yaml", num)

	if s.queue.TaskExists(filename) {
		return
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRReview, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		klog.Infof("Skipping PR #%d review because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	var bodies []string
	if pc.pr.GetBody() != "" {
		bodies = append(bodies, pc.pr.GetBody())
	}
	for _, refIssue := range pc.refIssues.all(ctx) {
		if refIssue.GetBody() != "" {
			bodies = append(bodies, refIssue.GetBody())
		}
	}
	instructions := common.ExtractReviewInstructions(bodies...)

	// The review is triggered by CI going green, so that is the event it is
	// timed from whenever the checks finished after the commit landed.
	var latestCheckCompletedTime time.Time
	for _, run := range checkRuns {
		t := run.GetCompletedAt().Time
		if t.After(latestCheckCompletedTime) {
			latestCheckCompletedTime = t
		}
	}
	eventTime := pc.lastCommitTime
	var notes string
	if !latestCheckCompletedTime.IsZero() && latestCheckCompletedTime.After(pc.lastCommitTime) {
		eventTime = latestCheckCompletedTime
		notes = fmt.Sprintf("Automated review triggered; all CI checks passed at %s for commit %s (committer date %s)", latestCheckCompletedTime.Format(time.RFC3339), pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339))
	} else {
		notes = fmt.Sprintf("Automated review triggered for commit %s at %s", pc.shortSHA, eventTime.Format(time.RFC3339))
	}

	task := s.newTask(taskOptions{
		Type:             api.TypePRReview,
		PR:               pc.pr,
		PRIssue:          pc.prIssue,
		Phase:            api.PhaseComments,
		CommitSHA:        pc.headSHA,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRReadyForReview,
		TriggerNotes:     notes,
		Instructions:     instructions,
	})

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue review task for PR #%d: %s\n", num, pc.prURL)
		return
	}
	fmt.Printf("Queueing review task for PR #%d (Instructions: %d)...\n", num, len(instructions))
	state.lastReviewedSHA = pc.headSHA
	s.state.set(num, state)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue review task for PR #%d: %v", num, err)
	}
}
