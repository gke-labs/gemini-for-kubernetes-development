// Package prs owns the pull request side of the watch daemon: evaluating the
// open pull requests the watcher is responsible for and queueing the work they
// need - rebasing a conflicted branch, investigating failed CI, addressing
// review feedback, or reviewing the change.
//
// Evaluating one pull request costs the better part of a dozen GitHub requests
// (commits, comments, reviews, inline review comments, check runs, commit
// statuses, merge queue state), which is what made this the slowest part of the
// daemon and the reason it now runs as its own goroutine: nothing else has to
// wait behind it. Within a cycle the pull requests are evaluated on a bounded
// worker pool, so one slow pull request no longer holds up the rest.
package prs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

const (
	// DefaultInterval is how often the pull requests assigned to the bot pool
	// are evaluated. Those are the ones with work in flight, and the listing
	// behind them is a single page per bot account.
	DefaultInterval = 1 * time.Minute
	// DefaultSweepInterval is how often every pull request the watcher is
	// responsible for is evaluated, and the open PR cache refreshed.
	//
	// Evaluation cost scales with the number of open pull requests, at roughly
	// a dozen requests each, so this interval is what keeps a busy repository
	// inside its hourly GitHub rate limit. Shortening it is not a free latency
	// win: the fast pass above already covers the pull requests that have work
	// in flight.
	DefaultSweepInterval = 5 * time.Minute
	// DefaultWorkers is how many pull requests are evaluated concurrently.
	// The pool bounds the burst of GitHub requests a cycle can produce while
	// still keeping one slow pull request from delaying the others.
	DefaultWorkers = 2
	// defaultScanLimit bounds the fast query when no limit is configured.
	defaultScanLimit = 30
)

// Queue is the subset of the task queue the Scanner needs. It adds pull request
// tasks, withdraws the pending ones for a pull request that has been stopped,
// reports whether a pull request has work in flight - which is what keeps the
// ready-for-human label from flapping while a task is running - and reports
// what has already finished.
//
// The finished work is read through here rather than off the filesystem
// because the queue owns the task files. A scanner that opened the processed
// directory itself would be a second reader of state it does not control, and
// would miss everything that finished after it first looked.
type Queue interface {
	// TaskExists reports whether a task with the given file name is queued or running.
	TaskExists(filename string) bool
	// HasActivePRTask reports whether any task for the pull request is queued or running.
	HasActivePRTask(prNumber int) bool
	// Enqueue adds a task to the queue under the given file name.
	Enqueue(filename string, task *api.QueueTask) error
	// RemovePendingTasksForNumber drops the not-yet-started tasks targeting a pull request.
	RemovePendingTasksForNumber(number int) error
	// GetProcessedTask returns the finished task recorded under the given file
	// name, or nil when nothing by that name has finished.
	GetProcessedTask(filename string) *api.QueueTask
	// ListProcessedTasks returns every finished task, keyed by task file name.
	ListProcessedTasks() map[string]*api.QueueTask
}

// Entities is the shared view of what the scanners have observed. The Scanner
// is the owner of its pull request half: it publishes the open pull requests,
// from which the issue scanner learns which issues already have a fix in
// flight, and the sandbox reconciler learns which sandboxes are still live.
type Entities interface {
	// UpdateOpenPRs replaces the cached open pull requests.
	UpdateOpenPRs(prs []*githubv39.PullRequest)
}

// Sandboxes resolves sandbox names and reports whether one is mid-run, which is
// the last check before queueing: a sandbox already executing holds the
// workspace a second task would fight over.
type Sandboxes interface {
	ResolveName(ctx context.Context, taskType api.TaskType, num int) string
	IsTaskRunning(ctx context.Context, name string) (bool, error)
}

// Config holds the tuning knobs of a Scanner.
type Config struct {
	// Interval is the delay between fast cycles over assigned pull requests.
	// Defaults to DefaultInterval.
	Interval time.Duration
	// SweepInterval is the delay between full sweeps. Defaults to DefaultSweepInterval.
	SweepInterval time.Duration
	// Workers is the size of the evaluation pool. Defaults to DefaultWorkers.
	Workers int
	// TriggerLabel is the label marking a pull request as the watcher's to work on.
	TriggerLabel string
	// GitHubLogin is the watcher's own account, whose comments are never
	// treated as feedback to act on.
	GitHubLogin string
	// BotUsers is the pool of bot accounts whose pull requests are evaluated.
	// A pull request authored outside the pool is skipped: the watcher cannot
	// push to a fork it does not own.
	BotUsers []string
	// ReviewerLogins are the accounts whose reviews count as automated review
	// feedback rather than as noise.
	ReviewerLogins []string
	// AllowlistedBots are the automated accounts whose comments are acted on.
	AllowlistedBots []string
	// ScanLimit caps the page size of the fast query. Defaults to defaultScanLimit.
	ScanLimit int
	// MinNumber skips pull requests numbered below it. Zero evaluates everything.
	MinNumber int
	// InactivityTimeout pauses a pull request that has seen no human activity
	// for this long. Zero disables the check.
	InactivityTimeout time.Duration
	// DryRun reports what would be queued without touching the queue or GitHub.
	DryRun bool
}

