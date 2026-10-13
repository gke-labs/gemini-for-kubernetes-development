package dispatcher

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// Recover returns the tasks a previous run left in the processing queue to
// incoming.
//
// Every task's factory command names its run (--run-name: a recipe task's
// RunName, an agent chore's ChoreRunName), and run again under that name
// it picks the run up instead of starting another: it follows a run still
// going, once its sandbox is free (dispatchTask waits for that), applies
// the result of one that finished while nobody was watching, finds one
// applied already, or starts it if it never started. So nothing here asks
// the sandbox what became of the task: requeued, the task finds out
// itself, and reports its outcome as any task does.
func (d *Dispatcher) Recover(ctx context.Context) {
	if err := d.queue.SyncProcessingFromDisk(); err != nil {
		klog.Errorf("Failed to sync processing tasks from disk during recovery: %v", err)
	}

	for filename, task := range d.queue.ProcessingTasks() {
		d.recoverTask(filename, task)
	}

	// Any file still left in processing could not be parsed, so return it to incoming
	// rather than leaving it orphaned.
	if err := d.queue.RequeueUntrackedProcessingFiles(); err != nil {
		klog.Errorf("Failed to requeue untracked processing files: %v", err)
	}
}

// recoverTask returns a single task found in the processing queue on
// startup to incoming, to pick up its run.
func (d *Dispatcher) recoverTask(filename string, task *api.QueueTask) {
	task.Status = api.StatusPending
	task.Recovered = true
	if err := d.queue.Enqueue(filename, task); err != nil {
		klog.Errorf("Failed to requeue stuck task %s: %v", filename, err)
		return
	}
	klog.Infof("Recovered stuck task %s from processing to incoming", filename)
}
