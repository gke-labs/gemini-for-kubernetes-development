package issues

import (
	"context"
	"fmt"
	"sort"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// WorkflowSandboxes reports whether a workflow already has a sandbox, which is
// what tells the Nudger that a linked issue's workflow is under way.
type WorkflowSandboxes interface {
	Exists(ctx context.Context, name string) (bool, error)
}

// NudgerDeps holds the collaborators of a Nudger.
type NudgerDeps struct {
	// GitHub is the repository-bound client used for every query.
	GitHub *github.Client
	// Queue receives the workflow tasks the Nudger queues.
	Queue Queue
	// Sandboxes reports which workflows have a sandbox.
	Sandboxes WorkflowSandboxes
	// Users resolves the bot account a queued task runs as.
	Users UserSelector
}

// Nudger wakes the workflows that are waiting on an issue which has just
// closed.
//
// A workflow issue usually tracks work it handed out as other issues: it files
// a step as its own issue, lists it in a progress comment, and checks on it
// each time it runs. Between runs it sits out a cooldown, which a workflow can
// set to hours, so without a nudge the step closing waits on that cooldown to
// be noticed. The Nudger queues the workflow's next run as soon as the step's
// sandbox is collected instead.
//
// It holds no state of its own, unlike the Scanner, so it is safe to call from
// another goroutine - in practice the sandbox reconciler's - while the Scanner
// runs.
type Nudger struct {
	cfg       Config
	gh        *github.Client
	queue     Queue
	sandboxes WorkflowSandboxes
	users     UserSelector
}

// NewNudger constructs a Nudger. It honours the TriggerLabel, TargetAssignee,
// MinNumber and DryRun fields of cfg, the same configuration the Scanner runs
// with.
func NewNudger(cfg Config, deps NudgerDeps) *Nudger {
	return &Nudger{
		cfg:       cfg,
		gh:        deps.GitHub,
		queue:     deps.Queue,
		sandboxes: deps.Sandboxes,
		users:     deps.Users,
	}
}

// NudgeLinkedWorkflows queues the next run of every workflow linked to the
// given closed issue, skipping the workflow's cooldown.
//
// A workflow is linked when its issue mentions the closed one - which is how a
// workflow's progress comment shows up, as a cross-reference on the closed
// issue's timeline - or when the closed issue mentions it. Only issues that
// are still open, still ask for a workflow, and already have a workflow
// sandbox are nudged; nothing else is waiting.
//
// A run already queued or in progress is left as it is: the queue dedupes on
// the task file name, which is the one the Scanner uses. That means a closure
// landing while the workflow is mid-run is picked up by its next run after
// the cooldown, rather than straight away.
func (n *Nudger) NudgeLinkedWorkflows(ctx context.Context, closed *githubv39.Issue) {
	if !n.gh.Ready() || closed == nil {
		return
	}
	for _, num := range n.linkedIssues(ctx, closed) {
		if ctx.Err() != nil {
			return
		}
		n.nudge(ctx, num, closed)
	}
}

// linkedIssues returns the numbers of the issues and pull requests that either
// mention the closed issue or are mentioned by it, in ascending order.
func (n *Nudger) linkedIssues(ctx context.Context, closed *githubv39.Issue) []int {
	closedNum := closed.GetNumber()
	linked := make(map[int]bool)

	// A truncated timeline still names who it does: unlike the linked-PR
	// check, this is not proving that a reference does not exist.
	timeline, _, err := n.gh.ListIssueTimeline(ctx, closedNum)
	if err != nil {
		klog.Warningf("Failed to list timeline of closed issue #%d for linked workflows: %v", closedNum, err)
	}
	for _, num := range crossReferencingIssues(timeline) {
		linked[num] = true
	}
	for _, num := range common.ExtractRelatedIssuesAndPRs(closed.GetBody(), nil, closedNum) {
		linked[num] = true
	}
	delete(linked, closedNum)

	nums := make([]int, 0, len(linked))
	for num := range linked {
		nums = append(nums, num)
	}
	sort.Ints(nums)
	return nums
}

// crossReferencingIssues returns the issues, not pull requests, that the
// timeline records as having mentioned it.
func crossReferencingIssues(timeline []*githubv39.Timeline) []int {
	var nums []int
	for _, event := range timeline {
		if event.GetEvent() != "cross-referenced" || event.Source == nil || event.Source.Issue == nil {
			continue
		}
		// A pull request mentioning the issue is the change that closed it or
		// one that discusses it, never a workflow waiting on it.
		if event.Source.Issue.PullRequestLinks != nil {
			continue
		}
		if num := event.Source.Issue.GetNumber(); num > 0 {
			nums = append(nums, num)
		}
	}
	return nums
}

// nudge queues the workflow run for one linked issue, if it has a workflow
// that is under way.
func (n *Nudger) nudge(ctx context.Context, num int, closed *githubv39.Issue) {
	if n.cfg.MinNumber > 0 && num < n.cfg.MinNumber {
		return
	}

	// The sandbox is checked first: it costs nothing against the GitHub rate
	// limit, and most of what an issue mentions has no workflow at all.
	sandboxName := fmt.Sprintf("wf-issue-%d", num)
	exists, err := n.sandboxes.Exists(ctx, sandboxName)
	if err != nil {
		klog.Warningf("Failed to check for workflow sandbox %s: %v", sandboxName, err)
		return
	}
	if !exists {
		return
	}

	issue, err := n.gh.GetIssue(ctx, num)
	if err != nil {
		klog.Warningf("Failed to fetch issue #%d linked to closed issue #%d: %v", num, closed.GetNumber(), err)
		return
	}
	if issue.IsPullRequest() || issue.GetState() != "open" {
		return
	}
	if conventions.HasStopLabel(issue.Labels, n.cfg.TriggerLabel) {
		klog.Infof("Not nudging the workflow of issue #%d because it has the stop label.", num)
		return
	}
	workflowPath, workflowName := resolveWorkflow(ctx, n.gh, issue)
	if workflowName == "" {
		return
	}

	filename := issueTaskFilename(num, workflowName)
	if n.queue.TaskExists(filename) {
		klog.Infof("Workflow %s of issue #%d is already queued or running; not nudging it for closed issue #%d.", workflowName, num, closed.GetNumber())
		return
	}

	task := newIssueTask(n.gh, taskOptions{
		Type:             api.TypeAgentChore,
		Issue:            issue,
		Phase:            api.PhaseChores,
		Assignee:         n.selectUser(ctx, num),
		TriggerEventTime: closed.GetClosedAt(),
		TriggerReason:    api.TriggerReasonLinkedIssueClosed,
		TriggerNotes:     fmt.Sprintf("Linked issue #%d was closed", closed.GetNumber()),
		AgentFile:        workflowPath,
		SessionID:        fmt.Sprintf("issue-%d", num),
	})

	if n.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue workflow task %s for issue #%d because linked issue #%d closed: %s\n", workflowName, num, closed.GetNumber(), task.URL)
		return
	}
	fmt.Printf("Queueing workflow task %s for issue #%d because linked issue #%d closed...\n", workflowName, num, closed.GetNumber())
	if err := n.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue workflow task for issue #%d: %v", num, err)
	}
}

// selectUser resolves the bot account a workflow task runs as, falling back
// to the configured assignee the way the Scanner does.
func (n *Nudger) selectUser(ctx context.Context, num int) string {
	if n.users == nil {
		return n.cfg.TargetAssignee
	}
	assignee, err := n.users.SelectUser(ctx, api.TypeAgentChore, num)
	if err != nil {
		klog.Errorf("Failed to select user for issue #%d: %v", num, err)
		return n.cfg.TargetAssignee
	}
	if assignee == "" {
		return n.cfg.TargetAssignee
	}
	return assignee
}
