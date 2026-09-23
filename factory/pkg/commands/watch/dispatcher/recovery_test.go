package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// writeProcessingTask writes a task YAML directly into the processing directory,
// simulating a task left behind by a previous run.
func writeProcessingTask(t *testing.T, queueDir, filename string, number int) {
	t.Helper()
	body := fmt.Sprintf("type: issue-fix\nnumber: %d\nstatus: Running\nurl: https://github.com/test-owner/test-repo/issues/%d\n", number, number)
	if err := os.WriteFile(filepath.Join(queueDir, "processing", filename), []byte(body), 0644); err != nil {
		t.Fatalf("failed to write processing task file: %v", err)
	}
}

func TestRecover_AdoptsRunningTaskAndCompletesIt(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-100.yaml", 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.Recover(ctx)

	// The adoption monitor must hold the sandbox lease while the task is running.
	if !d.sandboxLocks.IsBusy("sandbox") {
		t.Fatal("expected the adopted task to hold the sandbox lease")
	}
	if _, proc, _ := queue.GetCounts(); proc != 1 {
		t.Errorf("expected the adopted task to remain in processing, got %d", proc)
	}

	// Simulate the cluster task finishing successfully.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.completed["sandbox"] = true
	sandboxes.mu.Unlock()

	waitDone := make(chan struct{})
	go func() {
		d.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the adopted task to complete")
	}

	if d.sandboxLocks.IsBusy("sandbox") {
		t.Error("expected the sandbox lease to be released after the adopted task completed")
	}
	if len(runner.invocations()) != 0 {
		t.Error("expected an adopted task not to be re-executed")
	}
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}
	waitForCounts(t, queue, 0, 0, 1)

	if _, err := os.Stat(filepath.Join(tempDir, "processed", "task-issue-100.yaml")); err != nil {
		t.Errorf("expected the adopted task file in processed: %v", err)
	}
}

func TestRecover_AdoptedTaskFailureIsRecorded(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-104.yaml", 104)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Recover(ctx)

	// The sandbox stops without ever reporting completion.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.mu.Unlock()

	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] == nil {
		t.Errorf("expected a single failure notification, got %v", outcomes)
	}
	if d.sandboxLocks.IsBusy("sandbox") {
		t.Error("expected the sandbox lease to be released after the adopted task failed")
	}
}

// An unanswered probe is not a verdict: the monitor keeps polling rather than
// recording a failure for a task whose sandbox simply could not be reached.
func TestRecover_AdoptedTaskSurvivesFailedCompletionProbe(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true
	sandboxes.completed["sandbox"] = true
	sandboxes.completedErr = errors.New("transient connection reset")

	writeProcessingTask(t, tempDir, "task-issue-108.yaml", 108)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Recover(ctx)

	// The sandbox stops while the completion probe is still failing.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.mu.Unlock()

	// The monitor cannot resolve the task while the probe keeps failing, so it must
	// still be in processing, neither completed nor failed.
	time.Sleep(20 * time.Millisecond)
	if _, proc, _ := queue.GetCounts(); proc != 1 {
		t.Fatalf("expected the adopted task to stay in processing while the probe fails, got %d", proc)
	}
	if outcomes := coordinator.outcomes(); len(outcomes) != 0 {
		t.Fatalf("expected no outcome to be reported from a failed probe, got %v", outcomes)
	}

	// The cluster answers again, and the real outcome is recorded.
	sandboxes.mu.Lock()
	sandboxes.completedErr = nil
	sandboxes.mu.Unlock()

	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}
}

func TestRecover_AlreadyCompletedTaskMovesToProcessed(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, nil)
	sandboxes.completed["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-101.yaml", 101)

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if _, err := os.Stat(filepath.Join(tempDir, "processed", "task-issue-101.yaml")); err != nil {
		t.Errorf("expected the task file in processed: %v", err)
	}
	// Nothing reported this task while the watcher was down, so recovery has to.
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}
	if d.sandboxLocks.IsBusy("sandbox") {
		t.Error("expected no sandbox lease to be held for an already completed task")
	}
}

func TestRecover_MissingSandboxRequeuesTask(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, _, _ := testDispatcher(t, tempDir, nil)

	writeProcessingTask(t, tempDir, "task-issue-102.yaml", 102)

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 1, 0, 0)
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", "task-issue-102.yaml")); err != nil {
		t.Errorf("expected the task file back in incoming: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processing", "task-issue-102.yaml")); !os.IsNotExist(err) {
		t.Error("expected the task file to be removed from processing")
	}
	if d.sandboxLocks.IsBusy("sandbox") {
		t.Error("expected no sandbox lease to be held for a requeued task")
	}
}

