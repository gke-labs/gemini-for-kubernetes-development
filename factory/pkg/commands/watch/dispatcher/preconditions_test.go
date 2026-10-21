package dispatcher

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
)

// enqueueInvestigate queues a CI investigation decided at head "aaaaaaaa…".
func enqueueInvestigate(t *testing.T, queue *concurrency.TaskQueueManager, filename string, recovered bool) {
	t.Helper()
	task := &api.QueueTask{
		Type:       api.TypePRInvestigate,
		Number:     7,
		URL:        "https://github.com/test-owner/test-repo/pull/7",
		EnqueuedAt: time.Now(),
		CommitSHA:  "aaaaaaaaaaaa",
		Expect:     []string{api.ExpectHead},
		Recovered:  recovered,
	}
	if err := queue.Enqueue(filename, task); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

func journalEvents(t *testing.T, queueDir string) []api.JournalEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(queueDir, "journal.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []api.JournalEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e api.JournalEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// With preconditions on, a task whose head moved while it waited is
// dropped before it starts: never run, journalled as Dropped with the
// reason, served with the queue, and left out of processed, so it counts
// toward no cooldown, retry or give-up, and its name is free again.
func TestStaleTaskIsDroppedNotRun(t *testing.T) {
	dir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, dir, func(cfg *Config, _ *Deps) { cfg.Preconditions = true })
	coordinator.stale = func(*api.QueueTask) (bool, string) { return true, "head moved aaaaaaa→bbbbbbb" }
	enqueueInvestigate(t, queue, "task-pr-7-investigate.yaml", false)

	d.DispatchOnce(context.Background())
	d.Wait()

	if calls := runner.invocations(); len(calls) != 0 {
		t.Fatalf("a stale task ran: %+v", calls)
	}
	waitForCounts(t, queue, 0, 0, 0)
	if queue.GetProcessedTask("task-pr-7-investigate.yaml") != nil {
		t.Error("a dropped task was recorded as processed: it would count as an attempt")
	}
	if queue.TaskExists("task-pr-7-investigate.yaml") {
		t.Error("a dropped task's name is not free for the next scan")
	}
	var dropped *api.JournalEvent
	for _, e := range journalEvents(t, dir) {
		if e.Event == "Dropped" {
			e := e
			dropped = &e
		}
	}
	if dropped == nil || dropped.Error != "head moved aaaaaaa→bbbbbbb" {
		t.Errorf("journal Dropped event = %+v, want the reason", dropped)
	}
	if drops := queue.GetQueueResponse().Drops; len(drops) != 1 || drops[0].Reason != "head moved aaaaaaa→bbbbbbb" || drops[0].Number != 7 {
		t.Errorf("queue drops = %+v", drops)
	}
}

// A task whose facts still hold runs as before.
func TestTaskWhoseFactsHoldRuns(t *testing.T) {
	dir := t.TempDir()
	d, queue, _, _, runner := testDispatcher(t, dir, func(cfg *Config, _ *Deps) { cfg.Preconditions = true })
	enqueueInvestigate(t, queue, "task-pr-7-investigate.yaml", false)

	d.DispatchOnce(context.Background())
	d.Wait()

	if calls := runner.invocations(); len(calls) != 1 {
		t.Fatalf("a task whose facts hold did not run: %+v", calls)
	}
}

// A recovered task was running when the watcher stopped: it is followed by
// its run name, never dropped, whatever its facts say now.
func TestRecoveredTaskIsNeverDropped(t *testing.T) {
	dir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, dir, func(cfg *Config, _ *Deps) { cfg.Preconditions = true })
	coordinator.stale = func(*api.QueueTask) (bool, string) { return true, "head moved" }
	enqueueInvestigate(t, queue, "task-pr-7-investigate.yaml", true)

	d.DispatchOnce(context.Background())
	d.Wait()

	if calls := runner.invocations(); len(calls) != 1 {
		t.Fatalf("a recovered task was not followed: %+v", calls)
	}
	if coordinator.staleAsk != 0 {
		t.Errorf("a recovered task's facts were checked %d times", coordinator.staleAsk)
	}
}

// With preconditions off (the default) nothing is checked: today's
// behaviour exactly.
func TestPreconditionsOffChecksNothing(t *testing.T) {
	dir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, dir, nil)
	coordinator.stale = func(*api.QueueTask) (bool, string) { return true, "head moved" }
	enqueueInvestigate(t, queue, "task-pr-7-investigate.yaml", false)

	d.DispatchOnce(context.Background())
	d.Wait()

	if calls := runner.invocations(); len(calls) != 1 {
		t.Fatalf("with preconditions off the task did not run: %+v", calls)
	}
	if coordinator.staleAsk != 0 {
		t.Errorf("with preconditions off facts were checked %d times", coordinator.staleAsk)
	}
}

// With AtStart, the engine decides again before a first start: a task it
// no longer decides is dropped as a stale one is (journalled, not an
// attempt), one it still decides runs, and a recovered one is never
// asked about. Off, it is never asked.
func TestAtStartDropsWhatTheEngineNoLongerDecides(t *testing.T) {
	for _, tc := range []struct {
		name      string
		atStart   bool
		recovered bool
		drop      bool
		ran, ask  int
	}{
		{"no longer decided: dropped", true, false, true, 0, 1},
		{"still decided: runs", true, false, false, 1, 1},
		{"recovered: followed, not asked", true, true, true, 1, 0},
		{"off: not asked", false, false, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d, queue, _, coordinator, runner := testDispatcher(t, dir, func(cfg *Config, _ *Deps) { cfg.AtStart = tc.atStart })
			coordinator.redecide = func(string) (bool, string) {
				if tc.drop {
					return true, "the rules no longer decide ci: comments took the item"
				}
				return false, ""
			}
			enqueueInvestigate(t, queue, "task-pr-7-investigate.yaml", tc.recovered)

			d.DispatchOnce(context.Background())
			d.Wait()

			if calls := runner.invocations(); len(calls) != tc.ran {
				t.Fatalf("ran %d, want %d", len(calls), tc.ran)
			}
			if len(coordinator.asked) != tc.ask || (tc.ask > 0 && coordinator.asked[0] != "task-pr-7-investigate.yaml") {
				t.Errorf("asked %v, want %d", coordinator.asked, tc.ask)
			}
			if tc.ran == 0 {
				if queue.GetProcessedTask("task-pr-7-investigate.yaml") != nil {
					t.Error("a dropped task was recorded as processed")
				}
				if drops := queue.GetQueueResponse().Drops; len(drops) != 1 || drops[0].Reason != "the rules no longer decide ci: comments took the item" {
					t.Errorf("queue drops = %+v", drops)
				}
			}
		})
	}
}
