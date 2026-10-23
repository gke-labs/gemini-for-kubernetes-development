// Package issues owns the issue side of the watch daemon: finding the GitHub
// issues that need an agent and queueing the task that works on them.
//
// The Scanner runs as an autonomous goroutine so that a newly created or newly
// assigned issue is queued within one interval, instead of waiting behind the
// pull request evaluation that used to share its cycle. It reaches the rest of
// the daemon through narrow collaborators - a Queue for the tasks it creates,
// an Entities cache for what other subcontrollers have observed, a Sandboxes
// probe and a UserSelector - and never calls into another subcontroller.
//
// Pull requests are deliberately out of scope. GitHub's issue endpoints return
// pull requests alongside issues, and the scanner drops them: the pull request
// scanner lists them itself, on its own cadence.
package issues

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

const (
	// DefaultInterval is how often the issues assigned to the bot pool or
	// created by the operator are scanned. It is what sets pickup latency for a
	// new issue, and the queries behind it are bounded to a single page.
	DefaultInterval = 2 * time.Minute
	// DefaultSweepInterval is how often the full trigger-labelled sweep runs.
	// That sweep paginates over every labelled issue in the repository, so it
	// stays on the slow cadence it has always had; the fast cycle above is what
	// picks up the issues a person just filed or assigned.
	DefaultSweepInterval = 10 * time.Minute
	// defaultScanLimit bounds the fast queries when no limit is configured.
	defaultScanLimit = 30
)

// Queue is the subset of the task queue the Scanner needs: it adds issue tasks,
// withdraws the pending ones for an issue that has since been stopped, and
// reports what has already finished.
//
// The finished work is read through here rather than off the filesystem
// because the queue owns the task files. A scanner that opened the processed
// directory itself would be a second reader of state it does not control, and
// would miss everything that finished after it first looked.
type Queue interface {
	// TaskExists reports whether a task with the given file name is queued or running.
	TaskExists(filename string) bool
	// HasActiveIssueTask reports whether any task for the issue is queued
	// or running: its fix or a workflow step, whichever it is.
	HasActiveIssueTask(number int) bool
	// Enqueue adds a task to the queue under the given file name.
	Enqueue(filename string, task *api.QueueTask) error
	// RemovePendingTasksForNumber drops the not-yet-started tasks targeting an issue.
	RemovePendingTasksForNumber(number int) error
	// GetProcessedTask returns the finished task recorded under the given file
	// name, or nil when nothing by that name has finished.
	GetProcessedTask(filename string) *api.QueueTask
	// ListProcessedTasks returns every finished task, keyed by task file name.
	ListProcessedTasks() map[string]*api.QueueTask
	// NoteCooldown records a task held back by its cooldown, for the queue's
	// readers, and reports whether it is news (logged once per window).
	NoteCooldown(c api.Cooldown) bool
}

// Entities is the shared view of what the scanners have observed. The Scanner
// reads the referenced-issue map to skip issues that already have a fix in
// flight, and publishes the issues it knows to be open for the sandbox
// reconciler to garbage collect against.
type Entities interface {
	// HasOpenPRs reports whether a pull request scan has published to the cache.
	HasOpenPRs() bool
	// UpdateOpenPRs replaces the cached open pull requests.
	UpdateOpenPRs(prs []*githubv39.PullRequest)
	// GetReferencedIssuesMap returns the issues referenced by an open pull request.
	GetReferencedIssuesMap() map[int]bool
	// SetOpenIssueNumbers replaces the set of issues known to be open.
	SetOpenIssueNumbers(nums []int)
}

// Sandboxes reports whether a task is already executing in a sandbox, which is
// the last check before queueing: a sandbox mid-run holds the workspace that a
// second task would fight over.
type Sandboxes interface {
	IsTaskRunning(ctx context.Context, name string) (bool, error)
	// RequiresHuman is why the sandbox needs a person before anything runs
	// in it again, or "".
	RequiresHuman(ctx context.Context, name string) (string, error)
}

