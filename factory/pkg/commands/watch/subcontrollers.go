package watch

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/chores"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// newDispatcher constructs the task dispatcher used by the watcher, wiring it to
// the watcher's queue manager, sandbox leases, cluster clients and GitHub client.
// The runner is passed in so tests can substitute the child process execution.
func (w *Watcher) newDispatcher(runner dispatcher.TaskRunner) *dispatcher.Dispatcher {
	return dispatcher.New(dispatcher.Config{
		Interval:    dispatcher.DefaultInterval,
		MaxActions:  w.MaxActions,
		MaxPending:  w.MaxPending,
		TaskTimeout: w.TaskTimeout,
		DryRun:      w.DryRun,
	}, dispatcher.Deps{
		Queue:        w.queueMgr,
		SandboxLocks: w.sandboxLocks,
		Sandboxes:    w.sandboxes,
		Coordinator:  &watcherTaskCoordinator{w: w},
		Runner:       runner,
	})
}

// newReconciler constructs the sandbox reconciler, which runs as its own
// goroutine and keeps cluster sandbox state in sync without blocking scanning
// or dispatching.
func (w *Watcher) newReconciler() *sandbox.Reconciler {
	return sandbox.New(sandbox.Config{
		Interval:    sandbox.DefaultInterval,
		GCInterval:  sandbox.DefaultGCInterval,
		EvictionAge: w.SandboxEvictionAge,
		IdleTimeout: w.SandboxIdleTimeout,
		DryRun:      w.DryRun,
	}, sandbox.Deps{
		Sandboxes: w.sandboxes,
		Locks:     w.sandboxLocks,
		Entities:  w.entityCache,
		Paused:    w.draining,
	})
}

// newChoreScheduler constructs the chore scheduler, which runs as its own
// goroutine so that a scheduled agent is queued within one evaluation interval
// of coming due, instead of waiting for the slow PR scan that used to carry it.
func (w *Watcher) newChoreScheduler() *chores.Scheduler {
	return chores.New(chores.Config{
		Interval:        chores.DefaultInterval,
		RefreshInterval: chores.DefaultRefreshInterval,
		StateDir:        w.QueueDir,
		Owner:           w.Repo.Owner,
		Repo:            w.Repo.Repo,
		DryRun:          w.DryRun,
	}, chores.Deps{
		Queue:  w.queueMgr,
		Source: w.repoClient,
		Paused: w.draining,
	})
}

// newIssueScanner constructs the issue scanner, which runs as its own goroutine
// so that a newly filed or newly assigned issue is queued within one interval,
// instead of waiting behind the pull request evaluation that used to share its
// cycle.
func (w *Watcher) newIssueScanner() *issues.Scanner {
	return issues.New(issues.Config{
		Interval:       issues.DefaultInterval,
		SweepInterval:  issues.DefaultSweepInterval,
		TriggerLabel:   w.triggerLabel,
		TargetAssignee: w.targetAssignee,
		GitHubLogin:    w.githubLogin,
		BotUsers:       w.allBotUsers,
		ScanLimit:      w.ScanLimit,
		MinNumber:      w.minIssueNumber(),
		PrimeOpenPRs:   !w.prsEnabled(),
		DryRun:         w.DryRun,
	}, issues.Deps{
		GitHub:    w.repoClient,
		Queue:     w.queueMgr,
		Entities:  w.entityCache,
		Sandboxes: w.sandboxes,
		Users:     watcherUserSelector{w: w},
		Paused:    w.draining,
	})
}

