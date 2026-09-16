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
	"os"
	"path/filepath"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

const (
	// DefaultInterval is how often the issues assigned to the bot pool or
	// created by the operator are scanned. It is what sets pickup latency for a
	// new issue, and the queries behind it are bounded to a single page.
	DefaultInterval = 30 * time.Second
	// DefaultSweepInterval is how often the full trigger-labelled sweep runs.
	// That sweep paginates over every labelled issue in the repository, so it
	// stays on the slow cadence it has always had; the fast cycle above is what
	// picks up the issues a person just filed or assigned.
	DefaultSweepInterval = 5 * time.Minute
	// defaultScanLimit bounds the fast queries when no limit is configured.
	defaultScanLimit = 30
)

// Queue is the subset of the task queue the Scanner needs: it adds issue tasks,
// and withdraws the pending ones for an issue that has since been stopped.
type Queue interface {
	// TaskExists reports whether a task with the given file name is queued or running.
	TaskExists(filename string) bool
	// Enqueue adds a task to the queue under the given file name.
	Enqueue(filename string, task *api.QueueTask) error
	// RemovePendingTasksForNumber drops the not-yet-started tasks targeting an issue.
	RemovePendingTasksForNumber(number int) error
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
}

// UserSelector picks the bot account a task should run as. Role configuration
// and sandbox pinning live with the watcher, so the Scanner asks rather than
// deciding.
type UserSelector interface {
	SelectUser(ctx context.Context, taskType api.TaskType, number int) (string, error)
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
	// ScanLimit caps the page size of the fast queries. Defaults to defaultScanLimit.
	ScanLimit int
	// MinNumber skips issues numbered below it, which is how a deployment
	// ignores a repository's history. Zero scans everything.
	MinNumber int
	// ProcessedDir is the queue directory holding completed task files, read to
	// recover when each issue was last worked on.
	ProcessedDir string
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
	paused    func() bool

	// processed records when each issue was last queued or completed, read from
	// the processed queue directory on first use.
	processed map[int]time.Time
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
	return &Scanner{
		cfg:       cfg,
		gh:        deps.GitHub,
		queue:     deps.Queue,
		entities:  deps.Entities,
		sandboxes: deps.Sandboxes,
		users:     deps.Users,
		paused:    deps.Paused,
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

// primeOpenPRs fills the open PR half of the entity cache when no pull request
// scan has published to it yet.
//
// The pull request scanner owns that half and refreshes it on its own cadence,
// but it does not run in every mode, and on startup it may not have run yet.
// Rather than stall issue scanning until it does, the scanner pays for the one
// listing itself - it is a single paginated query, against the several per pull
// request that a full PR evaluation costs.
func (s *Scanner) primeOpenPRs(ctx context.Context) {
	if s.entities.HasOpenPRs() || !s.gh.Ready() {
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
// Issues that have already been processed are treated as open: their sandbox
// may still hold a workspace whose result has not been pushed yet, and the
// reconciler confirms the state with GitHub before deleting anything anyway.
func (s *Scanner) publishOpenIssues(openIssues []*githubv39.Issue) {
	processed := s.processedIssues()
	nums := make([]int, 0, len(openIssues)+len(processed))
	for _, iss := range openIssues {
		if num := iss.GetNumber(); num > 0 {
			nums = append(nums, num)
		}
	}
	for num := range processed {
		nums = append(nums, num)
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
	if conventions.HasStopLabel(issue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}
	if refIssues[num] {
		klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
		return
	}

	// An issue may name a workflow in its description, in which case the task
	// runs that definition instead of the standard fix.
	workflowPath := common.FindWorkflowPath(issue.GetBody())
	workflowName := ""
	if workflowPath != "" {
		if common.IsWorkflowDefinition(ctx, s.gh, workflowPath) {
			filenameOnly := filepath.Base(workflowPath)
			ext := filepath.Ext(filenameOnly)
			workflowName = strings.TrimSuffix(filenameOnly, ext)
		} else {
			// It was just a standard skill/agent prompt mentioned, not a
			// workflow. Fall back to the standard issue fix.
			workflowPath = ""
		}
	}

	filename := fmt.Sprintf("task-issue-%d.yaml", num)
	if workflowName != "" {
		filename = fmt.Sprintf("task-workflow-%s-issue-%d.yaml", common.Slugify(workflowName), num)
	}

	if s.queue.TaskExists(filename) {
		return
	}

	if s.inCooldown(ctx, filename, workflowPath) {
		return
	}

	processed := s.processedIssues()
	lastProcessed, ok := processed[num]
	if ok && !issue.GetUpdatedAt().After(lastProcessed) && workflowName == "" {
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
			klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
			return
		} else if linked {
			klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
			return
		}
	}

	sandboxName := fmt.Sprintf("fix-%s-%d", s.gh.Repo(), num)
	if workflowName != "" {
		sandboxName = fmt.Sprintf("wf-issue-%d", num)
	}

	running, err := s.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return
	} else if running {
		klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
		return
	}

	wasAutoLabeled := false
	if !conventions.HasTriggerLabel(issue.Labels, s.cfg.TriggerLabel) {
		wasAutoLabeled = true
		s.applyTriggerLabel(ctx, num)
	}

	triggerEventTime, triggerReason, triggerNotes := triggerInfo(issue, timeline, s.cfg.TriggerLabel, wasAutoLabeled)

	taskType := api.TypeIssueFix
	if workflowName != "" {
		taskType = api.TypeAgentChore
	}

	taskAssignee := s.selectUser(ctx, taskType, num)

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
			Type:             api.TypeIssueFix,
			Issue:            issue,
			Phase:            api.PhaseInvestigate,
			Assignee:         taskAssignee,
			TriggerEventTime: triggerEventTime,
			TriggerReason:    triggerReason,
			TriggerNotes:     triggerNotes,
		})
	}

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
	processed[num] = time.Now()
	if err := s.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to queue task for issue #%d: %v", num, err)
	}
}

