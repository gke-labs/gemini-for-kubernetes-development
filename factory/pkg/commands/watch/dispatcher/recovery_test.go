package dispatcher

import (
	"context"
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

func TestRecover_AlreadyCompletedTaskMovesToProcessed(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, _ := testDispatcher(t, tempDir, nil)
	sandboxes.completed["sandbox"] = true

	writeProcessingTask(t, tempDir, "task-issue-101.yaml", 101)

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if _, err := os.Stat(filepath.Join(tempDir, "processed", "task-issue-101.yaml")); err != nil {
		t.Errorf("expected the task file in processed: %v", err)
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