// newPRScanner constructs the pull request scanner, which runs as its own
// goroutine. It was the slowest thing in the watcher - a dozen GitHub requests
// per pull request - and everything else used to wait behind it; now nothing does.
func (w *Watcher) newPRScanner() *prs.Scanner {
	return prs.New(prs.Config{
		Interval:          prs.DefaultInterval,
		SweepInterval:     prs.DefaultSweepInterval,
		Workers:           prs.DefaultWorkers,
		TriggerLabel:      w.triggerLabel,
		GitHubLogin:       w.githubLogin,
		BotUsers:          w.allBotUsers,
		ReviewerLogins:    w.reviewerLogins(),
		AllowlistedBots:   w.allowlistedBots(),
		ScanLimit:         w.ScanLimit,
		MinNumber:         w.minIssueNumber(),
		InactivityTimeout: w.PRInactivityTimeout,
		DryRun:            w.DryRun,
	}, prs.Deps{
		GitHub:    w.repoClient,
		Queue:     w.queueMgr,
		Entities:  w.entityCache,
		Sandboxes: w.sandboxes,
		Paused:    w.draining,
	})
}

// minIssueNumber is the issue number below which the repository's history is
// ignored, as configured. Zero scans everything.
func (w *Watcher) minIssueNumber() int {
	if w.cfg == nil {
		return 0
	}
	return w.cfg.MinNumber
}

// reviewerLogins are the accounts configured in the reviewer role, whose
// comments count as review feedback to act on. Resolving the role here is what
// keeps the scanner package free of the factory config shape.
func (w *Watcher) reviewerLogins() []string {
	if w.cfg == nil {
		return nil
	}
	return w.cfg.Roles["reviewer"].Users
}

// allowlistedBots are the automated accounts whose comments are acted on rather
// than ignored as machine noise.
func (w *Watcher) allowlistedBots() []string {
	if w.cfg == nil {
		return nil
	}
	return w.cfg.AllowlistedBots
}

// draining reports whether the queue is in drain mode, in which case the
// subcontrollers that create work hold off: the sandbox reconciler stops
// reclaiming sandboxes, and the chore scheduler and the issue and pull request
// scanners stop queueing work.
//
// This is their only view of queue state besides the lease registry, and it is
// deliberately a one-way read: they can observe that the queue is draining but
// have no way to alter it.
//
// Shutdown is deliberately not reported here. All of them run under the daemon
// context, so cancelling it already stops them, and it stops a cycle that is
// already in flight - which this signal, read once at the top of a cycle,
// cannot. Drain cannot be expressed that way in turn: it is a marker file that
// an operator removes to resume, and a cancelled context never comes back.
func (w *Watcher) draining() bool {
	return w.queueMgr != nil && w.queueMgr.IsDrainMode()
}

// newCLIRunner builds the runner that executes tasks as child factory CLI processes.
func (w *Watcher) newCLIRunner() *dispatcher.CLIRunner {
	return dispatcher.NewCLIRunner(dispatcher.CLIRunnerConfig{
		Namespace:        w.Namespace,
		Image:            w.Image,
		DiskSize:         w.DiskSize,
		EphemeralStorage: w.EphemeralStorage,
		CPURequest:       w.CPURequest,
		CPULimit:         w.CPULimit,
		MemoryRequest:    w.MemoryRequest,
		MemoryLimit:      w.MemoryLimit,
		TaskTimeout:      w.TaskTimeout,
		ProcessingLogDir: w.processingLogDir,
	})
}

// The watcher's sandbox service satisfies the dispatcher's interface directly,
// so no adapter is needed.
var _ dispatcher.SandboxService = (*sandbox.Service)(nil)

// The watcher's queue manager satisfies the chore scheduler's interface directly.
var _ chores.Queue = (*concurrency.TaskQueueManager)(nil)

// A repository-bound GitHub client is the chore scheduler's definition source,
// so no adapter is needed on this side either.
var _ chores.Source = (*github.Client)(nil)

// The shared primitives satisfy the scanners' collaborators directly. Each
// scanner declares the narrow subset it uses; these assertions are what keep
// those subsets honest as the primitives evolve.
var (
	_ issues.Queue     = (*concurrency.TaskQueueManager)(nil)
	_ issues.Entities  = (*concurrency.EntityStateCache)(nil)
	_ issues.Sandboxes = (*sandbox.Service)(nil)

	_ prs.Queue     = (*concurrency.TaskQueueManager)(nil)
	_ prs.Entities  = (*concurrency.EntityStateCache)(nil)
	_ prs.Sandboxes = (*sandbox.Service)(nil)
)

