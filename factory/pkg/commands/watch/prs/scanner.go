// Package prs owns the pull request side of the watch daemon: evaluating the
// open pull requests the watcher is responsible for and queueing the work they
// need - rebasing a conflicted branch, investigating failed CI, addressing
// review feedback, or reviewing the change.
//
// Evaluating one pull request costs the better part of a dozen GitHub requests
// (commits, comments, reviews, inline review comments, check runs, commit
// statuses, merge queue state), which is what made this the slowest part of the
// daemon and the reason it now runs as its own goroutine: nothing else has to
// wait behind it. Within a cycle the pull requests are evaluated sequentially.
package prs

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

const (
	// DefaultInterval is how often the pull requests assigned to the bot pool
	// are evaluated. Those are the ones with work in flight, and the listing
	// behind them is a single page per bot account.
	DefaultInterval = 2 * time.Minute
	// DefaultSweepInterval is how often every pull request the watcher is
	// responsible for is evaluated, and the open PR cache refreshed.
	//
	// Evaluation cost scales with the number of open pull requests, at roughly
	// a dozen requests each, so this interval is what keeps a busy repository
	// inside its hourly GitHub rate limit. Shortening it is not a free latency
	// win: the fast pass above already covers the pull requests that have work
	// in flight.
	DefaultSweepInterval = 10 * time.Minute
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
	// RunsSince counts the runs of the task named filename that ended and
	// started after since.
	RunsSince(filename string, since time.Time) int
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

// Listings is the shared scan (package scan): the listings the issue and
// pull request scanners both read, refreshed incrementally and kept across
// restarts. A Scanner given one reads its candidates out of it instead of
// listing for itself; what it decides is unchanged.
type Listings interface {
	// Refresh brings the shared snapshot up to date; on error the views
	// serve the last good one.
	Refresh(ctx context.Context) error
	// Labelled is every open item carrying the trigger label.
	Labelled() []*githubv39.Issue
	// Assigned is every open item assigned to user.
	Assigned(user string) []*githubv39.Issue
	// AssignedFirst is the first n open items assigned to user, newest
	// updated first.
	AssignedFirst(user string, n int) []*githubv39.Issue
}

// Config holds the tuning knobs of a Scanner.
type Config struct {
	// Interval is the delay between fast cycles over assigned pull requests.
	// Defaults to DefaultInterval.
	Interval time.Duration
	// SweepInterval is the delay between full sweeps. Defaults to DefaultSweepInterval.
	SweepInterval time.Duration
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
	// TrustedLogins are trusted regardless of the author_association GitHub
	// reports for them (see conventions.IsTrustedAuthor). Feedback from
	// anyone else without write access is never acted on.
	TrustedLogins []string
	// ScanLimit caps the page size of the fast query. Defaults to defaultScanLimit.
	ScanLimit int
	// MinNumber skips pull requests numbered below it. Zero evaluates everything.
	MinNumber int
	// InactivityTimeout pauses a pull request that has seen no human activity
	// for this long. Zero disables the check.
	InactivityTimeout time.Duration
	// DryRun reports what would be queued without touching the queue or GitHub.
	DryRun bool
	// GraphQLPRDetails reads what an evaluation needs of each pull request -
	// the pull request, its merge queue entry, commits, comments, reviews and
	// their inline comments - in one GraphQL query per batch of pull
	// requests (github.PullRequestDetails) rather than six REST calls each.
	// A pull request the query could not read whole is read the REST way.
	// CI stays REST (evaluateChecks).
	GraphQLPRDetails bool
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
	// Listings, when set, is the shared scan the scanner reads its
	// candidates from; nil lists for itself, as before.
	Listings Listings
	// Paused reports whether scanning must be held off, which is how the
	// watcher propagates drain mode. It is deliberately a plain read-only
	// signal: the scanner can observe that the queue is draining but has no way
	// to mutate it. A nil Paused never pauses.
	//
	// Shutdown does not arrive through here. Cancelling the context passed to
	// Run is what stops the scanner, and unlike this signal it also aborts a
	// cycle that has already started.
	Paused func() bool
	// Recorder records each evaluation's decisions with the facts they were
	// made on, which the engine decides on, for the watch rules' replay
	// (rules.Record); nil keeps the record for the evaluation alone.
	Recorder *rules.Recorder
	// Rules is the operator's say over the rules: those disabled, which
	// the engine does not decide. nil: every rule on.
	Rules *rules.Control
}

// Scanner evaluates open pull requests and queues the work they need.
type Scanner struct {
	cfg       Config
	gh        *github.Client
	queue     Queue
	entities  Entities
	sandboxes Sandboxes
	listings  Listings
	paused    func() bool
	recorder  *rules.Recorder
	rules     *rules.Control
	// rec is the record of the pull request evaluate is deciding about:
	// the facts the engine decides on. Evaluations run one at a time.
	rec *rules.Recording
	// engineCare is the engine's decisions on that pull request.
	engineCare []rules.Decision

	// reactions reads the acknowledgement state the watcher records on
	// comments. It is bound once to this scanner's identity and bot allowlist,
	// so no call site has to restate whose marks count as the watcher's.
	reactions *conventions.ReactionInterpreter

	// state records what has already been done for each pull request.
	state *stateStore
	// lastSweep is when the full sweep last ran.
	lastSweep time.Time
	// details are pull requests read ahead by evaluateAll's GraphQL batches
	// (Config.GraphQLPRDetails), each used once by its evaluation.
	details map[int]*github.PRDetails
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
	// The engine decides every rule, on the facts the evaluation records:
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
		listings:  deps.Listings,
		paused:    deps.Paused,
		recorder:  recorder,
		rules:     control,
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
		klog.Infof("Fetched %d open PRs from GitHub API", len(prs))
		s.entities.UpdateOpenPRs(prs)
	}

	candidates, err := s.scanCandidates(ctx)
	if err != nil {
		klog.Errorf("Failed to scan PR issues: %v", err)
	}

	s.evaluateAll(ctx, candidates)
}

