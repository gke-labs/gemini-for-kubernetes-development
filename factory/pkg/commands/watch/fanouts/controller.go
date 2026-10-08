// Package fanouts is the watch daemon's fan-out subcontroller. It keeps every
// open fan-out parent - an issue labelled '<trigger>/fanout' - moving: each
// pass reads the parent's spec and children and applies fanout.Sync's plan,
// creating children, labelling the next window of them for the issue scanner
// to pick up, stopping at checkpoints and closing the parent at the end.
//
// It owns no workers. Parents are synced one after another from a single
// goroutine, on a slow sweep and whenever a child is reported closed, because
// a pass is a handful of GitHub requests and the work it starts is done by the
// issue scanner and the dispatcher, not here.
package fanouts

import (
	"context"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/fanout"
)

// DefaultInterval is the delay between sweeps of every open parent. A child
// closing wakes its parent sooner; the sweep catches what no wake reports,
// such as a pull request closed unmerged or a stop label removed.
const DefaultInterval = 5 * time.Minute

// GitHub is what the controller needs of the repository: what fanout.Sync
// needs, and listing the parents.
type GitHub interface {
	fanout.GitHub
	ListIssues(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error)
}

// Config holds the tuning knobs of a Controller.
type Config struct {
	// Interval is the delay between sweeps.
	Interval time.Duration
	// TriggerLabel is the label namespace: parents carry '<trigger>/fanout',
	// and the children the fan-out starts get the trigger label itself.
	TriggerLabel string
	// GitHubLogin is the account the watcher writes as; its comments are the
	// progress comment and a trusted source of the spec.
	GitHubLogin string
	// MinNumber ignores parents below it, as the scanners do. Zero syncs all.
	MinNumber int
	// DryRun logs each pass's plan and writes nothing.
	DryRun bool
}

// Deps holds the collaborators of a Controller.
type Deps struct {
	GitHub GitHub
	// Paused reports whether the watcher is draining, in which case no pass
	// runs: labelling children is starting work.
	Paused func() bool
}

// Controller syncs the open fan-out parents.
type Controller struct {
	cfg    Config
	gh     GitHub
	paused func() bool
	// wake carries the parents whose child just closed. It is buffered so the
	// reconciler that reports a closure never waits on a pass.
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

// SyncOnce syncs every open fan-out parent once.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.isPaused() {
		klog.V(2).Infof("Skipping fan-outs because the watcher is draining.")
		return
	}
	parents, err := c.listParents(ctx)
	if err != nil {
		klog.Errorf("Listing fan-out parents: %v", err)
	}
	for _, p := range parents {
		if ctx.Err() != nil {
			return
		}
		c.sync(ctx, p)
	}
}

// NudgeLinkedWorkflows wakes the parent of a fan-out child that just closed,
// so the next window is labelled without waiting for the sweep. Anything else
// is not a child and is ignored. It never blocks: when the wake buffer is
// full, the sweep picks the parent up.
func (c *Controller) NudgeLinkedWorkflows(_ context.Context, closed *githubv39.Issue) {
	parent, _, _, ok := fanout.ParseMarker(closed.GetBody())
	if !ok {
		return
	}
	select {
	case c.wake <- parent:
		klog.Infof("Fan-out child #%d closed; waking parent #%d", closed.GetNumber(), parent)
	default:
	}
}

func (c *Controller) syncParent(ctx context.Context, n int) {
	if c.isPaused() {
		return
	}
	issue, err := c.gh.GetIssue(ctx, n)
	if err != nil {
		klog.Errorf("Fetching fan-out parent #%d: %v", n, err)
		return
	}
	// A marker names its parent, but only the label makes it one: a parent
	// unlabelled by a person has been taken out of the fan-out's hands.
	if issue.GetState() != "open" || !conventions.HasFanoutLabel(issue.Labels, c.cfg.TriggerLabel) {
		return
	}
	c.sync(ctx, issue)
}

func (c *Controller) sync(ctx context.Context, parent *githubv39.Issue) {
	n := parent.GetNumber()
	if c.cfg.MinNumber > 0 && n < c.cfg.MinNumber {
		return
	}
	res, err := fanout.Sync(ctx, c.gh, fanout.SyncOptions{
		Issue:        n,
		Parent:       parent,
		TriggerLabel: c.cfg.TriggerLabel,
		BotLogin:     c.cfg.GitHubLogin,
		DryRun:       c.cfg.DryRun,
		Logf: func(format string, args ...any) {
			klog.Infof("fan-out #%d: "+format, append([]any{n}, args...)...)
		},
	})
	switch {
	case err != nil:
		klog.Errorf("Fan-out #%d: %v", n, err)
	case res.NoSpec:
		klog.V(2).Infof("Fan-out #%d has no spec yet", n)
	case res.SpecError != nil:
		klog.V(2).Infof("Fan-out #%d spec does not parse: %v", n, res.SpecError)
	case res.Plan.CloseParent && !c.cfg.DryRun:
		klog.Infof("Fan-out #%d is done", n)
	}
}

// listParents lists every open issue labelled as a fan-out parent, in each of
// the label's spellings, following pagination.
func (c *Controller) listParents(ctx context.Context) ([]*githubv39.Issue, error) {
	seen := map[int]bool{}
	var parents []*githubv39.Issue
	for _, label := range conventions.FanoutLabels(c.cfg.TriggerLabel) {
		opts := &githubv39.IssueListByRepoOptions{
			Labels:      []string{label},
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		for {
			page, resp, err := c.gh.ListIssues(ctx, opts)
			if err != nil {
				return parents, err
			}
			for _, issue := range page {
				if issue.PullRequestLinks == nil && !seen[issue.GetNumber()] {
					seen[issue.GetNumber()] = true
					parents = append(parents, issue)
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

func (c *Controller) isPaused() bool {
	return c.paused != nil && c.paused()
}
