// Package retros is the watch daemon's weekly retro (design/retro.md §6):
// on the overseer's schedule (spec.retro.schedule, --retro-schedule) it
// runs what `factory retro` runs for the repository - staging the week
// from the collector, the retro recipe on it, its Change opened as a PR.
//
// When it last ran is kept in the queue directory (retro_state.json), so a
// restart neither runs it again nor skips a week. The first time it sees
// a schedule it runs nothing and waits for the schedule's next point: an
// upgrade does not start a retro. One retro runs at a time.
package retros

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/klog/v2"
)

// DefaultInterval is the delay between checks of the schedule.
const DefaultInterval = 10 * time.Minute

// StateFile is the queue directory's file of when the retro last ran.
const StateFile = "retro_state.json"

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseSchedule is a schedule: cron's five fields or a descriptor
// (@weekly).
func ParseSchedule(s string) (cron.Schedule, error) {
	sched, err := parser.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("retro schedule %q: %w", s, err)
	}
	return sched, nil
}

// Deps holds the collaborators of a Controller.
type Deps struct {
	// Paused reports whether the watcher is draining: no retro starts.
	Paused func() bool
	// Run runs the retro for the window since; it may block for as long
	// as the recipe runs.
	Run func(ctx context.Context, since time.Time) error
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Controller runs the retro on its schedule.
type Controller struct {
	interval time.Duration
	schedule cron.Schedule
	state    string
	deps     Deps

	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
}

// New constructs a Controller that checks schedule every interval
// (DefaultInterval if not positive), keeping its state in queueDir.
func New(interval time.Duration, schedule cron.Schedule, queueDir string, deps Deps) *Controller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Controller{interval: interval, schedule: schedule, state: filepath.Join(queueDir, StateFile), deps: deps}
}

// state is retro_state.json.
type state struct {
	// LastRun is when the last retro started, or, before the first, when
	// the schedule was first seen.
	LastRun time.Time `json:"lastRun"`
}

// Run checks the schedule every interval until ctx is cancelled, then
// waits for a retro under way to return.
func (c *Controller) Run(ctx context.Context) error {
	defer c.wg.Wait()
	ticker := time.NewTicker(c.interval)
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

// SyncOnce starts the retro in the background when it is due and none is
// running.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.deps.Paused != nil && c.deps.Paused() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return
	}
	now := c.deps.Now().UTC()
	st, err := c.read()
	switch {
	case errors.Is(err, os.ErrNotExist):
		// First seen: wait for the schedule's next point.
		if err := c.write(state{LastRun: now}); err != nil {
			klog.Errorf("Retro: %v", err)
		}
		klog.Infof("Retro: first run at %s", c.schedule.Next(now).Format(time.RFC3339))
		return
	case err != nil:
		klog.Errorf("Retro: %v", err)
		return
	}
	if c.schedule.Next(st.LastRun).After(now) {
		return
	}
	// Recorded before it runs: a retro that fails, or a watch that
	// restarts under it, waits for the next point rather than starting
	// another at once.
	if err := c.write(state{LastRun: now}); err != nil {
		klog.Errorf("Retro: %v", err)
		return
	}
	// Its window is the time since the last, a week at least: the first
	// after the schedule was seen covers a whole week too.
	since := st.LastRun
	if week := now.AddDate(0, 0, -7); week.Before(since) {
		since = week
	}
	c.running = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
		}()
		klog.Infof("Retro: running for the window since %s", since.Format(time.RFC3339))
		if err := c.deps.Run(ctx, since); err != nil {
			klog.Errorf("Retro: %v", err)
		}
	}()
}

// Wait waits for a retro under way to return.
func (c *Controller) Wait() { c.wg.Wait() }

func (c *Controller) read() (state, error) {
	var st state
	data, err := os.ReadFile(c.state)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("reading %s: %w", c.state, err)
	}
	return st, nil
}

func (c *Controller) write(st state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := c.state + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.state)
}
