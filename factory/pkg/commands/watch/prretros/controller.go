// Package prretros is the watch daemon's per-PR retro
// (design/retro-per-pr.md): a maintainer labels one of overseer's PRs
// <trigger>/retro, and once it is closed, and its agent record is saved,
// the watch runs a retro on that PR alone - pkg/commands' runner stages
// it and runs the retro-pr recipe - and says on the PR how it went: the
// retro PR it opened, why it found nothing to change, or that it failed.
// Then it takes the label off.
//
// It is not a watch rule: rules act on open items, and a retro waits for
// the close. Each pass lists the repository's PRs carrying the label,
// open or closed; taking it off when a retro ends keeps that list to the
// pending ones.
//
// What it has done is kept per PR and per application of the label (the
// labeled event) in the queue directory (pr_retro_state.json): a retro
// running when the watch restarts is picked up, not started again, and a
// finished one never runs again, however long its label takes to come
// off. Adding the label again is a new application, and a new retro.
package prretros

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/retro"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

const (
	// DefaultInterval is how often the labelled PRs are listed.
	DefaultInterval = 10 * time.Minute
	// DefaultSettle is how long after its outcome record is first seen a
	// PR's archived traces are given to become readable.
	DefaultSettle = 10 * time.Minute
	// DefaultRecordWait is how long after its close a PR's outcome record
	// is waited for before the retro runs on what there is.
	DefaultRecordWait = 2 * time.Hour
	// DefaultMaxRunning is how many retros run at once, at most.
	DefaultMaxRunning = 3
	// MaxTries is how many times in a row a retro is tried when it fails
	// in a way a later try may mend (retro.ErrTryAgain) before the
	// failure is reported.
	MaxTries = 5
	// StateFile is the queue directory's file of the retros started.
	StateFile = "pr_retro_state.json"
	// LabelSuffix is the label's, after the trigger: <trigger>/retro.
	LabelSuffix = "/retro"
)

// GitHub is what the controller reads and writes, as the watcher's own
// account.
type GitHub interface {
	LabelledItems(ctx context.Context, label string) ([]*githubv39.Issue, error)
	LastLabeled(ctx context.Context, number int, label string) (*githubv39.IssueEvent, error)
	IsCollaborator(ctx context.Context, login string) (bool, error)
	GetPullRequest(ctx context.Context, number int) (*githubv39.PullRequest, error)
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	ListReviews(ctx context.Context, number int) ([]*githubv39.PullRequestReview, error)
	ListAllReviewComments(ctx context.Context, number int) ([]*githubv39.PullRequestComment, error)
	AddComment(ctx context.Context, number int, body string) error
	RemoveLabel(ctx context.Context, number int, label string) error
	AddAssignees(ctx context.Context, number int, assignees []string) error
}

// Config is the controller's configuration.
type Config struct {
	// Interval, Settle and RecordWait are DefaultInterval, DefaultSettle
	// and DefaultRecordWait if not positive.
	Interval, Settle, RecordWait time.Duration
	// MaxRunning is DefaultMaxRunning if not positive.
	MaxRunning   int
	TriggerLabel string
	// GitHubLogin is the watcher's own account.
	GitHubLogin string
	// Pool are the pool's bots: a PR one of them opened is overseer's.
	Pool []string
	// TrustedLogins are maintainers whatever GitHub says of them.
	TrustedLogins []string
	// StateDir is where StateFile is kept.
	StateDir string
	// DryRun says what it would do, and does nothing.
	DryRun bool
}