// Listings is the shared scan (package scan): the listings the issue and
// pull request scanners both read, refreshed incrementally and kept across
// restarts. A Scanner given one reads its lists out of it instead of listing
// for itself; what it decides is unchanged.
type Listings interface {
	// Refresh brings the shared snapshot up to date; on error the views
	// serve the last good one.
	Refresh(ctx context.Context) error
	// Labelled is every open item carrying the trigger label.
	Labelled() []*githubv39.Issue
	// AssignedFirst is the first n open items assigned to user, newest
	// updated first.
	AssignedFirst(user string, n int) []*githubv39.Issue
	// CreatedFirst is the first n open items opened by login, newest
	// updated first.
	CreatedFirst(login string, n int) []*githubv39.Issue
}

// UserSelector picks the bot account a task should run as. Role configuration
// and sandbox pinning live with the watcher, so the Scanner asks rather than
// deciding.
type UserSelector interface {
	// SelectUser is the bot a task of taskType running recipe runs as
	// (the recipe's as).
	SelectUser(ctx context.Context, taskType api.TaskType, recipe string, number int) (string, error)
}

// Config holds the tuning knobs of a Scanner.
type Config struct {
	// Interval is the delay between fast scan cycles. Defaults to DefaultInterval.
	Interval time.Duration
	// SweepInterval is the delay between full trigger-labelled sweeps.
	// Defaults to DefaultSweepInterval.
	SweepInterval time.Duration
	// TriggerLabel is the label marking an issue as the watcher's to work on.
	TriggerLabel string
	// TargetAssignee is the bot account the watcher runs as, used as the
	// fallback assignee and as the account auto-assigned to operator issues.
	TargetAssignee string
	// GitHubLogin is the login of the operator whose new issues are adopted
	// automatically. An empty login disables that query.
	GitHubLogin string
	// BotUsers is the pool of bot accounts whose assigned issues are scanned.
	BotUsers []string
	// AllowlistedBots and TrustedLogins tell a trusted human's comment from
	// the others, as on a pull request: where a give-up's count starts.
	AllowlistedBots []string
	TrustedLogins   []string
	// ScanLimit caps the page size of the fast queries. Defaults to defaultScanLimit.
	ScanLimit int
	// MinNumber skips issues numbered below it, which is how a deployment
	// ignores a repository's history. Zero scans everything.
	MinNumber int
	// PrimeOpenPRs makes this scanner populate the open pull request half of
	// the entity cache itself, which it must do when no pull request scanner is
	// running to do it. See primeOpenPRs.
	PrimeOpenPRs bool
	// DryRun reports what would be queued without touching the queue or GitHub.
	DryRun bool
}

// Deps holds the collaborators of a Scanner.
type Deps struct {
	// GitHub is the repository-bound client used for every query and write.
	// It carries the owner and repo, which is why the Config does not.
	GitHub *github.Client
	// Queue receives the issue tasks the scan creates.
	Queue Queue
	// Entities is the shared open PR / open issue cache.
	Entities Entities
	// Sandboxes probes whether a sandbox is already running the work.
	Sandboxes Sandboxes
	// Users resolves the bot account a queued task runs as.
	Users UserSelector
	// Listings, when set, is the shared scan the scanner reads its lists
	// from; nil lists for itself, as before.
	Listings Listings
	// Recorder, when set, records each decision with the facts it was made
	// on, for the watch rules' replay (rules.Record); nil records nothing.
	Recorder *rules.Recorder
	// Rules is the operator's say over the rules: those disabled, which
	// the scanner does not act on, and those the engine decides for it.
	// nil: the scanner alone, every rule on.
	Rules *rules.Control
	// Paused reports whether scanning must be held off, which is how the
	// watcher propagates drain mode. It is deliberately a plain read-only
	// signal: the scanner can observe that the queue is draining but has no way
	// to mutate it. A nil Paused never pauses.
	//
	// Shutdown does not arrive through here. Cancelling the context passed to
	// Run is what stops the scanner, and unlike this signal it also aborts a
	// cycle that has already started.
	Paused func() bool
}

// Scanner queues the agent tasks for issues that need one.
//
// It is single-goroutine by construction: the record of when each issue was
// last worked on is a plain map owned by whoever calls ScanOnce, which is the
// Run loop in daemon mode and the caller itself in --once mode. Nothing else
// may call into it concurrently.
type Scanner struct {
	cfg       Config
	gh        *github.Client
	queue     Queue
	entities  Entities
	sandboxes Sandboxes
	users     UserSelector
	listings  Listings
	paused    func() bool
	recorder  *rules.Recorder
	rules     *rules.Control
	// rec is the record of the issue queueTask is deciding about: the facts
	// the engine decides on.
	rec *rules.Recording

	// lastSweep is when the trigger-labelled sweep last completed successfully.
	lastSweep time.Time
}

