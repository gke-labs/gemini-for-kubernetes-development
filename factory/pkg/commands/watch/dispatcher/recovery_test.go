package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// Every task left in processing goes back to incoming, whatever its
// sandbox is doing: its command names its run, and run again under that
// name it follows the run, applies its result or finds it applied. Nothing
// is adopted, nothing is reported finished, and the run name survives the
// trip, or the requeued task would start a second run.
func TestRecover_RequeuesEveryTaskUnderItsRunName(t *testing.T) {
	enqueued := time.Date(2026, 10, 11, 7, 0, 1, 0, time.UTC)
	runName := func(task *api.QueueTask) string {
		if api.IsRuleTask(task.Type) {
			return api.RuleRunName(task)
		}
		return api.ChoreRunName(task)
	}
	for _, tc := range []struct {
		name, file, body string
	}{
		{"review", "task-pr-55-rule-review.yaml", "type: pr-rule\nrule: review\nnumber: 55\nurl: https://github.com/test-owner/test-repo/pull/55\ncommitSHA: abcdef0123\n"},
		{"fix", "task-issue-56-rule-fix.yaml", "type: issue-rule\nrule: fix\nnumber: 56\nurl: https://github.com/test-owner/test-repo/issues/56\n"},
		{"chore", "task-chore-deps.yaml", "type: agent-chore\nurl: https://github.com/test-owner/test-repo\nagentFile: .agents/deps.md\nagentName: Deps\n"},
		{"workflow", "task-workflow-x-issue-57.yaml", "type: agent-chore\nnumber: 57\nurl: https://github.com/test-owner/test-repo/issues/57\nagentFile: .agents/workflows/x.md\nsessionId: issue-57\n"},
	} {
		for _, running := range []bool{true, false} {
			tempDir := t.TempDir()
			d, queue, sandboxes, coordinator, runner := testDispatcher(t, tempDir, nil)
			sandboxes.running["sandbox"] = running
			body := tc.body + "status: Running\ncreatedAt: 2026-10-11T07:00:00Z\nenqueuedAt: 2026-10-11T07:00:01Z\n"
			if err := os.WriteFile(filepath.Join(tempDir, "processing", tc.file), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			var before api.QueueTask
			if err := yaml.Unmarshal([]byte(body), &before); err != nil {
				t.Fatal(err)
			}
			before.EnqueuedAt = enqueued

			d.Recover(context.Background())
			d.Wait()

			waitForCounts(t, queue, 1, 0, 0)
			if d.sandboxLocks.IsBusy("sandbox") || len(runner.invocations()) != 0 {
				t.Errorf("%s (running %v): adopted or run by recovery", tc.name, running)
			}
			if outcomes := coordinator.outcomes(); len(outcomes) != 0 {
				t.Errorf("%s (running %v): reported finished by recovery: %v", tc.name, running, outcomes)
			}
			b, err := os.ReadFile(filepath.Join(tempDir, "incoming", tc.file))
			if err != nil {
				t.Fatalf("%s: not back in incoming: %v", tc.name, err)
			}
			var task api.QueueTask
			if err := yaml.Unmarshal(b, &task); err != nil {
				t.Fatal(err)
			}
			if !task.Recovered || task.Status != api.StatusPending {
				t.Errorf("%s: requeued as %+v", tc.name, task)
			}
			if got, want := runName(&task), runName(&before); got != want {
				t.Errorf("%s: requeued under run name %s, not %s", tc.name, got, want)
			}
		}
	}
}

func TestRecover_RequeuesUnparsableProcessingFile(t *testing.T) {
	tempDir := t.TempDir()
	d, _, _, _, _ := testDispatcher(t, tempDir, nil)

	fn := "task-issue-103-rule-fix.yaml"
	if err := os.WriteFile(filepath.Join(tempDir, "processing", fn), []byte("::not yaml::"), 0644); err != nil {
		t.Fatalf("failed to write corrupt task file: %v", err)
	}

	d.Recover(context.Background())

	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected the unparsable task file to be moved back to incoming: %v", err)
	}
}

// A task a shutdown interrupted is left in processing, and the next run
// requeues it rather than adopting it.
func TestRecover_RequeuesTaskInterruptedByShutdown(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, nil)

	startInterruptedTask(t, d, queue, runner, "task-issue-10-rule-fix.yaml", 10)
	waitForCounts(t, queue, 0, 1, 0)

	sandboxes.mu.Lock()
	sandboxes.running["sandbox"] = true
	sandboxes.mu.Unlock()

	d.Recover(context.Background())
	d.Wait()

	waitForCounts(t, queue, 1, 0, 0)
	if got := len(runner.invocations()); got != 1 {
		t.Errorf("expected only the interrupted run, got %d runner invocations", got)
	}
}