// Deps holds the controller's collaborators.
type Deps struct {
	GitHub GitHub
	// Outcome reports whether PR pr's outcome record is saved; an error
	// is a collector that could not tell. Nil, there is no collector: a
	// PR's retro runs once it is closed, on what GitHub has.
	Outcome func(ctx context.Context, pr int) (bool, error)
	// Run runs one retro, for as long as it takes (pkg/commands).
	Run func(ctx context.Context, run retro.PRRun) (retro.PRResult, error)
	// Paused reports whether the watcher is draining: no retro starts.
	Paused func() bool
	// CanStart reports whether a task may start now (--max-pending).
	CanStart func(ctx context.Context) bool
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// The phases of a label application.
const (
	phaseWaiting   = "waiting"   // for the PR to close, or for its data
	phaseRunning   = "running"   // the retro is running
	phaseReporting = "reporting" // it ended; what came of it is to be said on the PR
	phaseDone      = "done"      // said; only the label may be left to take off
)

// The outcomes of a label application.
const (
	outcomeLessons       = "lessons"
	outcomeNoLessons     = "no-lessons"
	outcomeNothing       = "nothing"
	outcomeFailed        = "failed"
	outcomeNotOverseers  = "not-overseers"
	outcomeNotMaintainer = "not-maintainer"
)

// prState is what was done for a PR's latest label application.
type prState struct {
	// Application is the labeled event's id.
	Application string    `json:"application"`
	LabelledAt  time.Time `json:"labelledAt"`
	LabelledBy  string    `json:"labelledBy,omitempty"`
	Phase       string    `json:"phase"`
	// RecordSeenAt is when the PR's outcome record was first seen.
	RecordSeenAt time.Time `json:"recordSeenAt,omitzero"`
	StartedAt    time.Time `json:"startedAt,omitzero"`
	// Started is whether its task started, on some try: a later try picks
	// that run up. User is the pool account it started under.
	Started bool   `json:"started,omitempty"`
	User    string `json:"user,omitempty"`
	// Tries counts the tries in a row that failed in a way a later one
	// may mend.
	Tries      int       `json:"tries,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	// NotesSince is the end of the PR's previous retro: its notes before
	// that were answered then.
	NotesSince time.Time `json:"notesSince,omitzero"`
	// What came of it, kept until it is said on the PR.
	Outcome   string   `json:"outcome,omitempty"`
	RetroPR   string   `json:"retroPR,omitempty"`
	Comment   string   `json:"comment,omitempty"`
	Error     string   `json:"error,omitempty"`
	Session   string   `json:"session,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	// LabelRemoved is whether the label came off.
	LabelRemoved bool `json:"labelRemoved,omitempty"`
}

// state is StateFile: per PR number.
type state struct {
	PRs map[string]*prState `json:"prs"`
}

// Controller runs the per-PR retros.
type Controller struct {
	cfg   Config
	deps  Deps
	label string
	path  string

	mu      sync.Mutex
	st      *state
	running map[int]bool
	wg      sync.WaitGroup
}

// New constructs a Controller.
func New(cfg Config, deps Deps) *Controller {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Settle <= 0 {
		cfg.Settle = DefaultSettle
	}
	if cfg.RecordWait <= 0 {
		cfg.RecordWait = DefaultRecordWait
	}
	if cfg.MaxRunning <= 0 {
		cfg.MaxRunning = DefaultMaxRunning
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Controller{
		cfg: cfg, deps: deps,
		label:   Label(cfg.TriggerLabel),
		path:    filepath.Join(cfg.StateDir, StateFile),
		running: map[int]bool{},
	}
}

// Label is the label that asks for a PR's retro: <trigger>/retro.
func Label(trigger string) string {
	if trigger == "" {
		trigger = "overseer"
	}
	return trigger + LabelSuffix
}

// Run lists the labelled PRs every interval until ctx is cancelled, then
// waits for the retros under way to return.
func (c *Controller) Run(ctx context.Context) error {
	defer c.wg.Wait()
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	c.SyncOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.SyncOnce(ctx)
		}
	}
}

// Wait waits for the retros under way to return.
func (c *Controller) Wait() { c.wg.Wait() }

// SyncOnce moves each labelled PR's retro on by what it can: refuses a
// label it may not act on, starts a retro that is due, says how one that
// ended went, and takes the label off one that is done.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.deps.Paused != nil && c.deps.Paused() {
		return
	}
	if err := c.load(); err != nil {
		klog.Errorf("PR retro: %v", err)
		return
	}
	items, err := c.deps.GitHub.LabelledItems(ctx, c.label)
	if err != nil {
		klog.Errorf("PR retro: %v", err)
		return
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		if !it.IsPullRequest() {
			continue // the label is ignored on issues
		}
		c.sync(ctx, it)
	}
}