// New constructs a Scanner from its configuration and dependencies.
func New(cfg Config, deps Deps) *Scanner {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = DefaultSweepInterval
	}
	if cfg.ScanLimit <= 0 {
		cfg.ScanLimit = defaultScanLimit
	}
	// The engine decides every rule, on the facts the scan records:
	// without a control the prebaked rules, all on; without a recorder one
	// that keeps nothing.
	control := deps.Rules
	if control == nil {
		if set, err := rules.Builtin(); err == nil {
			control, _ = rules.NewControl(nil, set)
		}
	}
	recorder := deps.Recorder
	if recorder == nil {
		recorder = rules.NewSinkRecorder(func(rules.Record) {})
	}
	return &Scanner{
		cfg:       cfg,
		gh:        deps.GitHub,
		queue:     deps.Queue,
		entities:  deps.Entities,
		sandboxes: deps.Sandboxes,
		users:     deps.Users,
		listings:  deps.Listings,
		paused:    deps.Paused,
		recorder:  recorder,
		rules:     control,
	}
}

// Run scans for issues until ctx is cancelled, returning nil once it has stopped.
//
// A cycle runs immediately so that a restart picks up the issues filed while
// the daemon was down, instead of waiting out a full interval.
func (s *Scanner) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	s.ScanOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			// Cancellation is how this subcontroller is asked to stop, so it is
			// not an error worth propagating to the caller.
			return nil
		case <-ticker.C:
			s.ScanOnce(ctx)
		}
	}
}

// ScanOnce runs one scan cycle, queueing work for the issues that need it. The
// trigger-labelled sweep runs inside it whenever it has come due.
func (s *Scanner) ScanOnce(ctx context.Context) {
	if s.paused != nil && s.paused() {
		klog.V(2).Infof("Skipping issue scan because the watcher is draining.")
		return
	}

	s.primeOpenPRs(ctx)
	if !s.entities.HasOpenPRs() {
		// The open PR cache is the primary duplicate-suppression signal for
		// issue scans. An empty cache is indistinguishable from "no open PR
		// references this issue", so scanning without one re-triggers fixes for
		// issues that already have an open PR. This happens in practice when
		// the process restarts into a GitHub rate limit window and every
		// attempt to list open PRs fails. Fail closed and wait for a successful
		// scan instead.
		klog.Warningf("Skipping issue scan: the open PR cache is not populated, so issues with an open fix PR cannot be identified.")
		return
	}
	refIssues := s.entities.GetReferencedIssuesMap()

	if s.sweepDue() {
		klog.Infof("Running trigger-labelled issue sweep...")
		labelled, err := s.scanLabelled(ctx)
		if err != nil {
			klog.Errorf("Failed to list issues for label %s: %v", s.cfg.TriggerLabel, err)
		}
		s.queueTasks(ctx, labelled, refIssues)

		// Publish the issues this sweep observed so the sandbox reconciler can
		// garbage collect the closed ones without re-querying GitHub. A failed
		// sweep returns whatever it managed to page in, which would publish a
		// truncated set as though it were the whole picture.
		if err == nil {
			s.lastSweep = time.Now()
			s.publishOpenIssues(labelled)
		}
	}

	klog.Infof("Running fast issue scan cycle...")
	assigned, err := s.scanAssigned(ctx)
	if err != nil {
		klog.Errorf("Failed to scan assigned issues: %v", err)
	}
	s.queueTasks(ctx, assigned, refIssues)
}

// sweepDue reports whether the trigger-labelled sweep has come due. It runs on
// the first cycle, so a restart does not wait out a full sweep interval.
func (s *Scanner) sweepDue() bool {
	return s.lastSweep.IsZero() || time.Since(s.lastSweep) >= s.cfg.SweepInterval
}