// Deps holds the collaborators of a Scanner.
type Deps struct {
	// GitHub is the repository-bound client used for every query and write.
	// It carries the owner and repo, which is why the Config does not.
	GitHub *github.Client
	// Queue receives the pull request tasks the scan creates.
	Queue Queue
	// Entities is the shared open PR / open issue cache.
	Entities Entities
	// Sandboxes probes whether a sandbox is already running the work.
	Sandboxes Sandboxes
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

// Scanner evaluates open pull requests and queues the work they need.
type Scanner struct {
	cfg       Config
	gh        *github.Client
	queue     Queue
	entities  Entities
	sandboxes Sandboxes
	paused    func() bool

	// reactions reads the acknowledgement state the watcher records on
	// comments. It is bound once to this scanner's identity and bot allowlist,
	// so no call site has to restate whose marks count as the watcher's.
	reactions *conventions.ReactionInterpreter

	// state records what has already been done for each pull request. Unlike
	// the other subcontrollers' bookkeeping it is mutex-guarded, because the
	// worker pool evaluates several pull requests at once.
	state *stateStore
	// lastSweep is when the full sweep last ran.
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
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
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
		paused:    deps.Paused,
		reactions: conventions.NewReactionInterpreter(deps.GitHub, cfg.GitHubLogin, cfg.AllowlistedBots),
		state:     newStateStore(deps.Queue),
	}
}

// Run evaluates pull requests until ctx is cancelled, returning nil once it has
// stopped.
//
// A cycle runs immediately so that a restart picks up the pull requests that
// changed while the daemon was down, instead of waiting out a full interval.
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

// ScanOnce runs one cycle: a full sweep when one has come due, and otherwise a
// fast pass over the pull requests assigned to the bot pool.
//
// The two are alternatives rather than additions. The sweep's candidate set is
// a superset of the fast pass's, so running both in one cycle would evaluate
// the assigned pull requests twice for nothing.
func (s *Scanner) ScanOnce(ctx context.Context) {
	if s.paused != nil && s.paused() {
		klog.V(2).Infof("Skipping pull request scan because the watcher is draining.")
		return
	}

	if s.lastSweep.IsZero() || time.Since(s.lastSweep) >= s.cfg.SweepInterval {
		s.sweep(ctx)
		return
	}
	s.fastPass(ctx)
}

// sweep refreshes the open pull request cache and evaluates every pull request
// the watcher is responsible for.
func (s *Scanner) sweep(ctx context.Context) {
	klog.Infof("Running full PR scan cycle...")
	s.lastSweep = time.Now()

	// Publish the open pull requests first: the issue scanner reads the
	// referenced-issue map derived from them to tell which issues already have
	// a fix in flight, and it should see this cycle's picture, not the last
	// one's. A failed listing leaves the previous copy in place rather than
	// publishing a partial one as though it were complete.
	prs, err := s.gh.ListOpenPRs(ctx)
	if err != nil {
		klog.Errorf("Failed to list open PRs: %v", err)
	} else {
		s.entities.UpdateOpenPRs(prs)

		// Repair the pull requests the listing below cannot see before it
		// runs, so one adopted here is evaluated in this cycle, not the next.
		s.adoptOrphanedBotPRs(ctx, prs)
	}

	candidates, err := s.scanCandidates(ctx)
	if err != nil {
		klog.Errorf("Failed to scan PR issues: %v", err)
	}

	s.evaluateAll(ctx, candidates)
}

// fastPass evaluates the pull requests currently assigned to the bot pool,
// which are the ones with work in flight and therefore the ones whose state
// changes between sweeps.
func (s *Scanner) fastPass(ctx context.Context) {
	candidates, err := s.scanAssigned(ctx)
	if err != nil {
		klog.Errorf("Failed to scan assigned PRs: %v", err)
	}
	if len(candidates) == 0 {
		return
	}
	klog.Infof("Evaluating %d assigned PRs...", len(candidates))
	s.evaluateAll(ctx, candidates)
}