// sync moves one labelled PR on.
func (c *Controller) sync(ctx context.Context, it *githubv39.Issue) {
	n := it.GetNumber()
	if c.isRunning(n) {
		return
	}
	ev, err := c.deps.GitHub.LastLabeled(ctx, n, c.label)
	if err != nil || ev == nil {
		if err != nil {
			klog.Warningf("PR retro: #%d: %v", n, err)
		}
		return
	}
	app := strconv.FormatInt(ev.GetID(), 10)

	c.mu.Lock()
	ps := c.st.PRs[strconv.Itoa(n)]
	if ps == nil || ps.Application != app {
		next := &prState{Application: app, LabelledAt: ev.GetCreatedAt().UTC(), LabelledBy: ev.GetActor().GetLogin(), Phase: phaseWaiting}
		if ps != nil {
			next.NotesSince = ps.NotesSince
			if ps.Outcome == outcomeLessons || ps.Outcome == outcomeNoLessons {
				next.NotesSince = ps.FinishedAt
			}
		}
		ps = next
		c.st.PRs[strconv.Itoa(n)] = ps
		c.saveLocked()
	}
	phase := ps.Phase
	c.mu.Unlock()

	switch phase {
	case phaseDone:
		c.removeLabel(ctx, n, ps)
		return
	case phaseReporting:
		c.report(ctx, n, ps)
		return
	case phaseRunning:
		// Started before a restart: picked up (the runner finds the run
		// by its key), whatever the limits say - it was counted then.
		c.start(ctx, n, ps)
		return
	}

	// Waiting.
	if !c.inPool(it.GetUser().GetLogin()) {
		c.refuse(ctx, n, ps, outcomeNotOverseers)
		return
	}
	ok, err := c.maintainer(ctx, ps.LabelledBy)
	if err != nil {
		klog.Warningf("PR retro: #%d: %v", n, err)
		return
	}
	if !ok {
		c.refuse(ctx, n, ps, outcomeNotMaintainer)
		return
	}
	if it.GetState() != "closed" {
		return // nothing happens until it closes
	}
	if !c.ready(ctx, n, ps, it.GetClosedAt()) {
		return
	}
	if c.runningCount() >= c.cfg.MaxRunning || (c.deps.CanStart != nil && !c.deps.CanStart(ctx)) {
		klog.V(2).Infof("PR retro: #%d is due; waiting for room to run it", n)
		return
	}
	c.start(ctx, n, ps)
}

// ready reports whether PR n's data is ready: its outcome record saved,
// and the settling time since passed; or, without one, the wait for it
// over. A collector that cannot tell keeps it waiting: falling back while
// it is down would lose the traces too.
func (c *Controller) ready(ctx context.Context, n int, ps *prState, closedAt time.Time) bool {
	if c.deps.Outcome == nil {
		return true
	}
	now := c.deps.Now().UTC()
	has, err := c.deps.Outcome(ctx, n)
	if err != nil {
		klog.Warningf("PR retro: #%d: cannot tell whether its agent record is saved (%v); waiting", n, err)
		return false
	}
	waited := !closedAt.IsZero() && !now.Before(closedAt.Add(c.cfg.RecordWait))
	if !has {
		if waited {
			klog.Infof("PR retro: #%d has no agent record %s after it closed; running on what there is", n, c.cfg.RecordWait)
		}
		return waited
	}
	c.mu.Lock()
	if ps.RecordSeenAt.IsZero() {
		ps.RecordSeenAt = now
		c.saveLocked()
	}
	seen := ps.RecordSeenAt
	c.mu.Unlock()
	// A PR labelled long after it closed has had its traces settle.
	return waited || !now.Before(seen.Add(c.cfg.Settle))
}