// primeOpenPRs fills the open PR half of the entity cache on behalf of a pull
// request scanner that is not running.
//
// That half belongs to the pull request scanner, which republishes it every
// sweep, and this scan only reads it. But the two are gated independently: a
// deployment can scan issues with pull request scanning switched off entirely,
// and there the cache would never be published to at all. Since the scan fails
// closed on an unpublished cache, that is not a slow issue scan but no issue
// scan ever, so the scanner pays for the one listing itself.
//
// It is deliberately not a fallback for "the pull request scanner has not got
// to it yet". Priming whenever the cache happened to be cold would have both
// scanners issue the same paginated listing on every start, and would leave the
// cache with two writers.
func (s *Scanner) primeOpenPRs(ctx context.Context) {
	if !s.cfg.PrimeOpenPRs || s.entities.HasOpenPRs() || !s.gh.Ready() {
		return
	}
	klog.Infof("Populating open PRs cache for referenced issues...")
	prs, err := s.gh.ListOpenPRs(ctx)
	if err != nil {
		klog.Errorf("Failed to populate open PRs cache: %v", err)
		return
	}
	s.entities.UpdateOpenPRs(prs)
}

// publishOpenIssues records which issues are known to be open in the shared
// entity cache. The sandbox reconciler uses it to skip sandboxes belonging to
// live work instead of confirming every one of them against GitHub.
//
// Only the issues the sweep saw open are published. An issue the watcher has
// worked on carries the trigger label, so it is in the sweep while it is open
// and drops out when it closes - which is what lets the reconciler confirm it
// closed, nudge the workflows linked to it and collect its sandbox. Padding
// the set with every processed issue kept those issues "open" forever
// (k8s-config-connector#13652, closed by a merged PR, never nudged #13244).
func (s *Scanner) publishOpenIssues(openIssues []*githubv39.Issue) {
	nums := make([]int, 0, len(openIssues))
	for _, iss := range openIssues {
		if num := iss.GetNumber(); num > 0 {
			nums = append(nums, num)
		}
	}
	s.entities.SetOpenIssueNumbers(nums)
}

// queueTasks queues a fix - or the workflow the issue asks for - for every
// issue that still needs one.
func (s *Scanner) queueTasks(ctx context.Context, issues []*githubv39.Issue, refIssues map[int]bool) {
	klog.Infof("queueTasks called with %d issues", len(issues))
	for _, issue := range issues {
		s.queueTask(ctx, issue, refIssues)
	}
}

