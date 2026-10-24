package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/chores"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/fanouts"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/warmworkspace"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
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
		Sandboxes:  w.sandboxes,
		Locks:      w.sandboxLocks,
		Entities:   w.entityCache,
		Paused:     w.draining,
		LinkedWork: w.newLinkedWork(),
	})
}

// newLinkedWork constructs what the reconciler tells about the issues it finds
// closed: the nudger, which queues the workflows waiting on them, and the
// fan-out controller, which wakes the parent of a closed child. Both start
// issue work, the issue scanner's business, so a watcher that does not scan
// issues gets nil and nudges nothing.
func (w *Watcher) newLinkedWork() sandbox.LinkedWork {
	if !w.issuesEnabled() {
		return nil
	}
	nudger := issues.NewNudger(w.issueScannerConfig(), issues.NudgerDeps{
		GitHub:    w.repoClient,
		Queue:     w.queueMgr,
		Sandboxes: w.sandboxes,
		Users:     watcherUserSelector{w: w},
	})
	return linkedWorks{nudger, w.fanouts}
}

// linkedWorks tells each of its members about every closed issue, in order.
type linkedWorks []sandbox.LinkedWork

func (l linkedWorks) NudgeLinkedWorkflows(ctx context.Context, closed *githubv39.Issue) {
	for _, lw := range l {
		lw.NudgeLinkedWorkflows(ctx, closed)
	}
}

// newFanoutController constructs the fan-out controller, which runs as its
// own goroutine and keeps every open fan-out parent moving. It is built before
// the reconciler, which wakes it when a child's sandbox is collected.
func (w *Watcher) newFanoutController() *fanouts.Controller {
	return fanouts.New(fanouts.Config{
		Interval:     fanouts.DefaultInterval,
		TriggerLabel: w.triggerLabel,
		GitHubLogin:  w.githubLogin,
		MinNumber:    w.minIssueNumber(),
		DryRun:       w.loopDryRun(w.IssueMode),
	}, fanouts.Deps{
		GitHub:   w.repoClient,
		Paused:   w.draining,
		Propose:  w.proposeFanout,
		CanStart: w.belowMaxPending,
	})
}

// proposeFanout runs ProposeFanout on parent n, if it is set, as the
// account an issue fix of n would run as: the recipe runs in n's fix
// sandbox, which belongs to that account.
func (w *Watcher) proposeFanout(ctx context.Context, n int) error {
	if w.ProposeFanout == nil {
		return fmt.Errorf("this watcher cannot run recipes")
	}
	user, err := w.selectUserForTask(ctx, api.TypeIssueRule, "fanout", n)
	if err != nil {
		return fmt.Errorf("choosing the account for the fanout recipe on #%d: %w", n, err)
	}
	return w.ProposeFanout(ctx, fmt.Sprintf("https://github.com/%s/%s/issues/%d", w.Repo.Owner, w.Repo.Repo, n), user)
}

// newWarmWorkspaceController constructs the warm workspace pass, or nil
// when the config asks for no warming (or asks for it wrongly, which is
// logged: the rest of the watch runs on).
func (w *Watcher) newWarmWorkspaceController() *warmworkspace.Controller {
	if w.cfg == nil || w.cfg.WarmWorkspace == nil || w.WarmWorkspace == nil || w.kubeClient == nil {
		return nil
	}
	cfg := *w.cfg.WarmWorkspace
	interval, err := cfg.Validate()
	if err != nil {
		klog.Errorf("Not warming workspaces: %v", err)
		return nil
	}
	repoURL := fmt.Sprintf("https://github.com/%s/%s", w.Repo.Owner, w.Repo.Repo)
	return warmworkspace.New(warmworkspace.DefaultInterval, warmworkspace.Deps{
		Paused: w.draining,
		Due: func(ctx context.Context) (bool, error) {
			return factorysandbox.WarmDue(ctx, w.kubeClient, w.Namespace, w.Repo.Repo, interval, time.Now())
		},
		Warm: func(ctx context.Context) error {
			// The account a fix would clone as: the repository may be
			// private, and the watcher's own may be no coder's.
			user, err := w.selectUserForTask(ctx, api.TypeIssueRule, "warm", 0)
			if err != nil {
				return fmt.Errorf("choosing the account for the warm recipe: %w", err)
			}
			return w.WarmWorkspace(ctx, repoURL, user, cfg)
		},
	})
}

// belowMaxPending reports whether fewer sandbox tasks run than --max-pending.
func (w *Watcher) belowMaxPending(ctx context.Context) bool {
	if w.sandboxes == nil || w.MaxPending <= 0 {
		return true
	}
	running, err := w.sandboxes.CountRunningTasks(ctx)
	if err != nil {
		klog.Errorf("Counting running sandbox tasks: %v", err)
		return false
	}
	return running < w.MaxPending
}