// start runs PR n's retro in the background.
func (c *Controller) start(ctx context.Context, n int, ps *prState) {
	if c.cfg.DryRun {
		klog.Infof("PR retro: would run the retro of #%d (dry run)", n)
		return
	}
	c.mu.Lock()
	if c.running[n] {
		c.mu.Unlock()
		return
	}
	c.running[n] = true
	// A retro running before a restart, or whose task started on an
	// earlier try, may have a run to pick up.
	resume := ps.Phase == phaseRunning || ps.Started
	if ps.Phase != phaseRunning {
		ps.Phase = phaseRunning
		if ps.StartedAt.IsZero() {
			ps.StartedAt = c.deps.Now().UTC()
		}
		c.saveLocked()
	}
	c.mu.Unlock()
	klog.Infof("PR retro: running the retro of #%d (label application %s)", n, ps.Application)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.running, n)
			c.mu.Unlock()
		}()
		run, err := c.prepare(ctx, n, ps)
		var res retro.PRResult
		if err != nil {
			err = retro.TryAgain(err)
		} else {
			run.Resume = resume
			res, err = c.deps.Run(ctx, run)
		}
		if ctx.Err() != nil {
			// The watch is stopping: the retro runs on in its sandbox,
			// for the next watch to pick up.
			klog.Infof("PR retro: #%d: stopped waiting for its retro (%v)", n, ctx.Err())
			return
		}
		if errors.Is(err, retro.ErrTryAgain) && c.tryAgain(n, ps, err) {
			return
		}
		c.finish(ctx, n, ps, run, res, err)
	}()
}

// tryAgain puts PR n's retro back to waiting after a try that failed in a
// way a later one may mend, unless it has been tried MaxTries times in a
// row; false, the failure is reported.
func (c *Controller) tryAgain(n int, ps *prState, err error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ps.Tries++
	if ps.Tries >= MaxTries {
		klog.Errorf("PR retro: #%d: %v; tried %d times, giving up", n, err, ps.Tries)
		return false
	}
	ps.Phase = phaseWaiting
	c.saveLocked()
	klog.Warningf("PR retro: #%d: %v (try %d of %d)", n, err, ps.Tries, MaxTries)
	return true
}