// queueTask evaluates one issue and queues its task if it needs one.
func (s *Scanner) queueTask(ctx context.Context, issue *githubv39.Issue, refIssues map[int]bool) {
	num := issue.GetNumber()
	if s.cfg.MinNumber > 0 && num < s.cfg.MinNumber {
		return
	}
	s.rec = s.startRecording(issue)
	defer func() { s.rec.Finish(); s.rec = nil }()
	if conventions.HasStopLabel(issue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}
	if why := s.fanoutWork(issue); why != "" {
		klog.Infof("Skipping issue #%d because %s", num, why)
		return
	}
	if refIssues[num] {
		s.rec.Fill(func(it *rules.Item) { it.LinkedPR = "open" })
		klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
		return
	}

	// An issue may name a workflow in its description, in which case the task
	// runs that definition instead of the standard fix. A workflow named by
	// convention is identified from its path alone, so the gates keyed on its
	// task name run before its definition is fetched: an issue whose workflow
	// is already queued, running or cooling down costs no fetch here.
	wf := identifyWorkflow(ctx, s.gh, issue)
	workflowPath, workflowName := wf.Path, wf.Name
	filename := issueTaskFilename(num, workflowName)
	rule := "fix"
	if workflowName != "" {
		rule = "workflow"
	}
	s.rec.Fill(func(it *rules.Item) { it.Workflow = workflowName })
	s.recordRuns(rule, filename)
	if s.carryOutRuleTasks(ctx, issue) {
		return
	}
	if s.rules.Off(rule) {
		klog.V(2).Infof("Skipping issue #%d: the %s rule is disabled", num, rule)
		return
	}

	if s.queue.TaskExists(filename) {
		s.rec.Fill(func(it *rules.Item) { it.Queued = append(it.Queued, rule) })
		return
	}
	// One task per issue at a time, whatever the rule: a fix queued or
	// running holds back a workflow its edited body now names, and the
	// other way round (they run in different sandboxes, so nothing else
	// would keep them apart). The rules hold back the same way
	// (Item.TaskActive), as for a pull request.
	if s.queue.HasActiveIssueTask(num) {
		s.rec.Fill(func(it *rules.Item) { it.TaskActive = true })
		klog.Infof("Skipping issue #%d: another task of the issue is queued or running", num)
		return
	}

	// The rule's policy - cooldown, once per update, a give-up due - asked
	// on these cheap facts before the reads below.
	if s.engineHolds(ctx, issue, filename, workflowPath, workflowName, rule) {
		return
	}

	wf = wf.check(ctx, s.gh)
	switch wf.Status {
	case common.WorkflowMissing, common.WorkflowUnusable:
		s.rec.Fill(func(it *rules.Item) { it.WorkflowMissing = true })
		// The engine's workflow-missing (off, or not decided: the issue is
		// neither stopped nor stepped); its stop is the handler's, which
		// posts the rule's comment with the definition and its load error.
		d, ok := decisionOf(s.rules.Decide(*s.rec.Item, s.rulesEnv()), "workflow-missing")
		if !ok {
			klog.Infof("Skipping issue #%d: the rules engine does not decide workflow-missing", num)
			return
		}
		s.rec.Decided("workflow-missing", rules.OutcomeRun)
		s.stopForUnavailableWorkflow(ctx, num, wf, d.Writes.Comment)
		return
	case common.WorkflowUnresolved:
		// Whether the definition exists is unknown, so neither run it nor
		// stop the issue; the next cycle will look again.
		s.rec.Gated("fetching its workflow failed")
		klog.Warningf("Skipping issue #%d this cycle: could not fetch its workflow %s: %v", num, wf.Path, wf.Err)
		return
	}

	var timeline []*githubv39.Timeline
	timelineComplete := false
	if s.gh.Ready() {
		tl, complete, err := s.gh.ListIssueTimeline(ctx, num)
		if err != nil {
			klog.Warningf("Failed to list timeline for issue #%d: %v", num, err)
		} else {
			timeline = tl
			timelineComplete = complete
		}
	}

	// Skip the linked-PR check for workflow triggers, which do not necessarily
	// have a linked code PR.
	if workflowName == "" {
		// Only let the linked-PR check trust the timeline we already fetched if
		// it is complete. A missing or truncated timeline cannot prove the
		// absence of a linked PR, so pass nil and let HasLinkedPRWithTimeline
		// fall back to the Search API.
		var verifiedTimeline []*githubv39.Timeline
		if timelineComplete {
			verifiedTimeline = timeline
		}
		linked, err := s.gh.HasLinkedPRWithTimeline(ctx, num, verifiedTimeline)
		if err != nil {
			s.rec.Gated("checking the linked PR failed")
			klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
			return
		} else if linked {
			s.rec.Fill(func(it *rules.Item) { it.LinkedPR = "open" })
			klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
			return
		}
	}

	sandboxName := fmt.Sprintf("fix-%s-%d", s.gh.Repo(), num)
	if workflowName != "" {
		sandboxName = fmt.Sprintf("wf-issue-%d", num)
	}

	if reason, err := s.sandboxes.RequiresHuman(ctx, sandboxName); err != nil {
		s.rec.Gated("checking the sandbox failed")
		klog.Errorf("Failed to check whether sandbox %s needs a person: %v", sandboxName, err)
		return
	} else if reason != "" {
		s.rec.Gated("its sandbox needs a person")
		klog.Infof("Skipping issue #%d because sandbox %s needs a person: %s", num, sandboxName, reason)
		return
	}

	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		s.rec.Gated("checking the sandbox failed")
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		s.rec.Gated("its sandbox is busy")
		klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	wasAutoLabeled := false
	if !conventions.HasTriggerLabel(issue.Labels, s.cfg.TriggerLabel) {
		wasAutoLabeled = true
		s.applyTriggerLabel(ctx, num)
	}

	triggerEventTime, triggerReason, triggerNotes := triggerInfo(issue, timeline, s.cfg.TriggerLabel, wasAutoLabeled)

	taskType := api.TypeIssueRule
	if workflowName != "" {
		taskType = api.TypeAgentChore
	}

	recipeName := ""
	if r := s.rules.Set().Named(rule); r != nil {
		recipeName = r.Run.Recipe
	}
	taskAssignee := s.selectUser(ctx, taskType, recipeName, num)

	var task *api.QueueTask
	if workflowName != "" {
		task = s.newTask(taskOptions{
			Type:             api.TypeAgentChore,
			Issue:            issue,
			Phase:            api.PhaseChores,
			Assignee:         taskAssignee,
			TriggerEventTime: triggerEventTime,
			TriggerReason:    triggerReason,
			TriggerNotes:     triggerNotes,
			AgentFile:        workflowPath,
			SessionID:        fmt.Sprintf("issue-%d", num),
		})
	} else {
		task = s.newTask(taskOptions{
			Type:             api.TypeIssueRule,
			Issue:            issue,
			Phase:            api.PhaseInvestigate,
			Assignee:         taskAssignee,
			TriggerEventTime: triggerEventTime,
			TriggerReason:    triggerReason,
			TriggerNotes:     triggerNotes,
		})
	}

	// The engine decides on the facts gathered above: no decision, nothing
	// queued; its give-up stops the issue instead (the rule has run its
	// failed attempts out since a human last spoke on it or it was last
	// paused); its run is the task, with the rule's recipe on it.
	s.recordSpoken(ctx, num)
	d, ok := decisionOf(s.rules.Decide(*s.rec.Item, s.rulesEnv()), rule)
	act.Task(task, d, s.rules.Set().Named(rule))
	switch {
	case !ok:
		klog.Infof("Skipping issue #%d: the rules engine does not decide %s", num, rule)
		return
	case d.Outcome == rules.OutcomeGiveUp:
		s.rec.Decided(rule, rules.OutcomeGiveUp)
		klog.Infof("Stopping issue #%d: the rules engine gives up on %s", num, rule)
		act.ApplyWrites(ctx, s.gh, num, d.Writes, s.cfg.TriggerLabel, s.cfg.DryRun)
		if !s.cfg.DryRun {
			if err := s.queue.RemovePendingTasksForNumber(num); err != nil {
				klog.Warningf("Failed to withdraw pending tasks for issue #%d: %v", num, err)
			}
		}
		return
	}
	s.rec.Decided(rule, rules.OutcomeRun)
	if s.cfg.DryRun {
		if workflowName != "" {
			fmt.Printf("[DRYRUN] Would queue workflow task %s for issue #%d: %s\n", workflowName, num, task.URL)
		} else {
			fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, task.URL)
		}
		return
	}

	if workflowName != "" {
		fmt.Printf("Queueing workflow task %s for issue #%d...\n", workflowName, num)
	} else {
		fmt.Printf("Queueing fix task for issue #%d...\n", num)
	}
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue task for issue #%d: %v", num, err)
	}
}

