// Package sandboxrules is the watch's rules on sandboxes (rules.OnSandbox):
// on a sweep of its own, and as soon as a task in a sandbox ends, it reads
// each awake sandbox's facts - its workspace disk from the sandbox's daemon
// (GET /v1/disk), whether a task of it runs - records the disk's use on the
// sandbox, and queues the recipe a rule decides (a workspace's cleanup), to
// run in that sandbox (factory recipe <name> --sandbox).
package sandboxrules

import (
	"context"
	"encoding/json"
	"time"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/diskcare"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// DefaultInterval is how often every awake sandbox is checked; a sandbox
// whose task just ended is checked at once (Wake).
const DefaultInterval = 10 * time.Minute

// DiskAnnotation is the sandbox annotation that carries its workspace's
// space as last measured: diskcare.Usage, as JSON. The board shows it.
const DiskAnnotation = "factory.gemini.google.com/disk"

// Sandbox is a sandbox the rules look at: awake (its daemon can be asked),
// the repository it holds, and the account it belongs to.
type Sandbox struct {
	Name    string
	RepoURL string
	User    string
}

// Queue is the watch's task queue, as the rules on sandboxes use it.
type Queue interface {
	TaskExists(filename string) bool
	Enqueue(filename string, task *api.QueueTask) error
	GetProcessedTask(filename string) *api.QueueTask
	EndedRuns(filename string) []api.EndedRun
}

// Deps are the controller's collaborators.
type Deps struct {
	// Awake lists the sandboxes to check: awake, not the warm one, not the
	// overseer's own.
	Awake func(ctx context.Context) ([]Sandbox, error)
	// Get is one sandbox by name, false when it is not one to check.
	Get func(ctx context.Context, name string) (Sandbox, bool)
	// Busy is whether a task runs in the sandbox.
	Busy func(ctx context.Context, name string) bool
	// Disk is the sandbox's workspace space, as its daemon measures it.
	Disk func(ctx context.Context, name string) (diskcare.Usage, error)
	// Annotate records annotations on the sandbox.
	Annotate func(ctx context.Context, name string, annotations map[string]string) error
	// Control is the rules, and Env what a decision is made against.
	Control func() *rules.Control
	Env     func() rules.Env
	Queue   Queue
	// Paused reports whether the watcher is draining: nothing is queued.
	Paused func() bool
}

// Controller checks sandboxes against the rules on sandboxes.
type Controller struct {
	interval time.Duration
	deps     Deps
	wake     chan string
}

// New constructs a Controller that checks every interval (DefaultInterval
// when not positive).
func New(interval time.Duration, deps Deps) *Controller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Controller{interval: interval, deps: deps, wake: make(chan string, 64)}
}

// Run checks every awake sandbox now and every interval, and a woken one
// at once, until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.SyncOnce(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.SyncOnce(ctx)
		case name := <-c.wake:
			if sb, ok := c.deps.Get(ctx, name); ok {
				c.sync(ctx, sb)
			}
		}
	}
}

// Wake has sandbox name checked at once - a task in it just ended - without
// blocking: with the buffer full, the next sweep checks it.
func (c *Controller) Wake(name string) {
	if c == nil || name == "" {
		return
	}
	select {
	case c.wake <- name:
	default:
	}
}

// SyncOnce checks every awake sandbox.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.paused() || !c.hasRules() {
		return
	}
	sandboxes, err := c.deps.Awake(ctx)
	if err != nil {
		klog.Warningf("Rules on sandboxes: listing sandboxes: %v", err)
		return
	}
	for _, sb := range sandboxes {
		if ctx.Err() != nil {
			return
		}
		c.sync(ctx, sb)
	}
}

func (c *Controller) paused() bool { return c.deps.Paused != nil && c.deps.Paused() }

// recipeRules are the rules on sandboxes that run a recipe.
func (c *Controller) recipeRules() []*rules.Rule {
	control := c.deps.Control()
	if control == nil {
		return nil
	}
	var out []*rules.Rule
	for _, r := range control.Set() {
		if r.On == rules.OnSandbox && r.Run.Recipe != "" && !control.Off(r.Name) {
			out = append(out, r)
		}
	}
	return out
}