// newChoreScheduler constructs the chore scheduler, which runs as its own
// goroutine so that a scheduled agent is queued within one evaluation interval
// of coming due, instead of waiting for the slow PR scan that used to carry it.
func (w *Watcher) newChoreScheduler() *chores.Scheduler {
	return chores.New(w.choresConfig(), chores.Deps{
		Queue:  w.queueMgr,
		Source: w.repoClient,
		Paused: w.draining,
	})
}

// choresConfig is the chore scheduler's configuration.
func (w *Watcher) choresConfig() chores.Config {
	return chores.Config{
		Interval:        chores.DefaultInterval,
		RefreshInterval: chores.DefaultRefreshInterval,
		StateDir:        w.QueueDir,
		Owner:           w.Repo.Owner,
		Repo:            w.Repo.Repo,
		DryRun:          w.loopDryRun(w.ChoresMode),
	}
}

// loopDryRun reports whether a loop running in mode only reports what it
// would do: the watcher's --dryrun, or the loop's own "dryrun" mode.
func (w *Watcher) loopDryRun(mode string) bool {
	return w.DryRun || mode == "dryrun"
}

// newIssueScanner constructs the issue scanner, which runs as its own goroutine
// so that a newly filed or newly assigned issue is queued within one interval,
// instead of waiting behind the pull request evaluation that used to share its
// cycle.
func (w *Watcher) newIssueScanner() *issues.Scanner {
	return issues.New(w.issueScannerConfig(), issues.Deps{
		GitHub:    w.repoClient,
		Queue:     w.queueMgr,
		Entities:  w.entityCache,
		Sandboxes: w.sandboxes,
		Users:     watcherUserSelector{w: w},
		Listings:  w.issueListings(),
		Paused:    w.draining,
		Recorder:  w.rulesRecorder,
		Rules:     w.rulesControl,
	})
}

// newSharedScan constructs the listings both scanners read when SharedScan
// is on, persisted at scanStatePath; nil when it is off.
func (w *Watcher) newSharedScan() *scan.Store {
	if !w.SharedScan {
		return nil
	}
	return scan.New(scan.Config{
		Repo:         w.Repo.Owner + "/" + w.Repo.Repo,
		TriggerLabel: w.triggerLabel,
		BotUsers:     w.allBotUsers,
		GitHubLogin:  w.githubLogin,
		Labels:       w.prListingLabels,
		StatePath:    w.scanStatePath(),
	}, w.repoClient)
}

// ruleListingLabels are the labels the PR listings add for the rules on
// pull requests that the pool and trigger listings do not reach - their
// overseer-namespaced labels (rules.Set.ListingLabels) - logged, with a
// warning for a rule no listing reaches at all.
func (w *Watcher) ruleListingLabels() []string {
	if w.rulesControl == nil {
		return nil
	}
	set := w.rulesControl.Set()
	env := rules.Env{Trigger: w.triggerLabel, Prefixes: conventions.Prefixes(w.triggerLabel)}
	for _, name := range set.Unlisted(rules.OnPR, env) {
		klog.Warningf("Watch rules: the rule %s names nothing a listing narrows on (author: pool, assignee: pool, the trigger label, or a label of the overseer's own, %s/...): it sees only pull requests listed for other rules", name, strings.Join(env.Prefixes, "/..., "))
	}
	labels := set.ListingLabels(rules.OnPR, env)
	if len(labels) > 0 {
		klog.Infof("Watch rules: pull requests are also listed by %s", strings.Join(labels, ", "))
	}
	return labels
}

// scanStatePath is where the shared scan's snapshot persists: ScanState,
// else scan_state.json in the queue directory. A snapshot left in the
// queue directory from before ScanState moved it is moved along (or
// dropped, when the new place already has one), so the queue directory,
// which run.sh pushes to the repository, stops carrying it.
func (w *Watcher) scanStatePath() string {
	inQueue := filepath.Join(w.QueueDir, "scan_state.json")
	if w.ScanState == "" || filepath.Clean(w.ScanState) == filepath.Clean(inQueue) {
		return inQueue
	}
	if _, err := os.Stat(inQueue); err == nil {
		if _, err := os.Stat(w.ScanState); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(w.ScanState), 0o755); err == nil {
				if err := os.Rename(inQueue, w.ScanState); err != nil {
					klog.Warningf("Moving the shared scan's snapshot to %s: %v", w.ScanState, err)
				}
			}
		}
		if err := os.Remove(inQueue); err != nil && !errors.Is(err, os.ErrNotExist) {
			klog.Warningf("Removing the shared scan's old snapshot %s: %v", inQueue, err)
		}
	}
	return w.ScanState
}