func TestRecover_RequeuesUnparsableProcessingFile(t *testing.T) {
	tempDir := t.TempDir()
	d, _, _, _, _ := testDispatcher(t, tempDir, nil)

	fn := "task-issue-103.yaml"
	if err := os.WriteFile(filepath.Join(tempDir, "processing", fn), []byte("::not yaml::"), 0644); err != nil {
		t.Fatalf("failed to write corrupt task file: %v", err)
	}

	d.Recover(context.Background())

	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected the unparsable task file to be moved back to incoming: %v", err)
	}
}

func TestRecover_AdoptedTaskTimesOut(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
		cfg.TaskTimeout = 10 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-105.yaml", 105)

	d.Recover(context.Background())
	d.Wait()

	if deleted := sandboxes.deletedSandboxes(); len(deleted) != 1 || deleted[0] != "sandbox" {
		t.Errorf("expected the timed out sandbox to be deleted, got %v", deleted)
	}
	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] == nil {
		t.Errorf("expected a single failure notification, got %v", outcomes)
	}
}

func TestRemainingTimeout_SubtractsElapsedBudget(t *testing.T) {
	d := New(Config{TaskTimeout: time.Hour}, Deps{})

	t.Run("no timestamps uses the full budget", func(t *testing.T) {
		if got := d.remainingTimeout(&api.QueueTask{}); got != time.Hour {
			t.Errorf("remainingTimeout = %s, want %s", got, time.Hour)
		}
	})

	t.Run("elapsed time is deducted", func(t *testing.T) {
		task := &api.QueueTask{StartedAt: time.Now().Add(-30 * time.Minute)}
		got := d.remainingTimeout(task)
		if got > 30*time.Minute || got < 29*time.Minute {
			t.Errorf("remainingTimeout = %s, want ~30m", got)
		}
	})

	t.Run("exhausted budget is clamped", func(t *testing.T) {
		task := &api.QueueTask{StartedAt: time.Now().Add(-2 * time.Hour)}
		if got := d.remainingTimeout(task); got != time.Millisecond {
			t.Errorf("remainingTimeout = %s, want 1ms", got)
		}
	})
}

func TestAdoptionPollInterval_Default(t *testing.T) {
	d := New(Config{}, Deps{})
	if got := d.adoptionPollInterval(); got != DefaultAdoptionPollInterval {
		t.Errorf("adoptionPollInterval = %s, want %s", got, DefaultAdoptionPollInterval)
	}

	d = New(Config{AdoptionPollInterval: time.Second}, Deps{})
	if got := d.adoptionPollInterval(); got != time.Second {
		t.Errorf("adoptionPollInterval = %s, want 1s", got)
	}
}

func TestSandboxProbeRetryDelay_Default(t *testing.T) {
	d := New(Config{}, Deps{})
	if got := d.sandboxProbeRetryDelay(); got != DefaultSandboxProbeRetryDelay {
		t.Errorf("sandboxProbeRetryDelay = %s, want %s", got, DefaultSandboxProbeRetryDelay)
	}

	d = New(Config{SandboxProbeRetryDelay: time.Second}, Deps{})
	if got := d.sandboxProbeRetryDelay(); got != time.Second {
		t.Errorf("sandboxProbeRetryDelay = %s, want 1s", got)
	}
}

// Recovery is the only thing that inspects a stuck task's sandbox, so a probe that
// never answers must not be read as "the sandbox is gone": requeueing on that basis
// would re-run work that may already have finished. The task stays in processing.
func TestRecover_UnknownSandboxStateLeavesTaskInProcessing(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.SandboxProbeRetryDelay = time.Millisecond
	})
	sandboxes.runningErr = errors.New("the API server is unavailable")

	writeProcessingTask(t, tempDir, "task-issue-106.yaml", 106)

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 1, 0)
	if _, err := os.Stat(filepath.Join(tempDir, "processing", "task-issue-106.yaml")); err != nil {
		t.Errorf("expected the task file to stay in processing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", "task-issue-106.yaml")); !os.IsNotExist(err) {
		t.Error("expected the task not to be requeued while its sandbox state is unknown")
	}
	if len(runner.invocations()) != 0 {
		t.Error("expected no execution of a task whose sandbox state is unknown")
	}
	if d.sandboxLocks.IsBusy("sandbox") {
		t.Error("expected no sandbox lease to be held for an untriaged task")
	}
}

