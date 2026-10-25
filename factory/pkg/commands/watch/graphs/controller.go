// Package graphs is the watch daemon's issue graph subcontroller
// (design/issue-graph.md). It keeps every open graph parent moving: each
// pass reads the parent's steps and children and applies graph.Sync's
// actions - creating the children of the steps now ready, repairing,
// closing the parent at the end - and keeps its status comment.
//
// A parent is an issue labelled '<trigger>/graph'. A graph started from a
// plan (overseer/implement on a plan with a Breakdown) gets that label from
// here: its start block is posted by the implement task's bot, which may
// not label, so planned issues are swept too, and one found with a start
// block is labelled.
//
// It owns no workers: parents are synced one after another from a single
// goroutine, on a slow sweep and whenever a child is reported closed. The
// work a child starts is the issue scanner's and the dispatcher's.
package graphs

import (
	"context"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph"
)

// DefaultInterval is the delay between sweeps of every open parent. A child
// closing wakes its parent sooner; the sweep catches what no wake reports.
const DefaultInterval = 5 * time.Minute

// Config holds the tuning knobs of a Controller.
type Config struct {
	Interval time.Duration
	// TriggerLabel is the label namespace: parents carry '<trigger>/graph',
	// a child to be fixed gets the trigger label itself.
	TriggerLabel string
	// GitHubLogin is the account the watcher writes as.
	GitHubLogin string
	// Pool are the pool's bots, Members the logins the overseer trusts as
	// members: whose plans and start blocks are trusted, and whose PRs
	// complete a child (the pool's).
	Pool, Members []string
	// MaxDepth is how deep graphs nest; 0 is the default, 3.
	MaxDepth int
	// MinNumber ignores parents below it, as the scanners do.
	MinNumber int
	// DryRun logs each pass's actions and writes nothing.
	DryRun bool
}

// Deps holds the collaborators of a Controller.
type Deps struct {
	GitHub graph.GitHub
	// Paused reports whether the watcher is draining: no pass runs, since
	// creating children is starting work.
	Paused func() bool
}

// Controller syncs the open graph parents.
type Controller struct {
	cfg    Config
	gh     graph.GitHub
	paused func() bool
	// wake carries the parents whose child just closed. It is buffered so
	// that whoever reports one never waits on a pass.
	wake chan int
}

// New constructs a Controller.
func New(cfg Config, deps Deps) *Controller {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Controller{cfg: cfg, gh: deps.GitHub, paused: deps.Paused, wake: make(chan int, 64)}
}

// Run sweeps every parent each interval, and syncs a single parent as soon
// as one of its children is reported closed, until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	c.SyncOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.SyncOnce(ctx)
		case n := <-c.wake:
			c.syncParent(ctx, n)
		}
	}
}

// SyncOnce syncs every open graph parent once, and every planned issue
// not yet one, in case its graph was started since.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.isPaused() {
		klog.V(2).Infof("Skipping issue graphs because the watcher is draining.")
		return
	}
	parents, err := c.listParents(ctx)
	if err != nil {
		klog.Errorf("Listing issue graph parents: %v", err)
	}
	for _, n := range parents {
		if ctx.Err() != nil {
			return
		}
		c.sync(ctx, n)
	}
}

// NudgeLinkedWorkflows wakes the parent of a graph child that just closed,
// so the steps after it start without waiting for the sweep. A nested
// parent closing (its graph done) wakes the level above the same way.
// Anything else is ignored. It never blocks: when the buffer is full, the
// sweep picks the parent up.
func (c *Controller) NudgeLinkedWorkflows(_ context.Context, closed *githubv39.Issue) {
	m, ok := graph.ParseChildMarker(closed.GetBody())
	if !ok {
		return
	}
	select {
	case c.wake <- m.Parent:
		klog.Infof("Issue graph child #%d (step %s) closed; waking parent #%d", closed.GetNumber(), m.Step, m.Parent)
	default:
	}
}

func (c *Controller) syncParent(ctx context.Context, n int) {
	if c.isPaused() {
		return
	}
	c.sync(ctx, n)
}

func (c *Controller) sync(ctx context.Context, n int) {
	if c.cfg.MinNumber > 0 && n < c.cfg.MinNumber {
		return
	}
	p, err := graph.Sync(ctx, c.gh, graph.Options{
		Issue: n, Trigger: c.cfg.TriggerLabel, BotLogin: c.cfg.GitHubLogin,
		Pool: c.cfg.Pool, Members: c.cfg.Members, MaxDepth: c.cfg.MaxDepth, DryRun: c.cfg.DryRun,
		Logf: func(format string, args ...any) {
			klog.Infof("issue graph #%d: "+format, append([]any{n}, args...)...)
		},
	})
	switch {
	case err != nil:
		klog.Errorf("Issue graph #%d: %v", n, err)
		return
	case p.NoGraph:
		return // a planned issue whose graph has not started (or a one-PR plan)
	}
	if p.Result.Phase == graph.PhaseInvalid {
		klog.Infof("Issue graph #%d is invalid: %s", n, p.Result.Reason)
	}
	// A graph started from a plan has no graph label yet: label it, so the
	// sweep and the rules (fix and workflow leave a graph's parent alone)
	// know it.
	issue, err := c.gh.GetIssue(ctx, n)
	if err != nil || conventions.HasGraphLabel(issue.Labels, c.cfg.TriggerLabel) || p.Result.Phase == graph.PhaseSucceeded {
		return
	}
	label := conventions.GraphLabels(c.cfg.TriggerLabel)[len(conventions.GraphLabels(c.cfg.TriggerLabel))-1]
	if c.cfg.DryRun {
		klog.Infof("issue graph #%d: would label it %s", n, label)
		return
	}
	if err := c.gh.AddLabels(ctx, n, []string{label}); err != nil {
		klog.Errorf("Issue graph #%d: labelling it %s: %v", n, label, err)
	}
}

// listParents lists every open issue labelled a graph parent, and every
// open planned issue, in each label's spellings, following pagination.
func (c *Controller) listParents(ctx context.Context) ([]int, error) {
	seen := map[int]bool{}
	var parents []int
	labels := append(conventions.GraphLabels(c.cfg.TriggerLabel), conventions.PlannedLabels(c.cfg.TriggerLabel)...)
	for _, label := range labels {
		opts := &githubv39.IssueListByRepoOptions{Labels: []string{label}, State: "open", ListOptions: githubv39.ListOptions{PerPage: 100}}
		for {
			page, resp, err := c.gh.ListIssues(ctx, opts)
			if err != nil {
				return parents, err
			}
			for _, issue := range page {
				if issue.PullRequestLinks == nil && !seen[issue.GetNumber()] {
					seen[issue.GetNumber()] = true
					parents = append(parents, issue.GetNumber())
				}
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}
	return parents, nil
}

func (c *Controller) isPaused() bool { return c.paused != nil && c.paused() }