// prepare reads what the retro is given of PR n from GitHub: the PR, its
// notes since the last retro, and its human feedback.
func (c *Controller) prepare(ctx context.Context, n int, ps *prState) (retro.PRRun, error) {
	gh := c.deps.GitHub
	pr, err := gh.GetPullRequest(ctx, n)
	if err != nil {
		return retro.PRRun{}, err
	}
	info := retro.PRInfo{
		Number: n, URL: pr.GetHTMLURL(), Title: pr.GetTitle(), Body: pr.GetBody(),
		Author: pr.GetUser().GetLogin(), State: "closed", ClosedAt: pr.GetClosedAt().UTC(),
		Base: pr.GetBase().GetRef(), BaseSHA: pr.GetBase().GetSHA(), HeadSHA: pr.GetHead().GetSHA(),
	}
	if pr.GetMerged() {
		info.State = "merged"
	}
	for _, a := range pr.Assignees {
		if c.human(a) {
			info.Assignees = append(info.Assignees, a.GetLogin())
		}
	}
	comments, err := gh.ListIssueComments(ctx, n)
	if err != nil {
		return retro.PRRun{}, fmt.Errorf("reading #%d's comments: %w", n, err)
	}
	c.mu.Lock()
	since, user := ps.NotesSince, ps.User
	c.mu.Unlock()
	var notes []retro.Note
	var feedback []tokenusage.FeedbackItem
	addNote := func(author, url, body string, at time.Time) bool {
		text, ok := retro.NoteText(body)
		if !ok {
			return false
		}
		if at.After(since) {
			notes = append(notes, retro.Note{Author: author, At: at.UTC(), URL: url, Body: text})
		}
		return true
	}
	for _, cm := range comments {
		if !c.maintainerComment(cm.GetUser(), cm.GetAuthorAssociation()) {
			continue
		}
		if addNote(cm.GetUser().GetLogin(), cm.GetHTMLURL(), cm.GetBody(), cm.GetCreatedAt()) {
			continue
		}
		feedback = append(feedback, tokenusage.FeedbackItem{Author: cm.GetUser().GetLogin(), Kind: "comment", Body: cm.GetBody(), At: cm.GetCreatedAt().UTC().Format(time.RFC3339)})
	}
	// The PR's reviews and inline comments: /retro notes, and feedback for
	// when the collector has no record of it.
	reviews, err := gh.ListReviews(ctx, n)
	if err != nil {
		return retro.PRRun{}, fmt.Errorf("reading #%d's reviews: %w", n, err)
	}
	reviewSubmittedAt := make(map[int64]time.Time, len(reviews))
	for _, r := range reviews {
		if id := r.GetID(); id != 0 {
			reviewSubmittedAt[id] = r.GetSubmittedAt()
		}
		if !c.maintainerComment(r.GetUser(), r.GetAuthorAssociation()) || (r.GetBody() == "" && r.GetState() == "COMMENTED") {
			continue
		}
		if addNote(r.GetUser().GetLogin(), r.GetHTMLURL(), r.GetBody(), r.GetSubmittedAt()) {
			continue
		}
		feedback = append(feedback, tokenusage.FeedbackItem{Author: r.GetUser().GetLogin(), Kind: "review", State: r.GetState(), Body: r.GetBody(), At: r.GetSubmittedAt().UTC().Format(time.RFC3339), Head: r.GetCommitID()})
	}
	inline, err := gh.ListAllReviewComments(ctx, n)
	if err != nil {
		return retro.PRRun{}, fmt.Errorf("reading #%d's review comments: %w", n, err)
	}
	for _, ic := range inline {
		if !c.maintainerComment(ic.GetUser(), ic.GetAuthorAssociation()) {
			continue
		}
		at := conventions.ReviewCommentTime(ic, reviewSubmittedAt[ic.GetPullRequestReviewID()])
		if addNote(ic.GetUser().GetLogin(), ic.GetHTMLURL(), ic.GetBody(), at) {
			continue
		}
		line := ic.GetLine()
		if line == 0 {
			line = ic.GetOriginalLine()
		}
		feedback = append(feedback, tokenusage.FeedbackItem{Author: ic.GetUser().GetLogin(), Kind: "inline", Path: ic.GetPath(), Line: line, InReplyTo: ic.GetInReplyTo(), Body: ic.GetBody(), At: ic.GetCreatedAt().UTC().Format(time.RFC3339), Head: ic.GetCommitID()})
	}
	slices.SortStableFunc(notes, func(a, b retro.Note) int { return a.At.Compare(b.At) })
	slices.SortStableFunc(feedback, func(a, b tokenusage.FeedbackItem) int { return strings.Compare(a.At, b.At) })

	var labels []string
	if c.cfg.TriggerLabel != "" {
		labels = []string{c.cfg.TriggerLabel}
	}
	return retro.PRRun{
		PR: info, Notes: notes, Feedback: feedback,
		Key:    fmt.Sprintf("%d-%s", n, ps.Application),
		Labels: labels,
		User:   user,
		Started: func(st *retro.PRStaged, session, startedUser string) {
			c.mu.Lock()
			ps.Session, ps.User, ps.Started = session, startedUser, true
			c.saveLocked()
			c.mu.Unlock()
			if err := c.comment(ctx, n, ps, "start", startComment(st, session)); err != nil {
				klog.Warningf("PR retro: #%d: %v", n, err)
			}
		},
	}, nil
}

// finish records how PR n's retro ended, and says so on the PR.
func (c *Controller) finish(ctx context.Context, n int, ps *prState, run retro.PRRun, res retro.PRResult, runErr error) {
	c.mu.Lock()
	ps.FinishedAt = c.deps.Now().UTC()
	if res.Session != "" {
		ps.Session = res.Session
	}
	switch {
	case runErr != nil:
		// Given up on after MaxTries: its message no longer says it is
		// tried again.
		ps.Outcome, ps.Error = outcomeFailed, strings.TrimPrefix(runErr.Error(), retro.ErrTryAgain.Error()+": ")
	case res.Staged != nil && res.Staged.Empty():
		ps.Outcome = outcomeNothing
	case res.PRURL != "":
		ps.Outcome, ps.RetroPR = outcomeLessons, res.PRURL
		ps.Assignees = c.assignees(run)
	default:
		ps.Outcome, ps.Comment = outcomeNoLessons, res.Comment
	}
	ps.Phase = phaseReporting
	c.saveLocked()
	c.mu.Unlock()
	if runErr != nil {
		klog.Errorf("PR retro: #%d: %v", n, runErr)
	}
	klog.Infof("PR retro: #%d: %s", n, ps.Outcome)
	c.report(ctx, n, ps)
}

