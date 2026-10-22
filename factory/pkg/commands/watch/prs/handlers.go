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
	// checks and comments are the evaluation's reading of the pull
	// request's CI and feedback, which a rule's trigger is timed from.
	checks   prCheckAnalysis
	comments prCommentAnalysis
}

// prRuleTask is how a PR rule's task is queued: its file
// (task-pr-<n>-<suffix>.yaml) and type, what the logs call it, whether the
// DRY_RUN environment skips it, and its trigger - the rest of the task the
// rule's decision does not say (assignee, when and why; the phase is the
// rule's policy.phase). Factory's
// four pr-care rules have their own (prRuleTasks); any other rule's is a
// rule task (ruleTaskOf).
type prRuleTask struct {
	rule, suffix string
	typ          api.TaskType
	what         string
	envDryRun    bool
	trigger      func(ctx context.Context, s *Scanner, pc *prContext, filename string) taskOptions
}

// prRuleTasks are factory's pr-care rules' tasks, in the order their
// history is read (fillRuns).
var prRuleTasks = []prRuleTask{
	{rule: "comments", suffix: "comments", typ: api.TypePRComments, what: "address-comments", envDryRun: true, trigger: commentsTrigger},
	{rule: "conflict", suffix: "iterate", typ: api.TypePRIterate, what: "rebase", trigger: conflictTrigger},
	{rule: "ci", suffix: "investigate", typ: api.TypePRInvestigate, what: "investigate", trigger: ciTrigger},
	{rule: "review", suffix: "review", typ: api.TypePRReview, what: "review", envDryRun: true, trigger: reviewTrigger},
}

// prRuleTaskOf is rule's task, factory's or a rule task; false for a rule
// that runs no recipe (its decision is its writes).
func (s *Scanner) prRuleTaskOf(rule string) (prRuleTask, bool) {
	for _, t := range prRuleTasks {
		if t.rule == rule {
			return t, true
		}
	}
	if r := s.rules.Set().Named(rule); r == nil || r.Run.Recipe == "" {
		return prRuleTask{}, false
	}
	return ruleTaskOf(rule), true
}

// ruleTaskOf is the task of a rule factory does not handle by name (a
// repository's, .agents/watch): api.RuleTaskFile, type pr-rule.
func ruleTaskOf(rule string) prRuleTask {
	return prRuleTask{
		rule: rule, suffix: "rule-" + rule, typ: api.TypePRRule, what: "the " + rule + " rule's", envDryRun: true,
		trigger: func(_ context.Context, _ *Scanner, pc *prContext, _ string) taskOptions {
			return taskOptions{
				// Where the rule's policy sets no phase.
				Phase:         api.PhaseComments,
				Assignee:      pc.taskAssignee,
				TriggerReason: api.TriggerReasonRuleDecided,
				TriggerNotes:  fmt.Sprintf("The %s rule decided to run on commit %s", rule, pc.shortSHA),
			}
		},
	}
}

// queueRuleTask queues the task the engine decided for rule on the pull
// request: once (a task queued under its file stands), not while its
// sandbox is busy (gated), with the rule's recipe, apply and inputs on it
// (stampDecision) and its trigger. A rule that runs no recipe is carried
// out as its writes. It reports whether the phase completed cleanly; false
// means a sandbox probe or enqueue failed, and the pull request is looked
// at again next cycle.
func (s *Scanner) queueRuleTask(ctx context.Context, pc *prContext, rule string) bool {
	t, ok := s.prRuleTaskOf(rule)
	if !ok {
		s.actOn(ctx, pc.prIssue, rule)
		return true
	}
	if t.envDryRun && os.Getenv("DRY_RUN") == "true" {
		return true
	}
	num := pc.prIssue.GetNumber()
	filename := fmt.Sprintf("task-pr-%d-%s.yaml", num, t.suffix)
	if s.queue.TaskExists(filename) {
		return true
	}

	sandboxName := s.sandboxes.ResolveName(ctx, t.typ, s.rules.Set().Named(rule).Run.Recipe, num)
	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping PR #%d %s because there is an in-flight sandbox %s.", num, t.what, sandboxName)
		return false
	}

	opts := t.trigger(ctx, s, pc, filename)
	opts.Type, opts.PR, opts.PRIssue, opts.CommitSHA = t.typ, pc.pr, pc.prIssue, pc.headSHA
	task := s.newTask(opts)
	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue %s task for PR #%d: %s\n", t.what, num, pc.prURL)
		return true
	}
	fmt.Printf("Queueing %s task for PR #%d...\n", t.what, num)
	s.rec.Decided(rule, rules.OutcomeRun)
	s.stampDecision(task, rule)
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue %s task for PR #%d: %v", t.what, num, err)
		return false
	}
	// A comments task's feedback is acknowledged (the reactions, the
	// addressed time) when the task starts, not here (NotifyTaskStarted):
	// a task dropped before it starts leaves it for the next scan.
	return true
}

