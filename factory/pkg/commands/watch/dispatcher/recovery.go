package dispatcher

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// DefaultAdoptionPollInterval is how often an adopted task's sandbox is polled when
// no interval is configured.
const DefaultAdoptionPollInterval = 5 * time.Second

// Recover reconciles tasks that were left in the processing queue by a previous run.
//
// Because task execution inside a cluster sandbox is detached, a task may still be
// running even though the process that started it is gone. Each stuck task resolves
// to exactly one of three states:
//
//  1. Still running: the sandbox lease is re-acquired and an adoption monitor
//     supervises the task until it finishes.
//  2. Already finished: the task is moved straight to processed.
//  3. Gone (failed, evicted or missing): the task is requeued for a fresh attempt.
//
// Recover returns once every task has been triaged; adopted tasks continue to be
// supervised in the background and are covered by Wait.
func (d *Dispatcher) Recover(ctx context.Context) {
	if err := d.queue.SyncProcessingFromDisk(); err != nil {
		klog.Errorf("Failed to sync processing tasks from disk during recovery: %v", err)
	}

	for filename, task := range d.queue.ProcessingTasks() {
		d.recoverTask(ctx, filename, task)
	}

	// Any file still left in processing could not be parsed, so return it to incoming
	// rather than leaving it orphaned.
	if err := d.queue.RequeueUntrackedProcessingFiles(); err != nil {
		klog.Errorf("Failed to requeue untracked processing files: %v", err)
	}
}

// recoverTask triages a single task found in the processing queue on startup.
func (d *Dispatcher) recoverTask(ctx context.Context, filename string, task *api.QueueTask) {
	sandboxName := d.sandboxes.ResolveSandboxName(ctx, task.Type, task.Number)

	if sandboxName != "" {
		running, err := d.sandboxes.IsTaskRunning(ctx, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running during recovery: %v", sandboxName, err)
		} else if running {
			d.adoptTask(ctx, filename, task, sandboxName)
			return
		}

		completed, err := d.sandboxes.IsTaskCompleted(ctx, sandboxName, task.Type)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s completed its task during recovery: %v", sandboxName, err)
		} else if completed {
			klog.Infof("Task %s already completed in sandbox %s. Moving from processing to processed.", filename, sandboxName)
			if err := d.queue.CompleteTask(filename, task); err != nil {
				klog.Errorf("Failed to complete recovered task %s: %v", filename, err)
			}
			return
		}
	}

	// The sandbox is gone or never ran the task: requeue it for a fresh attempt.
	task.Status = api.StatusPending
	task.Recovered = true
	if err := d.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to requeue stuck task %s: %v", filename, err)
		return
	}
	klog.Infof("Recovered stuck task %s from processing to incoming", filename)
}

// adoptTask takes ownership of a task that is still executing in its sandbox by
// holding the sandbox lease and supervising the task until it finishes.
func (d *Dispatcher) adoptTask(ctx context.Context, filename string, task *api.QueueTask, sandboxName string) {
	if !d.sandboxLocks.TryAcquire(sandboxName, filename) {
		klog.Infof("Task %s is still actively running in sandbox %s. Failed to acquire lease.", filename, sandboxName)
		return
	}

	klog.Infof("Task %s is still actively running in sandbox %s. Adopting task.", filename, sandboxName)
	d.wg.Add(1)
	go func() {
		defer func() {
			d.sandboxLocks.Release(sandboxName, filename)
			d.wg.Done()
		}()
		d.monitorAdoptedTask(ctx, filename, task, sandboxName)
	}()
}

// monitorAdoptedTask supervises an adopted task that was already running in a cluster
// sandbox across restarts, recording its outcome once the sandbox reports it finished.
func (d *Dispatcher) monitorAdoptedTask(ctx context.Context, taskFilename string, task *api.QueueTask, sandboxName string) {
	monitorCtx := ctx
	if d.cfg.TaskTimeout > 0 {
		var monitorCancel context.CancelFunc
		monitorCtx, monitorCancel = context.WithTimeout(ctx, d.remainingTimeout(task))
		defer monitorCancel()
	}

	ticker := time.NewTicker(d.adoptionPollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-monitorCtx.Done():
			if monitorCtx.Err() == context.DeadlineExceeded {
				d.failTimedOutAdoptedTask(ctx, taskFilename, task, sandboxName)
			}
			return
		case <-ticker.C:
			running, err := d.sandboxes.IsTaskRunning(monitorCtx, sandboxName)
			if err != nil {
				klog.Warningf("Failed to check status of adopted sandbox %s: %v", sandboxName, err)
				continue
			}
			if running {
				continue
			}

			// Task has finished! Check whether it completed or failed
			completed, err := d.sandboxes.IsTaskCompleted(monitorCtx, sandboxName, task.Type)
			if err != nil {
				klog.Warningf("Failed to check completion state of adopted sandbox %s: %v", sandboxName, err)
			}

			if completed {
				klog.Infof("Adopted task %s in sandbox %s completed successfully.", taskFilename, sandboxName)
				_ = d.queue.CompleteTask(taskFilename, task)
				d.coordinator.NotifyTaskFinished(monitorCtx, task, nil)
			} else {
				klog.Warningf("Adopted task %s in sandbox %s failed or terminated.", taskFilename, sandboxName)
				_ = d.queue.FailTask(taskFilename, task, "adopted task failed or terminated in sandbox")
				d.coordinator.NotifyTaskFinished(monitorCtx, task, errAdoptedTaskFailed)
			}
			return
		}
	}
}

// failTimedOutAdoptedTask cleans up an adopted task that exhausted its timeout budget.
func (d *Dispatcher) failTimedOutAdoptedTask(ctx context.Context, taskFilename string, task *api.QueueTask, sandboxName string) {
	klog.Warningf("Adopted task %s in sandbox %s timed out after %s", taskFilename, sandboxName, d.cfg.TaskTimeout)
	if sandboxName != "" {
		if err := d.sandboxes.DeleteSandbox(ctx, sandboxName); err != nil {
			klog.Errorf("Failed to delete sandbox '%s' for timed out adopted task: %v", sandboxName, err)
		}
	}
	_ = d.queue.FailTask(taskFilename, task, "adopted task timed out")
	d.coordinator.NotifyTaskFinished(ctx, task, errAdoptedTaskTimedOut)
}

// remainingTimeout returns the portion of the task timeout budget that has not yet
// elapsed, so an adopted task is not granted a full fresh timeout after a restart.
func (d *Dispatcher) remainingTimeout(task *api.QueueTask) time.Duration {
	baseTime := task.StartedAt
	if baseTime.IsZero() {
		baseTime = task.EnqueuedAt
	}
	if baseTime.IsZero() {
		baseTime = task.CreatedAt
	}
	if baseTime.IsZero() {
		return d.cfg.TaskTimeout
	}

	remaining := d.cfg.TaskTimeout - time.Since(baseTime)
	if remaining <= 0 {
		return 1 * time.Millisecond
	}
	return remaining
}

// adoptionPollInterval returns the configured adoption poll interval, or the default.
func (d *Dispatcher) adoptionPollInterval() time.Duration {
	if d.cfg.AdoptionPollInterval > 0 {
		return d.cfg.AdoptionPollInterval
	}
	return DefaultAdoptionPollInterval
}