// report says on PR n how its retro went - assigning the retro PR first,
// when there is one - then takes the label off. A write that fails is
// tried again on a later pass; none is made twice.
func (c *Controller) report(ctx context.Context, n int, ps *prState) {
	c.mu.Lock()
	p := *ps
	c.mu.Unlock()
	if p.Outcome == outcomeLessons && len(p.Assignees) > 0 {
		if num, ok := prNumber(p.RetroPR); ok && !c.cfg.DryRun {
			if err := c.deps.GitHub.AddAssignees(ctx, num, p.Assignees); err != nil {
				klog.Warningf("PR retro: assigning %s to %s: %v", p.RetroPR, strings.Join(p.Assignees, ", "), err)
			}
		}
	}
	if err := c.comment(ctx, n, ps, "result", c.resultComment(p)); err != nil {
		klog.Warningf("PR retro: #%d: %v; trying again on the next pass", n, err)
		return
	}
	c.mu.Lock()
	ps.Phase = phaseDone
	c.saveLocked()
	c.mu.Unlock()
	c.removeLabel(ctx, n, ps)
}

// refuse takes the label off PR n, saying once why the retro will not
// run.
func (c *Controller) refuse(ctx context.Context, n int, ps *prState, outcome string) {
	c.mu.Lock()
	ps.Outcome, ps.Phase, ps.FinishedAt = outcome, phaseReporting, c.deps.Now().UTC()
	c.saveLocked()
	c.mu.Unlock()
	klog.Infof("PR retro: #%d: not running its retro: %s", n, outcome)
	c.report(ctx, n, ps)
}

// removeLabel takes the label off PR n, once its retro is done; a failure
// is tried again on a later pass.
func (c *Controller) removeLabel(ctx context.Context, n int, ps *prState) {
	if c.cfg.DryRun {
		klog.Infof("PR retro: would take %s off #%d (dry run)", c.label, n)
		return
	}
	if err := c.deps.GitHub.RemoveLabel(ctx, n, c.label); err != nil && !github.IsNotFound(err) {
		klog.Warningf("PR retro: taking %s off #%d: %v; trying again on the next pass", c.label, n, err)
		return
	}
	c.mu.Lock()
	ps.LabelRemoved = true
	c.saveLocked()
	c.mu.Unlock()
}

// comment posts body on PR n, marked as the label application's comment
// of kind, unless one is there already.
func (c *Controller) comment(ctx context.Context, n int, ps *prState, kind, body string) error {
	marker := fmt.Sprintf("<!-- factory:pr-retro application=%s kind=%s -->", ps.Application, kind)
	if c.cfg.DryRun {
		klog.Infof("PR retro: would comment on #%d (dry run):\n%s", n, body)
		return nil
	}
	comments, err := c.deps.GitHub.ListIssueComments(ctx, n)
	if err != nil {
		return fmt.Errorf("reading #%d's comments: %w", n, err)
	}
	for _, cm := range comments {
		if strings.Contains(cm.GetBody(), marker) {
			return nil
		}
	}
	return c.deps.GitHub.AddComment(ctx, n, body+"\n\n"+marker)
}

// startComment says the retro started, with what.
func startComment(st *retro.PRStaged, session string) string {
	var b strings.Builder
	b.WriteString("The retro of this PR has started")
	if session != "" {
		fmt.Fprintf(&b, " ([session](%s))", session)
	}
	b.WriteString(".")
	if st != nil {
		had, missing := st.Had()
		if len(had) > 0 {
			fmt.Fprintf(&b, " It works from %s.", strings.Join(had, ", "))
		}
		if len(missing) > 0 {
			fmt.Fprintf(&b, " It has no %s.", strings.Join(missing, ", no "))
		}
	}
	return b.String()
}