// watcherUserSelector adapts the watcher's role-based bot selection to the
// issue scanner's UserSelector. Selection reads the factory config and can pin
// a task to the account an existing sandbox already belongs to, both of which
// are the watcher's to own.
type watcherUserSelector struct {
	w *Watcher
}

var _ issues.UserSelector = watcherUserSelector{}

// SelectUser returns the bot account a task for the given entity should run as.
func (s watcherUserSelector) SelectUser(ctx context.Context, taskType api.TaskType, number int) (string, error) {
	return s.w.selectUserForTask(ctx, taskType, number)
}

// watcherTaskCoordinator adapts the watcher's GitHub interactions to the TaskCoordinator interface.
type watcherTaskCoordinator struct {
	w *Watcher
}

var _ dispatcher.TaskCoordinator = (*watcherTaskCoordinator)(nil)

// ShouldCancelTask reports whether the target issue or pull request carries a
// stop label or has been closed since the task was queued.
func (c *watcherTaskCoordinator) ShouldCancelTask(ctx context.Context, task *api.QueueTask) (bool, string) {
	w := c.w
	if task.Number <= 0 || w.DryRun || w.ghClient == nil {
		return false, ""
	}

	issueOrPR, err := w.repoClient.GetIssue(ctx, task.Number)
	if err != nil || issueOrPR == nil {
		return false, ""
	}
	if conventions.HasStopLabel(issueOrPR.Labels, w.triggerLabel) {
		return true, fmt.Sprintf("target #%d has the stop label ('overseer/stop' or '%s/stop')", task.Number, w.triggerLabel)
	}
	if issueOrPR.GetState() == "closed" {
		return true, fmt.Sprintf("target #%d is closed", task.Number)
	}
	return false, ""
}

// SelectUser returns the bot account the task should run as, preferring the
// assignee recorded on the task and otherwise falling back to role selection.
func (c *watcherTaskCoordinator) SelectUser(ctx context.Context, task *api.QueueTask) (string, error) {
	w := c.w
	selectedUser := task.Assignee
	if selectedUser == "" || (api.IsPRTask(task.Type) && strings.EqualFold(selectedUser, w.targetAssignee)) {
		return w.selectUserForTask(ctx, task.Type, task.Number)
	}
	return selectedUser, nil
}

// NotifyTaskStarted claims the issue for the assignee and posts a start comment on GitHub.
func (c *watcherTaskCoordinator) NotifyTaskStarted(ctx context.Context, task *api.QueueTask) {
	w := c.w
	if task.Number <= 0 || w.ghClient == nil {
		return
	}

	if (task.Type == api.TypeIssueFix || task.Type == api.TypeAgentChore) && task.Assignee != "" {
		klog.Infof("Assigning issue #%d to %s as claimed", task.Number, task.Assignee)
		if err := w.repoClient.AddAssignees(ctx, task.Number, []string{task.Assignee}); err != nil {
			klog.Errorf("Failed to assign issue #%d to %s: %v", task.Number, task.Assignee, err)
		}
		if task.Assignee != w.targetAssignee {
			if err := w.repoClient.RemoveAssignees(ctx, task.Number, []string{w.targetAssignee}); err != nil {
				klog.Errorf("Failed to remove watcher bot %s from issue #%d: %v", w.targetAssignee, task.Number, err)
			}
		}
	}

	if commentBody := taskStartedComment(task.Type); commentBody != "" {
		if err := w.repoClient.AddComment(ctx, task.Number, commentBody); err != nil {
			klog.Errorf("Failed to create GitHub comment on #%d: %v", task.Number, err)
		}
	}
}

