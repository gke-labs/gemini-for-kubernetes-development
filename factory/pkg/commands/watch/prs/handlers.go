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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

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

// handlePRIterate queues the rebase the engine decided for a pull request
// that conflicts with its base branch (once per head: the rule's dedupe).
func (s *Scanner) handlePRIterate(ctx context.Context, pc *prContext) {
	num := pc.prIssue.GetNumber()
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
		s.rec.Gated("its sandbox is busy")
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
	s.rec.Decided("conflict", rules.OutcomeRun)
	s.stampDecision(task, "conflict")
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue rebase task for PR #%d: %v", num, err)
	}
}

// handlePRInvestigate queues the investigation of the pull request's CI
// failures the engine decided (its give-up and cooldown are the rule's). It
// reports whether the phase completed cleanly; a false return means a
// sandbox probe or enqueue failed and the pull request must be looked at
// again next cycle.
func (s *Scanner) handlePRInvestigate(ctx context.Context, pc *prContext, checkAnalysis prCheckAnalysis) bool {
	num := pc.prIssue.GetNumber()
	filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
	if s.queue.TaskExists(filename) {
		return true
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRInvestigate, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
		return false
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
		return true
	}
	fmt.Printf("Queueing investigate task for PR #%d...\n", num)
	s.rec.Decided("ci", rules.OutcomeRun)
	s.stampDecision(task, "ci")
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue investigate task for PR #%d: %v", num, err)
		return false
	}
	return true
}

// handlePRComments queues the address-comments task the engine decided:
// for new trusted feedback, or again after a run that failed on this head
// (its give-up is the rule's). It reports whether the phase completed
// cleanly; a false return means a sandbox probe or enqueue failed and the
// pull request must be looked at again next cycle.
func (s *Scanner) handlePRComments(ctx context.Context, pc *prContext, commentAnalysis prCommentAnalysis) bool {
	if os.Getenv("DRY_RUN") == "true" {
		return true
	}
	num := pc.prIssue.GetNumber()
	filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
	if s.queue.TaskExists(filename) {
		return true
	}

	var eventTime time.Time
	var notes string
	retryCount := 0
	if commentAnalysis.hasNewComments {
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
		eventTime = commentAnalysis.oldestCommentTime
		notes = fmt.Sprintf("Oldest unaddressed %s%s added at %s (ID %d)%s", cType, authorStr, commentAnalysis.oldestCommentTime.Format(time.RFC3339), commentAnalysis.oldestCommentID, commitInfo)
	} else {
		// Again after a run that failed on this head (last-run: failed).
		if last := s.queue.GetProcessedTask(filename); last != nil {
			retryCount = last.RetryCount + 1
			eventTime = last.TriggerEventTime
		}
		if eventTime.IsZero() {
			eventTime = pc.lastCommitTime
		}
		notes = fmt.Sprintf("Retrying the failed address-comments task (retry %d) on commit %s", max(retryCount, 1), pc.shortSHA)
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRComments, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
		return false
	}

	task := s.newTask(taskOptions{
		Type:             api.TypePRComments,
		PR:               pc.pr,
		PRIssue:          pc.prIssue,
		Phase:            api.PhaseComments,
		Assignee:         pc.taskAssignee,
		CommitSHA:        pc.headSHA,
		RetryCount:       retryCount,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCommentsAdded,
		TriggerNotes:     notes,
	})

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, pc.prURL)
		return true
	}
	fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
	s.rec.Decided("comments", rules.OutcomeRun)
	s.stampDecision(task, "comments")
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
		return false
	}
	// The feedback is acknowledged (the reactions, the addressed time)
	// when the task starts, not here (NotifyTaskStarted): a task dropped
	// before it starts leaves it for the next scan. Queued, the task is
	// what keeps the next cycle from queueing it again.
	return true
}

// handlePRReview queues an automated review of a green, unreviewed pull request.
//
// Review instructions are collected from the pull request body and from the
// issues it closes, so that a repository can steer the review from wherever the
// requirement was written down. It reports whether the phase completed cleanly;
// a false return means a sandbox probe or enqueue failed and the pull request
// must be looked at again next cycle.
func (s *Scanner) handlePRReview(ctx context.Context, pc *prContext, checkRuns []*githubv39.CheckRun) bool {
	if os.Getenv("DRY_RUN") == "true" {
		return true
	}
	num := pc.prIssue.GetNumber()
	filename := fmt.Sprintf("task-pr-%d-review.yaml", num)

	if s.queue.TaskExists(filename) {
		return true
	}

	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRReview, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping PR #%d review because there is an in-flight sandbox %s.", num, sandboxName)
		return false
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
		return true
	}
	fmt.Printf("Queueing review task for PR #%d (Instructions: %d)...\n", num, len(instructions))
	s.rec.Decided("review", rules.OutcomeRun)
	s.stampDecision(task, "review")
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue review task for PR #%d: %v", num, err)
		return false
	}
	return true
}

// handleRuleTask queues the task of a rule factory does not handle by name
// (a repository's, .agents/watch) that the engine decided on the pull
// request: its recipe, apply and inputs as the rule says (act.Task), in
// api.RuleTaskFile. A rule that takes the PR with GitHub writes and no
// recipe is carried out as its writes.
func (s *Scanner) handleRuleTask(ctx context.Context, pc *prContext, rule string) bool {
	if r := s.rules.Set().Named(rule); r == nil || r.Run.Recipe == "" {
		s.actOn(ctx, pc.prIssue, rule)
		return true
	}
	if os.Getenv("DRY_RUN") == "true" {
		return true
	}
	num := pc.prIssue.GetNumber()
	filename := api.RuleTaskFile(true, num, rule)
	if s.queue.TaskExists(filename) {
		return true
	}
	sandboxName := s.sandboxes.ResolveName(ctx, api.TypePRRule, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping PR #%d rule %s because there is an in-flight sandbox %s.", num, rule, sandboxName)
		return false
	}
	task := s.newTask(taskOptions{
		Type:          api.TypePRRule,
		PR:            pc.pr,
		PRIssue:       pc.prIssue,
		Phase:         api.PhaseComments,
		Assignee:      pc.taskAssignee,
		CommitSHA:     pc.headSHA,
		TriggerReason: api.TriggerReasonRuleDecided,
		TriggerNotes:  fmt.Sprintf("The %s rule decided to run on commit %s", rule, pc.shortSHA),
	})
	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue the %s rule's task for PR #%d: %s\n", rule, num, pc.prURL)
		return true
	}
	fmt.Printf("Queueing the %s rule's task for PR #%d...\n", rule, num)
	s.rec.Decided(rule, rules.OutcomeRun)
	s.stampDecision(task, rule)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue the %s rule's task for PR #%d: %v", rule, num, err)
		return false
	}
	return true
}
