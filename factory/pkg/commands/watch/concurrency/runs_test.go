package concurrency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// RunsSince counts the runs of a task that ended (Completed or Failed) and
// started after a time: those already in the journal when the watcher starts,
// and those that end after. A task dropped before it started, or still
// running, is not a run.
func TestRunsSince(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)
	now := time.Now()
	const name = "task-pr-7-rule-ci.yaml"
	id := strings.TrimSuffix(name, ".yaml")

	// The journal a previous watcher left: two ended runs, a start, a drop,
	// another task's run, and a line that does not parse.
	var lines []string
	for _, je := range []api.JournalEvent{
		{TaskID: id, Event: "Completed", StartedAt: now.Add(-3 * time.Hour), Timestamp: now.Add(-150 * time.Minute)},
		{TaskID: id, Event: "Failed", StartedAt: now.Add(-1 * time.Hour), Timestamp: now.Add(-50 * time.Minute)},
		{TaskID: id, Event: "Started", StartedAt: now.Add(-30 * time.Minute), Timestamp: now.Add(-30 * time.Minute)},
		{TaskID: id, Event: "Dropped", Timestamp: now.Add(-20 * time.Minute)},
		{TaskID: "task-pr-8-investigate", Event: "Completed", StartedAt: now.Add(-1 * time.Hour), Timestamp: now.Add(-50 * time.Minute)},
	} {
		b, _ := json.Marshal(je)
		lines = append(lines, string(b))
	}
	lines = append(lines, "not json")
	if err := os.WriteFile(filepath.Join(queueDir, "journal.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := mgr.RunsSince(name, now.Add(-4*time.Hour)); got != 2 {
		t.Errorf("runs in the last 4h = %d, want 2", got)
	}
	if got := mgr.RunsSince(name, now.Add(-2*time.Hour)); got != 1 {
		t.Errorf("runs in the last 2h = %d, want 1 (the earlier one started before)", got)
	}

	// A run that ends now counts too, and a dropped task does not.
	task := &api.QueueTask{Type: api.TypePRRule, Number: 7, URL: "https://github.com/o/r/pull/7"}
	if err := mgr.Enqueue(name, task); err != nil {
		t.Fatal(err)
	}
	fn, started, err := claimAndStartTask(mgr)
	if err != nil || started == nil {
		t.Fatalf("start: %v", err)
	}
	if err := mgr.FailTask(fn, started, "boom"); err != nil {
		t.Fatal(err)
	}
	dropped := &api.QueueTask{Type: api.TypePRRule, Number: 7, URL: "https://github.com/o/r/pull/7"}
	if err := mgr.Enqueue(name, dropped); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DropTask(name, dropped, "head moved"); err != nil {
		t.Fatal(err)
	}
	if got := mgr.RunsSince(name, now.Add(-2*time.Hour)); got != 2 {
		t.Errorf("runs in the last 2h after one more ended and one was dropped = %d, want 2", got)
	}

	// The runs keep their outcomes, oldest first: the journal's, then the
	// one that ended now.
	ended := mgr.EndedRuns(name)
	var outcomes []bool
	for _, r := range ended {
		outcomes = append(outcomes, r.Failed)
	}
	if len(outcomes) != 3 || outcomes[0] || !outcomes[1] || !outcomes[2] {
		t.Errorf("ended runs failed = %v, want [false true true]", outcomes)
	}
}

// A run refused before doing anything (its error says it cannot run here)
// is marked so, from the journal and when it ends now: it is no attempt.
func TestEndedRunsCannotRun(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)
	now := time.Now()
	const name = "task-pr-7-rule-ci.yaml"
	id := strings.TrimSuffix(name, ".yaml")
	var lines []string
	for _, je := range []api.JournalEvent{
		{TaskID: id, Event: "Failed", StartedAt: now.Add(-2 * time.Hour), Timestamp: now.Add(-2 * time.Hour), Error: "recipe care-ci: " + api.CannotRunPrefix + "recipe care-ci runs on a PR you can push to"},
		{TaskID: id, Event: "Failed", StartedAt: now.Add(-time.Hour), Timestamp: now.Add(-50 * time.Minute), Error: "boom"},
	} {
		b, _ := json.Marshal(je)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(queueDir, "journal.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &api.QueueTask{Type: api.TypePRRule, Number: 7, URL: "https://github.com/o/r/pull/7"}
	if err := mgr.Enqueue(name, task); err != nil {
		t.Fatal(err)
	}
	fn, started, err := claimAndStartTask(mgr)
	if err != nil || started == nil {
		t.Fatalf("start: %v", err)
	}
	if err := mgr.FailTask(fn, started, api.CannotRunPrefix+"PR author 'ada' is not in the configured bot pool"); err != nil {
		t.Fatal(err)
	}
	var got []bool
	for _, r := range mgr.EndedRuns(name) {
		got = append(got, r.CannotRun)
	}
	if len(got) != 3 || !got[0] || got[1] || !got[2] {
		t.Errorf("ended runs cannot-run = %v, want [true false true]", got)
	}
}
