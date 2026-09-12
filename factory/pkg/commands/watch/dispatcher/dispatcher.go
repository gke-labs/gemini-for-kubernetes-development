// Package dispatcher owns the execution side of the watch daemon: claiming
// queued tasks and running them to completion.
//
// It is deliberately decoupled from repository scanning. The Dispatcher
// coordinates queue state, sandbox leases and concurrency limits, while the
// actual workload execution is delegated to a TaskRunner (see CLIRunner).
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
)

const (
	// DefaultInterval is the interval between dispatch cycles when none is configured.
	DefaultInterval = 30 * time.Second
)

var (
	// errAdoptedTaskFailed reports that a task adopted on startup ended unsuccessfully.
	errAdoptedTaskFailed = errors.New("adopted task failed or terminated in sandbox")
	// errAdoptedTaskTimedOut reports that a task adopted on startup exhausted its timeout budget.
	errAdoptedTaskTimedOut = errors.New("adopted task timed out")
)

// TaskRunner executes a single claimed task to completion.
//
// Implementations are responsible only for running the task workload itself;
// queue bookkeeping, sandbox leasing, and GitHub side effects are owned by the
// Dispatcher. This keeps the runner trivially fakeable in unit tests.
type TaskRunner interface {
	// Run executes the task synchronously and returns an error if it did not succeed.
	// The task is executed as the given GitHub user, which may be empty to use the default.
	Run(ctx context.Context, taskFilename string, task *api.QueueTask, selectedUser string) error
}

// SandboxService abstracts the cluster interactions the dispatcher needs to
// decide whether a task may be scheduled, and to clean up after a timeout.
type SandboxService interface {
	// ResolveSandboxName returns the name of the sandbox that a task would execute in.
	ResolveSandboxName(ctx context.Context, taskType api.TaskType, number int) string
	// IsTaskRunning reports whether the sandbox is currently executing a task.
	IsTaskRunning(ctx context.Context, sandboxName string) (bool, error)
	// IsTaskCompleted reports whether the sandbox has already completed a task of the given type.
	IsTaskCompleted(ctx context.Context, sandboxName string, taskType api.TaskType) (bool, error)
	// CountRunningTasks returns the number of sandboxes currently executing a task.
	CountRunningTasks(ctx context.Context) (int, error)
	// DeleteSandbox force deletes a sandbox, e.g. after a task times out.
	DeleteSandbox(ctx context.Context, sandboxName string) error
}

// TaskCoordinator abstracts the GitHub-side interactions that surround the
// execution of a task: validating that the task is still actionable, choosing
// which bot account runs it, and reporting progress back to the issue or PR.
type TaskCoordinator interface {
	// ShouldCancelTask reports whether a claimed task is no longer actionable
	// (e.g. the target issue/PR was closed or carries a stop label) along with
	// a human readable reason.
	ShouldCancelTask(ctx context.Context, task *api.QueueTask) (bool, string)
	// SelectUser returns the GitHub user the task should be executed as.
	SelectUser(ctx context.Context, task *api.QueueTask) (string, error)
	// NotifyTaskStarted reports that execution of the task has begun.
	NotifyTaskStarted(ctx context.Context, task *api.QueueTask)
	// NotifyTaskFinished reports the outcome of the task, where a nil error indicates success.
	NotifyTaskFinished(ctx context.Context, task *api.QueueTask, taskErr error)
}

// Config holds the tuning knobs of a Dispatcher.
type Config struct {
	// Interval is the delay between dispatch cycles.
	Interval time.Duration
	// AdoptionPollInterval is how often an adopted task's sandbox is polled during
	// startup recovery. Defaults to DefaultAdoptionPollInterval.
	AdoptionPollInterval time.Duration
	// MaxActions caps the number of tasks dispatched within a single cycle.
	MaxActions int
	// MaxPending caps the number of concurrently active sandbox tasks.
	MaxPending int
	// TaskTimeout bounds the execution time of a single task. Zero means no timeout.
	TaskTimeout time.Duration
	// DryRun reports what would be dispatched without executing anything.
	DryRun bool
}

// Deps holds the collaborators of a Dispatcher.
type Deps struct {
	Queue        *concurrency.TaskQueueManager
	SandboxLocks *concurrency.SandboxLockRegistry
	Sandboxes    SandboxService
	Coordinator  TaskCoordinator
	Runner       TaskRunner
}