// engineHolds is whether the rule's policy holds it back on the issue now
// (rules.Set.Held), on the facts recorded so far and a workflow's cooldown
// (read only after a run that succeeded: a failed run's cooldown is the
// rule's own). A cooldown
// is noted on the queue, for the overseer's work view, and logged once per
// window.
func (s *Scanner) engineHolds(ctx context.Context, issue *githubv39.Issue, filename, workflowPath, workflowName, rule string) bool {
	last := s.queue.GetProcessedTask(filename)
	if workflowName != "" && last != nil && last.Status != api.StatusFailed {
		cooldown, err := common.GetWorkflowCooldown(ctx, s.gh, workflowPath)
		if err != nil {
			// Not the default: a cooldown not known holds the issue back
			// this scan; the next reads it again.
			s.rec.Gated("reading its workflow's cooldown failed")
			klog.Warningf("Skipping issue #%d this cycle: %v", issue.GetNumber(), err)
			return true
		}
		s.rec.Fill(func(it *rules.Item) { it.WorkflowCooldown = cooldown })
	}
	why, until := s.rules.Set().Held(rule, *s.rec.Item, s.rulesEnv())
	if why == "" {
		return false
	}
	if until.IsZero() || last == nil {
		klog.V(2).Infof("Skipping issue #%d: the rules engine holds %s back: %s", issue.GetNumber(), rule, why)
		return true
	}
	held := api.Cooldown{
		FileName:        filename,
		Number:          issue.GetNumber(),
		URL:             issue.GetHTMLURL(),
		Workflow:        workflowName,
		Until:           until,
		LastStatus:      last.Status,
		LastCompletedAt: last.CompletedAt,
	}
	if s.queue.NoteCooldown(held) {
		klog.Infof("Skipping issue #%d: the rules engine holds %s back: %s (until %s)", held.Number, rule, why, until.UTC().Format(time.RFC3339))
	}
	return true
}