// resultComment says how the retro went.
func (c *Controller) resultComment(p prState) string {
	again := fmt.Sprintf("The `%s` label is off; add it again for another retro.", c.label)
	session := ""
	if p.Session != "" {
		session = fmt.Sprintf(" ([session](%s))", p.Session)
	}
	switch p.Outcome {
	case outcomeLessons:
		s := fmt.Sprintf("The retro of this PR found lessons%s: %s changes the agents' instructions", session, p.RetroPR)
		if len(p.Assignees) > 0 {
			s += ", assigned to " + strings.Join(p.Assignees, ", ")
		}
		return s + ".\n\n" + again
	case outcomeNoLessons:
		s := fmt.Sprintf("The retro of this PR found nothing that calls for a change to the agents' instructions%s.", session)
		if p.Comment != "" {
			s += "\n\n" + p.Comment
		}
		return s + "\n\n" + again
	case outcomeNothing:
		return "There is nothing to retro on: this PR has no agent record, no archived agent tasks, no human feedback and no `/retro` notes.\n\n" + again
	case outcomeNotOverseers:
		return fmt.Sprintf("Retros run only on overseer's own PRs, opened by one of its bots; this one is not. The `%s` label is off.", c.label)
	case outcomeNotMaintainer:
		return fmt.Sprintf("Only maintainers can ask for a retro. The `%s` label is off.", c.label)
	default:
		msg := p.Error
		if r := []rune(msg); len(r) > 500 {
			msg = string(r[:500]) + "…"
		}
		return fmt.Sprintf("The retro of this PR failed%s:\n\n```\n%s\n```\n\n%s", session, msg, again)
	}
}

// assignees are whom a retro PR is assigned to: the PR's human assignees
// and the authors of the notes the retro read, each once.
func (c *Controller) assignees(run retro.PRRun) []string {
	var out []string
	add := func(l string) {
		if l == "" || c.inPool(l) || strings.EqualFold(l, c.cfg.GitHubLogin) || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			return
		}
		out = append(out, l)
	}
	for _, a := range run.PR.Assignees {
		add(a)
	}
	for _, nt := range run.Notes {
		add(nt.Author)
	}
	return out
}

// maintainer reports whether login may ask for a retro: the watcher, a
// trusted login, or one of the repository's collaborators (its owners and
// members with access included). A pool bot is not.
func (c *Controller) maintainer(ctx context.Context, login string) (bool, error) {
	switch {
	case login == "":
		return false, nil
	case strings.EqualFold(login, c.cfg.GitHubLogin), containsFold(c.cfg.TrustedLogins, login):
		return true, nil
	case c.inPool(login):
		return false, nil
	}
	return c.deps.GitHub.IsCollaborator(ctx, login)
}

// maintainerComment reports whether a comment's author is a human
// maintainer: the repository's owners, members and collaborators, and
// the trusted logins - not an app, the watcher or a pool bot.
func (c *Controller) maintainerComment(u *githubv39.User, association string) bool {
	return c.human(u) && github.IsTrustedAuthor(u, association, c.cfg.TrustedLogins)
}

// human reports whether u is a person: not an app, the watcher or a pool
// bot.
func (c *Controller) human(u *githubv39.User) bool {
	if u == nil || u.GetLogin() == "" || strings.EqualFold(u.GetType(), "Bot") {
		return false
	}
	return !c.inPool(u.GetLogin()) && !strings.EqualFold(u.GetLogin(), c.cfg.GitHubLogin)
}

func (c *Controller) inPool(login string) bool { return containsFold(c.cfg.Pool, login) }

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(l string) bool { return strings.EqualFold(l, s) })
}

var prURLRE = regexp.MustCompile(`/pull/(\d+)/?$`)

// prNumber is a PR URL's number.
func prNumber(url string) (int, bool) {
	m := prURLRE.FindStringSubmatch(url)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

func (c *Controller) isRunning(n int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running[n]
}

func (c *Controller) runningCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.running)
}

// load reads the state, once.
func (c *Controller) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st != nil {
		return nil
	}
	st := &state{PRs: map[string]*prState{}}
	data, err := os.ReadFile(c.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, st); err != nil {
			return fmt.Errorf("reading %s: %w", c.path, err)
		}
		if st.PRs == nil {
			st.PRs = map[string]*prState{}
		}
	}
	c.st = st
	return nil
}

// saveLocked writes the state; c.mu is held. A failure is logged: the
// state in memory goes on, and the next save tries again.
func (c *Controller) saveLocked() {
	if c.cfg.DryRun {
		return
	}
	data, err := json.MarshalIndent(c.st, "", "  ")
	if err == nil {
		tmp := c.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o644); err == nil {
			err = os.Rename(tmp, c.path)
		}
	}
	if err != nil {
		klog.Errorf("PR retro: saving %s: %v", c.path, err)
	}
}