// inCooldown reports whether the same task completed recently enough that it
// must not be queued again yet. The cooldown is declared by the workflow
// definition; a standard fix uses the default.
func (s *Scanner) inCooldown(ctx context.Context, filename, workflowPath string) bool {
	processedPath := filepath.Join(s.cfg.ProcessedDir, filename)
	info, err := os.Stat(processedPath)
	if err != nil {
		return false
	}

	// The file's timestamp is only a proxy for when the task finished; the
	// recorded completion time is authoritative when the file holds one.
	lastRunTime := info.ModTime()
	if data, err := os.ReadFile(processedPath); err == nil {
		var t api.QueueTask
		if err := yaml.Unmarshal(data, &t); err == nil && !t.CompletedAt.IsZero() {
			lastRunTime = t.CompletedAt
		}
	}
	return time.Since(lastRunTime) < common.GetWorkflowCooldown(ctx, s.gh, workflowPath)
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

// selectUser resolves the bot account the task runs as, falling back to the
// watcher's own account when role selection cannot answer.
func (s *Scanner) selectUser(ctx context.Context, taskType api.TaskType, num int) string {
	if s.users == nil {
		return s.cfg.TargetAssignee
	}
	assignee, err := s.users.SelectUser(ctx, taskType, num)
	if err != nil {
		klog.Errorf("Failed to select user for issue #%d: %v", num, err)
		return s.cfg.TargetAssignee
	}
	if assignee == "" {
		return s.cfg.TargetAssignee
	}
	return assignee
}

// processedIssues returns when each issue was last worked on, reading the
// processed queue directory on first use.
func (s *Scanner) processedIssues() map[int]time.Time {
	if s.processed == nil {
		s.processed = loadProcessedIssues(s.cfg.ProcessedDir)
	}
	return s.processed
}