// fastPass evaluates the pull requests currently assigned to the bot pool whose
// state could have moved since they were last looked at.
//
// The filter is what makes this pass cheap enough to run often. Listing the
// candidates costs one request per bot account; *evaluating* one costs the
// better part of a dozen, and on a repository whose assigned pull requests are
// mostly sitting green waiting for a human, nearly all of that was being spent
// to re-derive a verdict that had not changed.
func (s *Scanner) fastPass(ctx context.Context) {
	klog.Infof("Running fast PR scan cycle...")
	candidates, err := s.scanAssigned(ctx)
	if err != nil {
		klog.Errorf("Failed to scan assigned PRs: %v", err)
	}
	if len(candidates) == 0 {
		klog.Infof("evaluateAll called with 0 PRs")
		return
	}

	changed := make([]*githubv39.Issue, 0, len(candidates))
	for _, prIssue := range candidates {
		if s.needsEvaluation(prIssue) {
			changed = append(changed, prIssue)
		}
	}
	if len(changed) == 0 {
		klog.Infof("Skipping fast pass: none of the %d assigned PRs have moved since their last evaluation.", len(candidates))
		return
	}

	klog.Infof("Evaluating %d of %d assigned PRs...", len(changed), len(candidates))
	s.evaluateAll(ctx, changed)
}

// needsEvaluation reports whether a pull request has to be evaluated in full,
// or whether the last evaluation's verdict still stands.
//
// Only the fast pass asks. The sweep evaluates everything unconditionally,
// which is what bounds how long any of the blind spots below can last: a signal
// this gate cannot see is acted on at the next sweep at the latest.
//
// The timestamp alone would not be safe to gate on, because two things the
// scanner reacts to do not touch it:
//
//   - CI. A check run completing, or being re-run, leaves updated_at where it
//     was. Hence the "was CI in flight last time?" clause, which keeps any pull
//     request mid-CI on every cycle - the ones the fast pass exists for.
//   - The queue. A task finishing is what makes a pull request ready for a
//     human, and it may finish without pushing anything. Hence the clause
//     comparing the queue's current answer with the one recorded last time.
func (s *Scanner) needsEvaluation(prIssue *githubv39.Issue) bool {
	num := prIssue.GetNumber()
	state := s.state.get(num)

	switch {
	case state.lastEvaluatedUpdatedAt.IsZero():
		// Never evaluated in this process. A restart therefore costs one full
		// pass over the assigned pull requests, which is the price of not
		// trusting a verdict reached before the daemon went down.
		return true
	case prIssue.GetUpdatedAt().After(state.lastEvaluatedUpdatedAt):
		// A push, a comment, a review, a label: anything a person or an agent
		// did to the pull request itself.
		return true
	case state.lastEvaluationActive:
		// CI was pending or failing, and its progress is invisible here.
		return true
	case s.queue.HasActivePRTask(num) != state.lastEvaluationHadTask:
		// Work started or finished since the last look.
		return true
	}
	return false
}