// applyTriggerLabel adopts an issue that was picked up by assignment rather
// than by label, so that the label always records what the watcher is acting on.
func (s *Scanner) applyTriggerLabel(ctx context.Context, num int) {
	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d\n", s.cfg.TriggerLabel, num)
		return
	}
	klog.Infof("Adding '%s' label to issue #%d", s.cfg.TriggerLabel, num)
	if err := s.gh.AddLabels(ctx, num, []string{s.cfg.TriggerLabel}); err != nil {
		klog.Errorf("Failed to add label '%s' to issue #%d: %v", s.cfg.TriggerLabel, num, err)
	}
}

// stopForUnavailableWorkflow pauses an issue whose workflow definition does
// not exist or cannot be used, and tells its author why: the
// workflow-missing rule's comment (ruleComment), then which definition and
// its error. Queueing the run would only fail, and fail again after every
// cooldown, with nothing on the issue to say so.
//
// The stop label goes on first. It is what keeps later cycles from coming back
// here, so if it cannot be applied the comment is held back too: the next
// cycle retries both, rather than posting the same comment every cycle.
func (s *Scanner) stopForUnavailableWorkflow(ctx context.Context, num int, wf issueWorkflow, ruleComment string) {
	stopLabel := conventions.StopLabel(s.cfg.TriggerLabel)
	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d and comment: workflow %s unavailable: %v\n", stopLabel, num, wf.Path, wf.Err)
		return
	}
	klog.Infof("Stopping issue #%d: its workflow %s is unavailable: %v", num, wf.Path, wf.Err)
	if err := s.gh.AddLabels(ctx, num, []string{stopLabel}); err != nil {
		klog.Errorf("Failed to add stop label '%s' to issue #%d: %v", stopLabel, num, err)
		return
	}
	if err := s.queue.RemovePendingTasksForNumber(num); err != nil {
		klog.Warningf("Failed to withdraw pending tasks for issue #%d: %v", num, err)
	}
	if err := s.gh.AddComment(ctx, num, unavailableWorkflowComment(ruleComment, wf, stopLabel)); err != nil {
		klog.Errorf("Failed to comment on issue #%d about its unavailable workflow: %v", num, err)
	}
}

// unavailableWorkflowComment is the stop comment of the workflow-missing
// rule (act.StopComment of its words), then which definition could not be
// found or loaded, what to check, and its error.
func unavailableWorkflowComment(ruleComment string, wf issueWorkflow, stopLabel string) string {
	var b strings.Builder
	b.WriteString(act.StopComment(ruleComment, stopLabel))
	if wf.Status == common.WorkflowUnusable {
		fmt.Fprintf(&b, "\n\nThe workflow definition `%s` could not be loaded: fix the problem described below.", wf.Path)
	} else {
		fmt.Fprintf(&b, "\n\nThe workflow definition `%s` could not be found (or is not accessible): check that the path is correct, that the file exists on the default branch and is readable by the bot, and that any `redirect` it declares points to a file that exists.", wf.Path)
	}
	if wf.Err != nil {
		fmt.Fprintf(&b, "\n\n<details><summary>Details</summary>\n\n```\n%v\n```\n</details>", wf.Err)
	}
	return b.String()
}

// selectUser resolves the bot account the task runs as, falling back to the
// watcher's own account when role selection cannot answer.
func (s *Scanner) selectUser(ctx context.Context, taskType api.TaskType, recipe string, num int) string {
	if s.users == nil {
		return s.cfg.TargetAssignee
	}
	assignee, err := s.users.SelectUser(ctx, taskType, recipe, num)
	if err != nil {
		klog.Errorf("Failed to select user for issue #%d: %v", num, err)
		return s.cfg.TargetAssignee
	}
	if assignee == "" {
		return s.cfg.TargetAssignee
	}
	return assignee
}