// evaluateAll evaluates the candidates on a bounded worker pool and returns
// once every one of them has been handled.
//
// The pool is what keeps a pull request whose comment history takes ten seconds
// to page from delaying every pull request behind it. Each candidate appears at
// most once per cycle, so no two workers ever touch the same pull request's
// state.
func (s *Scanner) evaluateAll(ctx context.Context, candidates []*githubv39.Issue) {
	if len(candidates) == 0 {
		return
	}

	work := make(chan *githubv39.Issue)
	var wg sync.WaitGroup
	for i := 0; i < s.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for prIssue := range work {
				s.evaluate(ctx, prIssue)
			}
		}()
	}

	for _, prIssue := range candidates {
		select {
		case <-ctx.Done():
			// Stop feeding the pool on shutdown; the workers drain what they
			// already took and the next run picks the rest up.
			close(work)
			wg.Wait()
			return
		case work <- prIssue:
		}
	}
	close(work)
	wg.Wait()
}

// evaluate decides what a single pull request needs and queues it.
//
// The phases are ordered rather than independent: unaddressed feedback wins
// over everything, because a human asking for a change makes the conflict or
// the CI failure the agent would otherwise chase irrelevant. A conflicted
// branch is next, since nothing can be verified until it merges; then CI
// failures; and only a green, unreviewed pull request is reviewed.
func (s *Scanner) evaluate(ctx context.Context, prIssue *githubv39.Issue) {
	num := prIssue.GetNumber()
	if s.cfg.MinNumber > 0 && num < s.cfg.MinNumber {
		return
	}
	if conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping PR #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		s.reconcileReadyForHumanLabel(ctx, num, prIssue, false, "")
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}
	pr, err := s.gh.GetPullRequest(ctx, num)
	if err != nil {
		klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
		return
	}

	// A pull request in the merge queue is out of the watcher's hands: pushing
	// to it now would only knock it back out.
	inMergeQueue, err := s.gh.IsInMergeQueue(ctx, num)
	if err != nil {
		klog.Errorf("Failed to check if PR #%d is in merge queue: %v", num, err)
	} else if inMergeQueue {
		klog.Infof("Skipping PR #%d because it is in the merge queue", num)
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}

	// Only pull requests created by a bot in the pool can be worked on: we do
	// not have permission to push to an external fork.
	author := pr.GetUser().GetLogin()
	isBotPR := false
	for _, bot := range s.cfg.BotUsers {
		if strings.EqualFold(author, bot) {
			isBotPR = true
			break
		}
	}
	if !isBotPR {
		klog.Infof("Skipping PR #%d because it was created by %s (not in our bot pool). We do not have permission to push to external forks.", num, author)
		return
	}

	// Sync labels from referenced parent issues to the PR, then re-check: the
	// stop label may have been inherited by the sync we just performed.
	s.syncReferencedIssueLabels(ctx, pr, prIssue)
	if conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping PR #%d after label sync because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		s.reconcileReadyForHumanLabel(ctx, num, prIssue, false, "")
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}

	headSHA := pr.GetHead().GetSHA()

	history, err := s.fetchHistory(ctx, num)
	if err != nil {
		klog.Errorf("Failed to fetch history for PR #%d: %v", num, err)
		return
	}

	state := s.state.get(num)

	if s.pauseIfInactive(ctx, pr, prIssue, history, headSHA) {
		return
	}

	// Check Phase 1: Rebase/Conflicts
	isConflicting := pr.Mergeable != nil && !*pr.Mergeable

	var checkAnalysis prCheckAnalysis
	var canReview bool

	// The retry state is read before the feedback is evaluated because it
	// decides how the evaluation reads: what a failed attempt left on the
	// comments describes that attempt, not the one about to be queued.
	feedbackRetry := s.commentRetryState(num, headSHA, pr, history)
	commentAnalysis := s.evaluateComments(ctx, num, pr, history, history.lastCommitTime, state.lastCommentAddressedTime, state.lastCommentAddressedSHA, headSHA, feedbackRetry)

	if !isConflicting {
		checkAnalysis = s.evaluateChecks(ctx, headSHA)
		isApproved := isPRApprovedOrLGTM(pr, prIssue, history.reviews)
		if isApproved {
			klog.V(2).Infof("PR #%d is approved / LGTM'd", num)
		}
		if !checkAnalysis.hasFailure && !checkAnalysis.hasPending && !isApproved && state.lastReviewedSHA != headSHA && s.shouldAutoReviewPR(ctx, pr, prIssue) {
			canReview = !hasBotReviewAfterLastCommit(history.reviews, history.lastCommitTime, headSHA, s.cfg.GitHubLogin, s.cfg.AllowlistedBots)
		}
	}

	assignedBot := conventions.AssignedBotUser(prIssue, s.cfg.BotUsers)
	isExplicitlyAssigned := assignedBot != ""

	taskAssignee := assignedBot
	if taskAssignee == "" {
		taskAssignee = author
	}

	canInvestigate := !isConflicting && checkAnalysis.hasFailure && s.canInvestigatePR(num, headSHA, isExplicitlyAssigned, state, history.comments, history.lastCommitTime)

	shortSHA := headSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	pc := &prContext{
		pr:                   pr,
		prIssue:              prIssue,
		headSHA:              headSHA,
		shortSHA:             shortSHA,
		lastCommitTime:       history.lastCommitTime,
		taskAssignee:         taskAssignee,
		isExplicitlyAssigned: isExplicitlyAssigned,
		prURL:                fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.gh.Owner(), s.gh.Repo(), num),
	}

	// Top level case statement for handling each type of PR task
	switch {
	case commentAnalysis.hasNewComments:
		s.handlePRComments(ctx, pc, commentAnalysis, feedbackRetry)

	case isConflicting:
		s.handlePRIterate(ctx, pc)
		return

	case canInvestigate:
		s.handlePRInvestigate(ctx, pc, checkAnalysis, history.comments)

	case canReview:
		s.handlePRReview(ctx, pc, checkAnalysis.checkRuns)
	}

	s.reconcileReadiness(ctx, pc, checkAnalysis, commentAnalysis, history, isConflicting, assignedBot)
}