// evaluateAll evaluates the candidates sequentially and returns once every one
// of them has been handled.
//
// With Config.GraphQLPRDetails, the candidates are read ahead a batch at a
// time, just before that batch is evaluated, so what an evaluation decides on
// is no older than the batch's other evaluations.
func (s *Scanner) evaluateAll(ctx context.Context, candidates []*githubv39.Issue) {
	klog.Infof("evaluateAll called with %d PRs", len(candidates))
	batch := len(candidates)
	if s.cfg.GraphQLPRDetails {
		batch = github.PRDetailsBatch
	}
	for start := 0; start < len(candidates); start += batch {
		end := min(start+batch, len(candidates))
		if s.cfg.GraphQLPRDetails {
			s.readAhead(ctx, candidates[start:end])
		}
		for _, prIssue := range candidates[start:end] {
			if ctx.Err() != nil {
				s.details = nil
				return
			}
			s.evaluate(ctx, prIssue)
		}
		s.details = nil
	}
}

// readAhead reads the details of the pull requests about to be evaluated, the
// ones evaluate will read at all (past the number floor and the stop label),
// in one GraphQL query. Any it could not read whole, or at all, are left for
// evaluate to read the REST way.
func (s *Scanner) readAhead(ctx context.Context, candidates []*githubv39.Issue) {
	var nums []int
	for _, prIssue := range candidates {
		num := prIssue.GetNumber()
		if s.cfg.MinNumber > 0 && num < s.cfg.MinNumber {
			continue
		}
		if conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
			continue
		}
		nums = append(nums, num)
	}
	if len(nums) == 0 {
		return
	}
	details, err := s.gh.PullRequestDetails(ctx, nums)
	if err != nil {
		klog.Warningf("Reading the details of %d PRs with GraphQL: %v; the PRs it did not read are read the REST way", len(nums), err)
	}
	s.details = make(map[int]*github.PRDetails, len(details))
	for num, d := range details {
		if !d.Complete {
			klog.Infof("PR #%d has %s; reading it the REST way", num, d.Why)
			continue
		}
		s.details[num] = d
	}
}