// Dispatcher claims eligible tasks from the queue and executes them via a TaskRunner.
//
// It owns the dispatch loop and the lifecycle of the workers it spawns:
//   - eligibility (drain mode, concurrency limits, sandbox availability)
//   - sandbox lease acquisition and release
//   - queue state transitions (claim, start, complete, fail, release)
//
// It deliberately knows nothing about how a task is actually executed, nor how
// issues and pull requests are scanned.
type Dispatcher struct {
	cfg          Config
	queue        *concurrency.TaskQueueManager
	sandboxLocks *concurrency.SandboxLockRegistry
	sandboxes    SandboxService
	coordinator  TaskCoordinator
	runner       TaskRunner

	// wg tracks in-flight task workers spawned by the dispatcher.
	wg sync.WaitGroup
}

// New constructs a Dispatcher from its configuration and dependencies.
func New(cfg Config, deps Deps) *Dispatcher {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Dispatcher{
		cfg:          cfg,
		queue:        deps.Queue,
		sandboxLocks: deps.SandboxLocks,
		sandboxes:    deps.Sandboxes,
		coordinator:  deps.Coordinator,
		runner:       deps.Runner,
	}
}

// Run executes the dispatch loop until ctx is cancelled.
// It dispatches immediately, then once per configured interval.
// It waits for all in-flight tasks to finish before returning.
func (d *Dispatcher) Run(ctx context.Context) error {
	defer d.wg.Wait()

	d.DispatchOnce(ctx)

	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.DispatchOnce(ctx)
			// Reset the ticker after running tasks to ensure we wait the expected interval
			ticker.Reset(d.cfg.Interval)
		}
	}
}

// Wait blocks until all in-flight tasks spawned by the dispatcher have completed.
func (d *Dispatcher) Wait() {
	d.wg.Wait()
}

// DispatchOnce executes a single task dispatch cycle, claiming and starting as
// many eligible tasks as the configured limits allow.
func (d *Dispatcher) DispatchOnce(ctx context.Context) {
	// sync from disk in case task files were changed out of band (e.g. admin editing files on disk)
	_ = d.queue.SyncIncomingFromDisk()

	var releasedTasks []string
	defer func() {
		for _, fn := range releasedTasks {
			_ = d.queue.ReleaseTask(fn)
		}
	}()

	actionsTaken := 0
	activeSandboxesInCycle := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if d.queue.IsDrainMode() {
			klog.Infof("[DO NOT PROCESS] Drain mode detected during cycle execution. Stopping scheduling of remaining queued tasks.")
			return
		}
		if actionsTaken >= d.cfg.MaxActions {
			fmt.Printf("Reached maximum actions limit (%d) for this cycle. Stopping execution.\n", d.cfg.MaxActions)
			return
		}

		runningCount, err := d.sandboxes.CountRunningTasks(ctx)
		if err != nil {
			klog.Errorf("Failed to count running sandbox tasks: %v", err)
		}
		_, filesInProcessing, _ := d.queue.GetCounts()
		activeCount := max(runningCount, filesInProcessing)

		if activeCount >= d.cfg.MaxPending {
			fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining queue items.\n", d.cfg.MaxPending)
			return
		}

		// Pop next fair-share candidate
		filename, task, err := d.queue.ClaimNextCandidate()
		if err != nil {
			klog.Errorf("Failed to claim next candidate task: %v", err)
			return
		}
		if task == nil {
			// No more eligible tasks ready in incoming queue
			return
		}

		sandboxName := d.sandboxes.ResolveSandboxName(ctx, task.Type, task.Number)
		dispatched, requeue := d.dispatchTask(ctx, filename, task, sandboxName, activeSandboxesInCycle)
		if requeue {
			releasedTasks = append(releasedTasks, filename)
		}
		if dispatched {
			activeSandboxesInCycle[sandboxName] = true
			actionsTaken++
		}
	}
}