// issueListings and prListings are the shared scan as each scanner's
// Listings, or nil (not a nil *scan.Store in an interface) when it is off.
func (w *Watcher) issueListings() issues.Listings {
	if w.sharedScan == nil {
		return nil
	}
	return w.sharedScan
}

func (w *Watcher) prListings() prs.Listings {
	if w.sharedScan == nil {
		return nil
	}
	return w.sharedScan
}

// issueScannerConfig is the issue scanner's configuration, which the nudger
// that queues workflow runs on its behalf shares.
func (w *Watcher) issueScannerConfig() issues.Config {
	return issues.Config{
		Interval:        issues.DefaultInterval,
		SweepInterval:   issues.DefaultSweepInterval,
		TriggerLabel:    w.triggerLabel,
		TargetAssignee:  w.targetAssignee,
		GitHubLogin:     w.githubLogin,
		BotUsers:        w.allBotUsers,
		AllowlistedBots: w.allowlistedBots(),
		TrustedLogins:   w.cfg.TrustedLogins(),
		ScanLimit:       w.ScanLimit,
		MinNumber:       w.minIssueNumber(),
		PrimeOpenPRs:    !w.prsEnabled(),
		DryRun:          w.loopDryRun(w.IssueMode),
	}
}

// newPRScanner constructs the pull request scanner, which runs as its own
// goroutine. It was the slowest thing in the watcher - a dozen GitHub requests
// per pull request - and everything else used to wait behind it; now nothing does.
func (w *Watcher) newPRScanner() *prs.Scanner {
	return prs.New(w.prScannerConfig(), prs.Deps{
		GitHub:    w.repoClient,
		Queue:     w.queueMgr,
		Entities:  w.entityCache,
		Sandboxes: w.sandboxes,
		Listings:  w.prListings(),
		Paused:    w.draining,
		Recorder:  w.rulesRecorder,
		Rules:     w.rulesControl,
	})
}