// reconcileReadiness decides whether a pull request is ready for a human and
// applies the consequences: the label, and unassigning the bot that was working
// on it.
//
// Every gate has to hold, including that no task is queued or running for the
// pull request. Reading that from the in-memory queue rather than from disk is
// what stopped the label from flapping while a task file was being renamed
// between directories.
func (s *Scanner) reconcileReadiness(
	ctx context.Context,
	pc *prContext,
	checkAnalysis prCheckAnalysis,
	commentAnalysis prCommentAnalysis,
	history *prHistory,
	isConflicting bool,
	assignedBot string,
) {
	num := pc.prIssue.GetNumber()

	isReviewRequired := s.shouldAutoReviewPR(ctx, pc.pr, pc.prIssue)
	hasBotReviewOnHead := s.hasCompletedBotReviewOnHead(history.reviews, pc.headSHA, history.lastCommitTime)
	reviewSatisfied := !isReviewRequired || hasBotReviewOnHead

	isReadyForHuman := !isConflicting &&
		!checkAnalysis.hasFailure &&
		!checkAnalysis.hasPending &&
		!commentAnalysis.hasNewComments &&
		!s.queue.HasActivePRTask(num) &&
		reviewSatisfied &&
		!conventions.HasStopLabel(pc.prIssue.Labels, s.cfg.TriggerLabel) &&
		!pc.pr.GetDraft() &&
		pc.pr.GetState() == "open"

	s.reconcileReadyForHumanLabel(ctx, num, pc.prIssue, isReadyForHuman, pc.headSHA)

	if isReadyForHuman && assignedBot != "" {
		if s.cfg.DryRun {
			fmt.Printf("[DRYRUN] Would unassign bot %s from PR #%d (ready for human review)\n", assignedBot, num)
		} else {
			fmt.Printf("Unassigning bot %s from PR #%d (ready for human review)...\n", assignedBot, num)
			if err := s.gh.RemoveAssignees(ctx, num, []string{assignedBot}); err != nil {
				klog.Errorf("Failed to unassign bot %s from PR #%d: %v", assignedBot, num, err)
			}
		}
	}
}

// comment posts a comment on a pull request, reporting a failure rather than
// propagating it. A missed comment is cosmetic and must not abort the cycle
// that was about to queue the actual work.
func (s *Scanner) comment(ctx context.Context, num int, body string) {
	if err := s.gh.AddComment(ctx, num, body); err != nil {
		klog.Errorf("Failed to create GitHub comment on #%d: %v", num, err)
	}
}

// react records a reaction on a conversation comment. Reactions are how the
// watcher signals what it has picked up, so a failure is worth reporting but is
// never a reason to abandon the work itself.
func (s *Scanner) react(ctx context.Context, commentID int64, content conventions.Reaction) {
	if err := s.gh.AddIssueCommentReaction(ctx, commentID, string(content)); err != nil {
		klog.Warningf("Failed to create reaction '%s' on comment %d: %v", content, commentID, err)
	}
}

// reactToReviewComment records a reaction on an inline review comment.
func (s *Scanner) reactToReviewComment(ctx context.Context, commentID int64, content conventions.Reaction) {
	if err := s.gh.AddPullRequestCommentReaction(ctx, commentID, string(content)); err != nil {
		klog.Warningf("Failed to create reaction '%s' on PR review comment %d: %v", content, commentID, err)
	}
}