// conflictTrigger is a rebase's: the pull request conflicts with its base
// branch, timed from its last commit.
func conflictTrigger(_ context.Context, _ *Scanner, pc *prContext, _ string) taskOptions {
	num := pc.prIssue.GetNumber()
	baseRef := ""
	if pc.pr.GetBase() != nil {
		baseRef = pc.pr.GetBase().GetRef()
	}
	return taskOptions{
		Assignee:         pc.taskAssignee,
		TriggerEventTime: pc.lastCommitTime,
		TriggerReason:    api.TriggerReasonPRMergeConflict,
		TriggerNotes:     fmt.Sprintf("PR #%d has merge conflicts with base branch '%s'; head commit %s committer date %s, PR updated at %s", num, baseRef, pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339), pc.pr.GetUpdatedAt().Format(time.RFC3339)),
	}
}

// ciTrigger is an investigation's: timed from the earliest CI failure.
func ciTrigger(_ context.Context, _ *Scanner, pc *prContext, _ string) taskOptions {
	checks := pc.checks
	eventTime := checks.earliestFailureTime
	if eventTime.IsZero() {
		eventTime = pc.lastCommitTime
	}
	failName := checks.earliestFailureName
	if failName == "" {
		failName = "unknown check"
	}
	failConclusion := checks.earliestFailureConclusion
	if failConclusion == "" {
		failConclusion = "failed"
	}
	return taskOptions{
		Assignee:         pc.taskAssignee,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCheckFailed,
		TriggerNotes:     fmt.Sprintf("Earliest CI failure in '%s' (%s) at %s; total %d failed check(s) on commit %s", failName, failConclusion, eventTime.Format(time.RFC3339), checks.failedCount, pc.shortSHA),
	}
}

// commentsTrigger is an address-comments task's: timed from the oldest
// unaddressed feedback, or, again after a run that failed on this head
// (last-run: failed), from that run's trigger, counting the retry.
func commentsTrigger(_ context.Context, s *Scanner, pc *prContext, filename string) taskOptions {
	comments := pc.comments
	var eventTime time.Time
	var notes string
	retryCount := 0
	if comments.hasNewComments {
		commitInfo := ""
		if !pc.lastCommitTime.IsZero() {
			commitInfo = fmt.Sprintf(" since last commit %s (committer date %s)", pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339))
		}
		authorStr := ""
		if comments.oldestCommentAuthor != "" {
			authorStr = fmt.Sprintf(" by %s", comments.oldestCommentAuthor)
		}
		cType := comments.oldestCommentType
		if cType == "" {
			cType = "comment"
		}
		eventTime = comments.oldestCommentTime
		notes = fmt.Sprintf("Oldest unaddressed %s%s added at %s (ID %d)%s", cType, authorStr, comments.oldestCommentTime.Format(time.RFC3339), comments.oldestCommentID, commitInfo)
	} else {
		if last := s.queue.GetProcessedTask(filename); last != nil {
			retryCount = last.RetryCount + 1
			eventTime = last.TriggerEventTime
		}
		if eventTime.IsZero() {
			eventTime = pc.lastCommitTime
		}
		notes = fmt.Sprintf("Retrying the failed address-comments task (retry %d) on commit %s", max(retryCount, 1), pc.shortSHA)
	}
	return taskOptions{
		Assignee:         pc.taskAssignee,
		RetryCount:       retryCount,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCommentsAdded,
		TriggerNotes:     notes,
	}
}

// reviewTrigger is an automated review's: timed from when the checks went
// green, with the review instructions the pull request and the issues it
// closes carry, so a repository can steer the review from wherever the
// requirement was written down.
func reviewTrigger(ctx context.Context, _ *Scanner, pc *prContext, _ string) taskOptions {
	var bodies []string
	if pc.pr.GetBody() != "" {
		bodies = append(bodies, pc.pr.GetBody())
	}
	// Shared with the evaluation, which fetched them for the label sync.
	for _, refIssue := range pc.refIssues.all(ctx) {
		if refIssue.GetBody() != "" {
			bodies = append(bodies, refIssue.GetBody())
		}
	}
	var latestCheckCompletedTime time.Time
	for _, run := range pc.checks.checkRuns {
		if t := run.GetCompletedAt().Time; t.After(latestCheckCompletedTime) {
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
	return taskOptions{
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRReadyForReview,
		TriggerNotes:     notes,
		Instructions:     common.ExtractReviewInstructions(bodies...),
	}
}