// A blip while probing must not decide the task's fate either: recovery retries, and
// the answer it eventually gets is the one that counts.
func TestRecover_RetriesTransientProbeFailures(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.SandboxProbeRetryDelay = time.Millisecond
	})

	// The first probe fails; the retry reports a sandbox that finished the task.
	var probes int
	sandboxes.runningFn = func(string) (bool, error) {
		probes++
		if probes == 1 {
			return false, errors.New("transient connection reset")
		}
		return false, nil
	}
	sandboxes.completed["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-107.yaml", 107)

	d.Recover(context.Background())
	d.Wait()

	if probes < 2 {
		t.Errorf("expected the failed probe to be retried, got %d probes", probes)
	}
	waitForCounts(t, queue, 0, 0, 1)
	if _, err := os.Stat(filepath.Join(tempDir, "processed", "task-issue-107.yaml")); err != nil {
		t.Errorf("expected the task file in processed: %v", err)
	}
}

func writeProcessingChoreTask(t *testing.T, queueDir, filename string, number int) {
	t.Helper()
	body := fmt.Sprintf("type: agent-chore\nnumber: %d\nstatus: Running\nurl: https://github.com/test-owner/test-repo/issues/%d\n", number, number)
	if err := os.WriteFile(filepath.Join(queueDir, "processing", filename), []byte(body), 0644); err != nil {
		t.Fatalf("failed to write processing chore task file: %v", err)
	}
}

func TestRecover_AdoptsRunningChoreTaskAndSuspendsSandboxOnCompletion(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true

	writeProcessingChoreTask(t, tempDir, "task-chore-120.yaml", 120)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.Recover(ctx)

	// Simulate the cluster chore task finishing successfully.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.completed["sandbox"] = true
	sandboxes.mu.Unlock()

	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}

	suspended := sandboxes.suspendedSandboxes()
	if len(suspended) != 1 || suspended[0] != "sandbox" {
		t.Errorf("expected sandbox to be suspended after adopted chore completion, got %v", suspended)
	}
}

func TestRecover_AdoptsRunningChoreTaskAndSuspendsSandboxOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})
	sandboxes.running["sandbox"] = true

	writeProcessingChoreTask(t, tempDir, "task-chore-121.yaml", 121)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.Recover(ctx)

	// The sandbox stops without reporting completion (failure).
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.mu.Unlock()

	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] == nil {
		t.Errorf("expected a single failure notification, got %v", outcomes)
	}

	suspended := sandboxes.suspendedSandboxes()
	if len(suspended) != 1 || suspended[0] != "sandbox" {
		t.Errorf("expected sandbox to be suspended after adopted chore failure, got %v", suspended)
	}
}

func TestRecover_AlreadyCompletedChoreTaskSuspendsSandbox(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, _ := testDispatcher(t, tempDir, nil)
	sandboxes.completed["sandbox"] = true

	writeProcessingChoreTask(t, tempDir, "task-chore-122.yaml", 122)

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}

	suspended := sandboxes.suspendedSandboxes()
	if len(suspended) != 1 || suspended[0] != "sandbox" {
		t.Errorf("expected sandbox to be suspended for already completed chore task, got %v", suspended)
	}
}

func TestRecover_AdoptsTaskInterruptedByShutdown(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, coordinator, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.AdoptionPollInterval = 5 * time.Millisecond
	})

	startInterruptedTask(t, d, queue, runner, "task-issue-10.yaml", 10)
	waitForCounts(t, queue, 0, 1, 0)

	// Next run: the workload is still executing in its sandbox.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = true
	sandboxes.mu.Unlock()

	d.Recover(context.Background())

	if got := len(runner.invocations()); got != 1 {
		t.Errorf("expected the adopted task not to be re-executed, got %d runner invocations", got)
	}

	// The sandbox finishes the work that outlived the previous watch cycle.
	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = false
	sandboxes.completed["sandbox"] = true
	sandboxes.mu.Unlock()

	d.Wait()
	waitForCounts(t, queue, 0, 0, 1)

	outcomes := coordinator.outcomes()
	if len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single success notification from adoption, got %v", outcomes)
	}
}