// prScannerConfig is the pull request scanner's configuration.
func (w *Watcher) prScannerConfig() prs.Config {
	return prs.Config{
		Interval:          prs.DefaultInterval,
		SweepInterval:     prs.DefaultSweepInterval,
		ListingLabels:     w.prListingLabels,
		TriggerLabel:      w.triggerLabel,
		GitHubLogin:       w.githubLogin,
		BotUsers:          w.allBotUsers,
		ReviewerLogins:    w.reviewerLogins(),
		AllowlistedBots:   w.allowlistedBots(),
		TrustedLogins:     w.cfg.TrustedLogins(),
		ScanLimit:         w.ScanLimit,
		MinNumber:         w.minIssueNumber(),
		InactivityTimeout: w.PRInactivityTimeout,
		DryRun:            w.loopDryRun(w.PRMode),
		GraphQLPRDetails:  w.GraphQLPRDetails,
	}
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
		StorageClass:     w.StorageClass,
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
func (s watcherUserSelector) SelectUser(ctx context.Context, taskType api.TaskType, recipeName string, number int) (string, error) {
	return s.w.selectUserForTask(ctx, taskType, recipeName, number)
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

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// SelectUser returns the bot account the task should run as, preferring the
// assignee recorded on the task and otherwise falling back to role selection.
func (c *watcherTaskCoordinator) SelectUser(ctx context.Context, task *api.QueueTask) (string, error) {
	w := c.w
	selectedUser := task.Assignee
	if selectedUser == "" || (api.IsPRTask(task.Type) && strings.EqualFold(selectedUser, w.targetAssignee)) || !w.inPool(w.recipeAs(task.Recipe), selectedUser) {
		return w.selectUserForTask(ctx, task.Type, task.Recipe, task.Number)
	}
	return selectedUser, nil
}

// inPool reports whether user may run a task that runs as as: any user
// for author (the PR's own bot) or a task no rule decided, else a member
// of that pool - a review the PR's author was put on runs as a reviewer.
func (w *Watcher) inPool(as, user string) bool {
	if as == "" || as == recipe.AsAuthor || w.cfg == nil {
		return true
	}
	role, ok := w.cfg.Roles[as]
	if !ok || len(role.Users) == 0 {
		// No such pool: selection falls back as it would (agent to coder).
		return true
	}
	for _, u := range role.Users {
		if strings.EqualFold(u, user) {
			return true
		}
	}
	return false
}

// endWrites makes the writes the task's rule declares for its end: on
// success when it completed, on failure when it failed, then on end
// either way.
func (c *watcherTaskCoordinator) endWrites(ctx context.Context, task *api.QueueTask, taskErr error) {
	outcome, hook := "on-success", task.OnSuccess
	if taskErr != nil {
		outcome, hook = "on-failure", task.OnFailure
	}
	c.hookWrites(ctx, task, outcome, hook)
	c.hookWrites(ctx, task, "on-end", task.OnEnd)
}

// hookWrites makes a lifecycle hook's writes on the task's item - as the
// watcher, which may label and assign where the task's bot may only
// comment - {{bot}} being the bot the task runs as. A stop withdraws the
// item's pending tasks, as a rule's does.
func (c *watcherTaskCoordinator) hookWrites(ctx context.Context, task *api.QueueTask, name string, tw *api.TaskWrites) {
	w := c.w
	if tw == nil || task.Number <= 0 || w.repoClient == nil {
		return
	}
	writes := act.HookWrites(tw)
	bot := func(logins []string) []string {
		var out []string
		for _, l := range logins {
			if l == rules.Bot {
				l = task.Assignee
			}
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	writes.Assign, writes.Unassign = bot(writes.Assign), bot(writes.Unassign)
	mode := w.IssueMode
	if api.IsPRTask(task.Type) {
		mode = w.PRMode
	}
	dry := w.loopDryRun(mode)
	done := act.ApplyWrites(ctx, w.repoClient, task.Number, writes, w.triggerLabel, dry)
	klog.Infof("#%d: %s's task %s: %+v (%+v)", task.Number, task.Rule, name, writes, done)
	if writes.Stop && !dry && w.queueMgr != nil {
		if err := w.queueMgr.RemovePendingTasksForNumber(task.Number); err != nil {
			klog.Warningf("Withdrawing the pending tasks of #%d after %s's stop: %v", task.Number, name, err)
		}
	}
}

// NotifyTaskStarted does on GitHub what the task's rule declares for its
// start (act.Declare): claims the issue for its bot, acknowledges the
// feedback it answers, posts its start comment.
func (c *watcherTaskCoordinator) NotifyTaskStarted(ctx context.Context, task *api.QueueTask) {
	w := c.w
	if task.Number <= 0 || w.ghClient == nil {
		return
	}

	if task.Claim && task.Assignee != "" {
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

	// The rule's on-start hook: after the claim, so {{bot}} is assigned.
	c.hookWrites(ctx, task, "on-start", task.OnStart)

	// A comments task acknowledges the feedback it answers now that it
	// starts, not when it was queued: one dropped before it starts leaves
	// the feedback for the next scan.
	if task.Acknowledge == rules.AcknowledgeFeedback && w.cfg != nil {
		conventions.AcknowledgeCommentReactions(ctx, w.repoClient, conventions.ResolveOptions{
			PRNumber:        task.Number,
			SelfLogin:       w.githubLogin,
			AllowlistedBots: w.cfg.AllowlistedBots,
			ReviewerLogins:  w.reviewerLogins(),
			TrustedLogins:   w.cfg.TrustedLogins(),
			Since:           task.TriggerEventTime,
			TriggerLabel:    w.triggerLabel,
		})
		if w.prScanner != nil {
			w.prScanner.MarkCommentsAddressed(task.Number, task.EnqueuedAt)
		}
	}

	if task.StartComment != "" {
		if err := w.repoClient.AddComment(ctx, task.Number, task.StartComment); err != nil {
			klog.Errorf("Failed to create GitHub comment on #%d: %v", task.Number, err)
		}
	}
}

// NotifyTaskFinished resolves the acknowledgement reactions on PR feedback -
// conversation comments, review bodies, and inline review comments - of a
// task whose rule acknowledges feedback.
func (c *watcherTaskCoordinator) NotifyTaskFinished(ctx context.Context, task *api.QueueTask, taskErr error) {
	w := c.w
	c.endWrites(ctx, task, taskErr)
	if task.Acknowledge != rules.AcknowledgeFeedback || w.cfg == nil {
		return
	}
	resolution := conventions.ReactionResolved
	if taskErr != nil {
		resolution = conventions.ReactionFailed
	}
	conventions.ResolveCommentReactions(ctx, w.repoClient, conventions.ResolveOptions{
		PRNumber:        task.Number,
		Resolution:      resolution,
		SelfLogin:       w.githubLogin,
		AllowlistedBots: w.cfg.AllowlistedBots,
		ReviewerLogins:  w.reviewerLogins(),
		TrustedLogins:   w.cfg.TrustedLogins(),
		// The scanner times an address-comments task from the oldest
		// feedback it picked up, so nothing older can be this task's.
		Since: task.TriggerEventTime,
	})
}