// NotifyTaskFinished resolves the acknowledgement reactions on PR review comments.
//
// A failure with attempts still owed is deliberately silent. The 'confused'
// mark means the watcher has given up on a comment, and reactions here can only
// be added - there is no removal call - so stamping one between attempts would
// both mislead whoever reads the thread and be impossible to take back. The
// final failure is the one that gets marked, and the one that says so out loud.
func (c *watcherTaskCoordinator) NotifyTaskFinished(ctx context.Context, task *api.QueueTask, taskErr error) {
	w := c.w
	if task.Type != api.TypePRComments || w.cfg == nil {
		return
	}

	if taskErr == nil {
		conventions.ResolveCommentReactions(ctx, w.repoClient, task.Number, conventions.ReactionResolved, w.cfg.AllowlistedBots, w.githubLogin)
		return
	}

	// A task queued before attempts were counted carries no number, so it keeps
	// the single-attempt treatment it was queued under.
	if task.Attempt > 0 && task.Attempt < api.MaxPRCommentAttempts {
		klog.Infof("Address-comments task for PR #%d failed on attempt %d of %d; leaving the feedback unmarked for the next attempt.", task.Number, task.Attempt, api.MaxPRCommentAttempts)
		return
	}

	if task.Attempt >= api.MaxPRCommentAttempts {
		if err := w.repoClient.AddComment(ctx, task.Number, giveUpOnFeedbackComment(task)); err != nil {
			klog.Errorf("Failed to comment on PR #%d after exhausting address-comments attempts: %v", task.Number, err)
		}
	}

	conventions.ResolveCommentReactions(ctx, w.repoClient, task.Number, conventions.ReactionFailed, w.cfg.AllowlistedBots, w.githubLogin)
}

// giveUpOnFeedbackComment is what the watcher says when it stops retrying a
// pull request's review feedback.
//
// No stop label goes with it, unlike the investigation circuit breaker: failing
// to address feedback is a statement about this feedback on this revision, not
// about the pull request, and pausing the rebase and CI automation too would
// strand a change over a comment nobody has to act on.
func giveUpOnFeedbackComment(task *api.QueueTask) string {
	revision := task.CommitSHA
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if revision != "" {
		revision = fmt.Sprintf(" for commit %s", revision)
	}
	return fmt.Sprintf("🤖 AI Factory attempted to address this review feedback %d times without success, and is pausing automated feedback handling%s.\n\nTo ask for another attempt: push a new commit, leave a new comment, or react with 🚀 on the comment you want revisited.", api.MaxPRCommentAttempts, revision)
}

// taskStartedComment returns the GitHub comment announcing that a task has started,
// or an empty string if the task type does not warrant a comment.
//
// The body is tagged as an announcement. Saying that work has begun is not a
// reply to anything said before it, and the PR scanner needs to be able to tell
// the difference: a comment from the watcher's own account otherwise reads as
// an answer to the feedback above it and buries it.
func taskStartedComment(taskType api.TaskType) string {
	var body string
	switch taskType {
	case api.TypeIssueFix:
		body = "🤖 AI Factory started fixing this issue in a sandbox."
	case api.TypePRInvestigate:
		body = "🤖 AI Factory started investigating CI check failures for this pull request.\n\nNote: We recommend waiting for the 'ready-for-human' label before leaving review comments. Comments added while the system is actively working may be associated with outdated commits once a new commit is pushed, causing them to be ignored."
	case api.TypePRComments:
		body = "🤖 AI Factory started addressing review feedback for this pull request."
	case api.TypePRIterate:
		body = "🤖 AI Factory started resolving merge conflicts / rebasing this pull request in a sandbox.\n\nNote: We recommend waiting for the 'ready-for-human' label before leaving review comments. Comments added while the system is actively working may be associated with outdated commits once a new commit is pushed, causing them to be ignored."
	case api.TypePRReview:
		body = "🤖 AI Factory started reviewing this pull request in a sandbox."
	default:
		return ""
	}
	return conventions.Announce(body)
}
