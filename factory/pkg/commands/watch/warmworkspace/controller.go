// Package warmworkspace is the watch daemon's warm workspace pass
// (design/warm-workspace.md). It keeps a recent snapshot of a warmed
// workspace disk for the repository, which new sandboxes start from.
//
// It keeps no state of its own: a watch process lives minutes and a warm
// takes far longer, so the cluster holds it (the warm sandbox's
// annotations and the snapshots). Each pass asks Due whether there is
// anything to do and, if so, runs Warm, which moves the cycle on by a
// step and may block for as long as the warm recipe runs. One Warm runs
// at a time.
package warmworkspace

import (
	"context"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// DefaultInterval is the delay between passes.
const DefaultInterval = 5 * time.Minute

// Deps holds the collaborators of a Controller.
type Deps struct {
	// Paused reports whether the watcher is draining, in which case no
	// warm starts: it is starting work.
	Paused func() bool
	// Due reports whether the warm cycle has anything to do.
	Due func(ctx context.Context) (bool, error)
	// Warm moves the warm cycle on by a step.
	Warm func(ctx context.Context) error
}

// Controller runs the warm cycle.
type Controller struct {
	interval time.Duration
	deps     Deps

	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
}

// New constructs a Controller that passes every interval (DefaultInterval
// if it is not positive).
func New(interval time.Duration, deps Deps) *Controller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Controller{interval: interval, deps: deps}
}

// Run passes every interval until ctx is cancelled, then waits for a Warm
// under way to return.
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

// SyncOnce starts Warm in the background if the cycle is due and no Warm
// is running.
func (c *Controller) SyncOnce(ctx context.Context) {
	if c.deps.Paused != nil && c.deps.Paused() {
		return
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	due, err := c.deps.Due(ctx)
	if err != nil {
		c.mu.Unlock()
		klog.Errorf("Warm workspace: %v", err)
		return
	}
	if !due {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
		}()
		if err := c.deps.Warm(ctx); err != nil && ctx.Err() == nil {
			klog.Errorf("Warm workspace: %v", err)
		}
	}()
}

// Wait waits for a Warm under way to return.
func (c *Controller) Wait() {
	c.wg.Wait()
}
