package concurrency

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// A watch upgraded from one that queued factory's tasks under their own
// types and names brings them over when it loads the queue: renamed to
// the rule tasks they are now, with their type, their rule, and the run
// name their old type gave them (a run in flight is followed, not started
// again); a comments task acknowledges feedback. Runs journalled under an
// old name count toward the rule task's history. Other tasks stand.
func TestLoadFromDiskMigratesLegacyTasks(t *testing.T) {
	mgr, dir := setupTestQueueManager(t)
	write := func(sub, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, sub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("incoming", "task-pr-7-investigate.yaml", "type: pr-investigate\nnumber: 7\nurl: https://github.com/o/r/pull/7\ncommitSHA: abcdef123\nenqueuedAt: 2026-10-20T10:00:00Z\nstatus: Pending\nrule: ci\nrecipe: care-ci\n")
	write("processing", "task-issue-8.yaml", "type: issue-fix\nnumber: 8\nurl: https://github.com/o/r/issues/8\nenqueuedAt: 2026-10-20T10:00:00Z\nstatus: Running\nrecipe: fix\n")
	write("processed", "task-pr-9-comments.yaml", "type: pr-comments\nnumber: 9\nurl: https://github.com/o/r/pull/9\nenqueuedAt: 2026-10-20T10:00:00Z\ncompletedAt: 2026-10-20T11:00:00Z\nstatus: Completed\n")
	write("incoming", "task-workflow-x-issue-10.yaml", "type: agent-chore\nnumber: 10\nurl: https://github.com/o/r/issues/10\nstatus: Pending\n")
	journal := `{"taskId":"task-pr-9-comments","event":"Completed","startedAt":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mgr.LoadFromDisk(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sub, old, now, rule, runName string
		typ                          api.TaskType
	}{
		{"incoming", "task-pr-7-investigate.yaml", "task-pr-7-rule-ci.yaml", "ci", "investigate-7-abcdef1-", api.TypePRRule},
		{"processing", "task-issue-8.yaml", "task-issue-8-rule-fix.yaml", "fix", "fix-8-head-", api.TypeIssueRule},
		{"processed", "task-pr-9-comments.yaml", "task-pr-9-rule-comments.yaml", "comments", "comments-9-head-", api.TypePRRule},
	} {
		if _, err := os.Stat(filepath.Join(dir, tc.sub, tc.old)); !os.IsNotExist(err) {
			t.Errorf("%s/%s is still there", tc.sub, tc.old)
		}
		data, err := os.ReadFile(filepath.Join(dir, tc.sub, tc.now))
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.sub, tc.now, err)
		}
		body := string(data)
		for _, want := range []string{"type: " + string(tc.typ), "rule: " + tc.rule, "runName: " + tc.runName} {
			if !strings.Contains(body, want) {
				t.Errorf("%s/%s lacks %q:\n%s", tc.sub, tc.now, want, body)
			}
		}
	}
	if task := mgr.GetProcessedTask("task-pr-9-rule-comments.yaml"); task == nil || task.Acknowledge != "feedback" {
		t.Errorf("the processed comments task = %+v, want it found by its new name, acknowledging feedback", task)
	}
	if !mgr.TaskExists("task-pr-7-rule-ci.yaml") || !mgr.TaskExists("task-workflow-x-issue-10.yaml") {
		t.Error("the queue does not have the migrated task, or lost the workflow step")
	}
	if runs := mgr.EndedRuns("task-pr-9-rule-comments.yaml"); len(runs) != 1 {
		t.Errorf("runs under the new name = %v, want the one journalled under the old", runs)
	}
}

// MigratedTaskID maps the legacy ids, and only those.
func TestMigratedTaskID(t *testing.T) {
	for id, want := range map[string]string{
		"task-pr-7-iterate":          "task-pr-7-rule-conflict",
		"task-pr-7-review":           "task-pr-7-rule-review",
		"task-issue-12":              "task-issue-12-rule-fix",
		"task-pr-7-rule-ci":          "task-pr-7-rule-ci",
		"task-workflow-x-issue-3":    "task-workflow-x-issue-3",
		"task-chore-release-schedul": "task-chore-release-schedul",
	} {
		if got, _ := api.MigratedTaskID(id); got != want {
			t.Errorf("MigratedTaskID(%q) = %q, want %q", id, got, want)
		}
	}
}