// dispatchTask validates a single claimed task and, if it is eligible, starts a
// worker goroutine for it. It reports whether the task counted as an action for
// this cycle, and whether the claimed task must be returned to the incoming queue.
func (d *Dispatcher) dispatchTask(ctx context.Context, filename string, task *api.QueueTask, sandboxName string, activeSandboxesInCycle map[string]bool) (dispatched, requeue bool) {
	if activeSandboxesInCycle[sandboxName] || d.sandboxLocks.IsBusy(sandboxName) {
		klog.Infof("Skipping task %s because sandbox %s is already scheduled or busy.", filename, sandboxName)
		return false, true
	}

	// Check if target sandbox pod is currently running in Kubernetes
	running, err := d.sandboxes.IsTaskRunning(ctx, sandboxName)
	if err != nil {
		klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
		return false, true
	}
	if running {
		klog.Infof("Skipping task %s because sandbox %s is currently busy running another task.", filename, sandboxName)
		return false, true
	}

	// Validate GitHub status (stop label or closed)
	if cancel, reason := d.coordinator.ShouldCancelTask(ctx, task); cancel {
		klog.Infof("Skipping task %s and removing from incoming because %s", filename, reason)
		_ = d.queue.RemoveTask(filename)
		return false, false
	}

	// Check if recovered task is already completed in sandbox
	if task.Type != api.TypeAgentChore && task.Recovered {
		completed, err := d.sandboxes.IsTaskCompleted(ctx, sandboxName, task.Type)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s completed task: %v", sandboxName, err)
			return false, true
		}
		if completed {
			klog.Infof("Recovered task %s is already completed in sandbox %s. Marking as completed.", filename, sandboxName)
			if d.cfg.DryRun {
				return false, true
			}
			_ = d.queue.CompleteTask(filename, task)
			return false, false
		}
	}

	if !d.sandboxLocks.TryAcquire(sandboxName, filename) {
		klog.Infof("Skipping task %s because lease for sandbox %s could not be acquired.", filename, sandboxName)
		return false, true
	}

	if d.cfg.DryRun {
		d.sandboxLocks.Release(sandboxName, filename)
		fmt.Printf("[DRYRUN] Would process task %s (Type: %s, URL: %s)\n", filename, task.Type, task.URL)
		return true, true
	}

	if err := d.queue.StartTask(filename, task); err != nil {
		klog.Errorf("Failed to start task %s: %v", filename, err)
		d.sandboxLocks.Release(sandboxName, filename)
		return false, true
	}

	d.wg.Add(1)
	go func() {
		defer func() {
			d.sandboxLocks.Release(sandboxName, filename)
			d.wg.Done()
		}()
		d.executeTask(ctx, filename, task, sandboxName)
	}()

	return true, false
}

// executeTask runs a started task to completion and records the outcome in the queue.
func (d *Dispatcher) executeTask(ctx context.Context, taskFilename string, task *api.QueueTask, sandboxName string) {
	fmt.Printf("Starting task %s (Type: %s, URL: %s)...\n", taskFilename, task.Type, task.URL)
	if task.StartedAt.IsZero() {
		task.StartedAt = time.Now()
	}

	taskCtx := ctx
	if d.cfg.TaskTimeout > 0 {
		var taskCancel context.CancelFunc
		taskCtx, taskCancel = context.WithTimeout(ctx, d.cfg.TaskTimeout)
		defer taskCancel()
	}

	d.coordinator.NotifyTaskStarted(ctx, task)

	selectedUser, err := d.coordinator.SelectUser(ctx, task)
	if err != nil {
		klog.Errorf("Failed to select user for task %s: %v", taskFilename, err)
		_ = d.queue.FailTask(taskFilename, task, err.Error())
		return
	}

	if taskErr := d.runner.Run(taskCtx, taskFilename, task, selectedUser); taskErr != nil {
		klog.Errorf("Task %s failed: %v", taskFilename, taskErr)
		_ = d.queue.FailTask(taskFilename, task, taskErr.Error())
		d.coordinator.NotifyTaskFinished(ctx, task, taskErr)

		// Force clean up sandbox if the task timed out
		if taskCtx.Err() == context.DeadlineExceeded && sandboxName != "" {
			klog.Warningf("Task %s timed out after %s! Force cleaning up sandbox '%s'...", taskFilename, d.cfg.TaskTimeout, sandboxName)
			if err := d.sandboxes.DeleteSandbox(ctx, sandboxName); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' on timeout: %v", sandboxName, err)
			}
		}
		return
	}

	fmt.Printf("Task %s completed successfully.\n", taskFilename)
	_ = d.queue.CompleteTask(taskFilename, task)
	d.coordinator.NotifyTaskFinished(ctx, task, nil)
}