func (c *Controller) hasRules() bool { return len(c.recipeRules()) > 0 }

// Item is sandbox sb's facts for the rules: its disk (usage), whether a
// task of it runs (busy, or a rule's task queued), and its rules' history.
func (c *Controller) Item(sb Sandbox, usage diskcare.Usage, busy bool) rules.Item {
	it := rules.Item{Kind: rules.OnSandbox, Sandbox: sb.Name, Awake: true,
		DiskFree: usage.Free, DiskSize: usage.Size, TaskActive: busy}
	for _, r := range c.recipeRules() {
		filename := api.SandboxRuleTaskFile(sb.Name, r.Name)
		var ended []rules.PastRun
		for _, e := range c.deps.Queue.EndedRuns(filename) {
			ended = append(ended, rules.PastRun{StartedAt: e.StartedAt, Failed: e.Failed, EngineUnavailable: e.EngineUnavailable})
		}
		var lastStart, lastEnd time.Time
		failed := false
		if last := c.deps.Queue.GetProcessedTask(filename); last != nil {
			lastStart, lastEnd, failed = last.StartedAt, last.CompletedAt, last.Status == api.StatusFailed
		}
		it.Runs = append(it.Runs, rules.PastRuns(r.Name, ended, lastStart, lastEnd, failed, "")...)
		if c.deps.Queue.TaskExists(filename) {
			it.Queued = append(it.Queued, r.Name)
			it.TaskActive = true
		}
	}
	return it
}

// sync checks one awake sandbox: measures its disk, records it, and queues
// what a rule on sandboxes decides.
func (c *Controller) sync(ctx context.Context, sb Sandbox) {
	if c.paused() || !c.hasRules() {
		return
	}
	usage, err := c.deps.Disk(ctx, sb.Name)
	if err != nil {
		// An image without GET /v1/disk, a pod not ready: no disk fact,
		// so no disk predicate holds.
		klog.V(2).Infof("Rules on sandboxes: %s's disk: %v", sb.Name, err)
		usage = diskcare.Usage{}
	} else if b, err := json.Marshal(usage); err == nil {
		if err := c.deps.Annotate(ctx, sb.Name, map[string]string{DiskAnnotation: string(b)}); err != nil {
			klog.V(2).Infof("Rules on sandboxes: recording %s's disk: %v", sb.Name, err)
		}
	}
	it := c.Item(sb, usage, c.deps.Busy(ctx, sb.Name))
	control := c.deps.Control()
	for _, d := range control.Decide(it, c.deps.Env()) {
		if d.Outcome != rules.OutcomeRun || d.Recipe == "" {
			continue
		}
		c.queue(sb, d, usage, control)
	}
}

// queue enqueues the task rule decision d runs in sandbox sb.
func (c *Controller) queue(sb Sandbox, d rules.Decision, usage diskcare.Usage, control *rules.Control) {
	filename := api.SandboxRuleTaskFile(sb.Name, d.Rule)
	if c.deps.Queue.TaskExists(filename) {
		return
	}
	var rule *rules.Rule
	for _, r := range control.Set() {
		if r.Name == d.Rule {
			rule = r
		}
	}
	now := time.Now()
	task := &api.QueueTask{
		Type:          api.TypeSandboxRule,
		URL:           sb.RepoURL,
		Sandbox:       sb.Name,
		Priority:      api.PriorityLow,
		Phase:         api.PhaseChores,
		CreatedAt:     now,
		EnqueuedAt:    now,
		Assignee:      sb.User,
		Status:        api.StatusPending,
		TriggerReason: api.TriggerReasonRuleDecided,
		TriggerNotes:  "workspace " + usage.String(),
	}
	act.Task(task, d, rule)
	if err := c.deps.Queue.Enqueue(filename, task); err != nil {
		klog.Warningf("Rules on sandboxes: queueing %s on %s: %v", d.Rule, sb.Name, err)
		return
	}
	klog.Infof("Rules on sandboxes: queued %s (recipe %s) on %s: workspace %s", d.Rule, d.Recipe, sb.Name, usage)
}