// takeDetails returns the read-ahead details of a pull request, once.
func (s *Scanner) takeDetails(num int) *github.PRDetails {
	d := s.details[num]
	delete(s.details, num)
	return d
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
	// Clear any previously recorded evaluation up front so that every early
	// return below leaves the pull request unrecorded for the next fast pass,
	// even when updated_at has not moved since a prior completed evaluation.
	s.state.clearEvaluation(num)
	s.rec = s.startRecording(prIssue)
	defer func() { s.rec.Finish(); s.rec = nil }()

	if conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping PR #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}
	details := s.takeDetails(num)
	var pr *githubv39.PullRequest
	var err error
	if details != nil {
		pr = details.PR
	} else if pr, err = s.gh.GetPullRequest(ctx, num); err != nil {
		s.rec.Gated("reading the PR failed")
		klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
		return
	}
	s.rec.Fill(func(it *rules.Item) { fillPR(it, pr) })

	// A pull request in the merge queue is out of the watcher's hands: pushing
	// to it now would only knock it back out.
	var inMergeQueue bool
	if details != nil {
		inMergeQueue = details.InMergeQueue
	} else {
		inMergeQueue, err = s.gh.IsInMergeQueue(ctx, num)
	}
	if err != nil {
		s.rec.Gated("checking the merge queue failed")
		klog.Errorf("Failed to check if PR #%d is in merge queue: %v", num, err)
	} else if inMergeQueue {
		s.rec.Fill(func(it *rules.Item) { it.MergeQueue = true })
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

	// The issues this pull request closes are wanted by the label sync, the
	// review opt-in check and the review prompt. Resolving them through one
	// shared fetcher is what stops the same issue being fetched three times in
	// the course of a single evaluation.
	refs := newRefIssues(s.gh, pr)
	s.recordCloses(ctx, refs)

	// The closing issues' labels the engine has the PR inherit, then the
	// stop label checked again: it may have been inherited.
	s.actOn(ctx, prIssue, "inherit-labels")
	if conventions.HasStopLabel(prIssue.Labels, s.cfg.TriggerLabel) {
		klog.Infof("Skipping PR #%d after label sync because it has the stop label ('overseer/stop' or '%s/stop')", num, s.cfg.TriggerLabel)
		_ = s.queue.RemovePendingTasksForNumber(num)
		return
	}

	headSHA := pr.GetHead().GetSHA()

	// If a task is already queued or running for this pull request, no second
	// task can be queued and the pull request cannot be ready for a human.
	// Recording the evaluation with lastEvaluationHadTask set lets the fast
	// pass skip the pull request until the task finishes.
	s.recordRuns(num)
	if s.queue.HasActivePRTask(num) {
		s.rec.Fill(func(it *rules.Item) { it.TaskActive = true })
		klog.V(2).Infof("Skipping PR #%d evaluation because a task is already active in the queue", num)
		s.recordEvaluation(prIssue, pr, prCheckAnalysis{})
		return
	}

	var history *prHistory
	if details != nil {
		history = historyFromDetails(details)
	} else {
		history, err = s.fetchHistory(ctx, num)
	}
	if err != nil {
		s.rec.Gated("reading the PR's history failed")
		klog.Errorf("Failed to fetch history for PR #%d: %v", num, err)
		return
	}

	state := s.state.get(num)
	s.recordHistory(pr, history, headSHA)

	// The engine's pause of an inactive PR: nothing more on it.
	if s.actOn(ctx, prIssue, "inactive") {
		return
	}

	isConflicting := pr.Mergeable != nil && !*pr.Mergeable
	commentAnalysis := s.evaluateComments(ctx, pr, history, history.lastCommitTime, state.lastCommentAddressedTime)
	s.rec.Fill(func(it *rules.Item) { it.NewTrustedFeedback = commentAnalysis.hasNewComments })

	var checkAnalysis prCheckAnalysis
	if !isConflicting {
		checkAnalysis, err = s.evaluateChecks(ctx, headSHA)
		if err != nil {
			s.rec.Gated("reading the checks failed")
			klog.Errorf("Failed to evaluate checks for PR #%d: %v", num, err)
			return
		}
		isApproved := isPRApprovedOrLGTM(pr, prIssue, history.reviews)
		s.rec.Fill(func(it *rules.Item) { fillChecks(it, checkAnalysis, isApproved) })
	}

	assignedBot := conventions.AssignedBotUser(prIssue, s.cfg.BotUsers)
	taskAssignee := assignedBot
	if taskAssignee == "" {
		taskAssignee = author
	}
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
		isExplicitlyAssigned: assignedBot != "",
		prURL:                fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.gh.Owner(), s.gh.Repo(), num),
		refIssues:            refs,
	}

	// The pr-care rule the engine decides, at most one (they take the PR):
	// its give-up is its writes, its run its handler's task.
	s.engineCare = s.engineDecide()
	completed := true
	rule := s.engineCareRule()
	switch {
	case rule == "":
	case s.engineOutcome(rule) == rules.OutcomeGiveUp:
		s.actOn(ctx, prIssue, rule)
	case rule == "comments":
		completed = s.handlePRComments(ctx, pc, commentAnalysis)
	case rule == "conflict":
		// Returns without recording the evaluation, so a conflicted pull
		// request is evaluated again every cycle: whether the conflict is
		// gone is answered by GetPullRequest, and nothing about resolving
		// it upstream necessarily moves this pull request's updated_at.
		s.handlePRIterate(ctx, pc)
		return
	case rule == "ci":
		completed = s.handlePRInvestigate(ctx, pc, checkAnalysis)
	case rule == "review":
		completed = s.handlePRReview(ctx, pc, checkAnalysis.checkRuns)
	default:
		completed = s.handleRuleTask(ctx, pc, rule)
	}

	// The engine's ready or ready-unreview: its writes.
	if s.gh.Ready() {
		for _, rule := range []string{"ready", "ready-unreview"} {
			if rules.Decided(s.engineCare, rule) {
				s.actOn(ctx, prIssue, rule)
			}
		}
	}
	// A repository's rules that do not take the PR: their writes.
	s.actOnRuleWrites(ctx, prIssue)
	if !completed {
		return
	}

	// Recorded last, and only here, so that the fast pass may skip this pull
	// request next cycle. Every path that returns before this point left
	// something unresolved and must be looked at again.
	s.recordEvaluation(prIssue, pr, checkAnalysis)
}

// recordEvaluation notes what the evaluation that just finished saw, which is
// what lets the next fast pass decide whether it can be skipped.
//
// The state is re-read rather than carried in, because the handlers that ran in
// between record their own progress against the same pull request and must not
// be overwritten by a stale copy.
func (s *Scanner) recordEvaluation(prIssue *githubv39.Issue, pr *githubv39.PullRequest, checkAnalysis prCheckAnalysis) {
	num := prIssue.GetNumber()
	state := s.state.get(num)
	state.lastEvaluatedUpdatedAt = prIssue.GetUpdatedAt()
	state.lastEvaluationActive = checkAnalysis.hasPending || checkAnalysis.hasFailure || pr.Mergeable == nil
	state.lastEvaluationHadTask = s.queue.HasActivePRTask(num)
	s.state.set(num, state)
}
