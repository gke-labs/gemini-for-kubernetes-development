package chores

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// fakeSource serves agent definitions out of an in-memory map.
type fakeSource struct {
	// files maps a repository path to the raw contents of the definition there.
	files map[string]string
	// listErr, when set, is what ListAgentFiles fails with.
	listErr error
	// lists and reads count the calls, which is how the definition cache is observed.
	lists int
	reads int
}

var _ Source = (*fakeSource)(nil)

// ListAgentFiles returns the known paths in a stable order, so that a test
// asserting on call counts is not also asserting on map iteration order.
func (s *fakeSource) ListAgentFiles(_ context.Context) ([]string, error) {
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	paths := make([]string, 0, len(s.files))
	for path := range s.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *fakeSource) ReadAgentFile(_ context.Context, path string) (string, error) {
	s.reads++
	content, ok := s.files[path]
	if !ok {
		return "", fmt.Errorf("no such file %s", path)
	}
	return content, nil
}

// fakeQueue records what the scheduler enqueues. Its accessors are mutex
// guarded because the Run test observes it while the scheduler's goroutine
// writes to it.
type fakeQueue struct {
	mu sync.Mutex
	// queued holds the tasks handed to Enqueue, keyed by file name.
	queued map[string]*api.QueueTask
	// present is what TaskExists answers, which lets a test model a chore that
	// is still queued or running independently of what this fake was handed.
	present map[string]bool
	// err, when set, is what Enqueue fails with.
	err error
}

var _ Queue = (*fakeQueue)(nil)

func newFakeQueue() *fakeQueue {
	return &fakeQueue{queued: map[string]*api.QueueTask{}, present: map[string]bool{}}
}

func (q *fakeQueue) TaskExists(filename string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.present[filename]
}

func (q *fakeQueue) Enqueue(filename string, task *api.QueueTask) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.queued[filename] = task
	return nil
}

// tasks returns a copy of everything enqueued so far.
func (q *fakeQueue) tasks() map[string]*api.QueueTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	tasks := make(map[string]*api.QueueTask, len(q.queued))
	for name, task := range q.queued {
		tasks[name] = task
	}
	return tasks
}

// forget drops an enqueued task, modelling one that has left the queue.
func (q *fakeQueue) forget(filename string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.queued, filename)
}

// hold makes TaskExists report the named task as still queued or running.
func (q *fakeQueue) hold(filename string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.present[filename] = true
}

// failWith makes every subsequent Enqueue fail.
func (q *fakeQueue) failWith(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.err = err
}

// agentFile renders an agent definition with the frontmatter ParseAgent expects.
func agentFile(name, schedule string) string {
	return fmt.Sprintf("---\nname: %s\nschedule: \"%s\"\n---\nDo the thing.\n", name, schedule)
}

// newTestScheduler builds a scheduler over the given definitions, keeping its
// run state in a temporary directory.
func newTestScheduler(t *testing.T, files map[string]string) (*Scheduler, *fakeSource, *fakeQueue) {
	t.Helper()

	source := &fakeSource{files: files}
	queue := newFakeQueue()
	s := New(Config{
		RefreshInterval: time.Hour,
		StateDir:        t.TempDir(),
		Owner:           "test-owner",
		Repo:            "test-repo",
	}, Deps{Queue: queue, Source: source})
	return s, source, queue
}

func TestScheduleOnceQueuesDueChore(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})

	s.ScheduleOnce(context.Background())

	task, ok := queue.tasks()["task-chore-nightly-triage.yaml"]
	if !ok {
		t.Fatalf("expected the chore to be queued, got %v", queue.tasks())
	}
	if task.Type != api.TypeAgentChore {
		t.Errorf("task type = %q, want %q", task.Type, api.TypeAgentChore)
	}
	if task.AgentFile != ".agents/triage.md" {
		t.Errorf("task agent file = %q, want %q", task.AgentFile, ".agents/triage.md")
	}
	if task.Phase != api.PhaseChores {
		t.Errorf("task phase = %d, want %d", task.Phase, api.PhaseChores)
	}
	if task.TriggerReason != api.TriggerReasonChoreScheduled {
		t.Errorf("task trigger reason = %q, want %q", task.TriggerReason, api.TriggerReasonChoreScheduled)
	}
	if task.URL != "https://github.com/test-owner/test-repo" {
		t.Errorf("task URL = %q, want the repository URL", task.URL)
	}
}

func TestScheduleOnceRecordsTheRunOnDisk(t *testing.T) {
	s, _, _ := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})

	s.ScheduleOnce(context.Background())

	state := loadRunState(s.statePath())
	if state["Nightly Triage"].LastRun.IsZero() {
		t.Fatalf("expected the run to be recorded in %s, got %v", s.statePath(), state)
	}
}

// TestScheduleOnceQueuesADueChoreOnlyOnce covers the case the run state exists
// for: a chore that has been queued and has since left the queue must not be
// queued again until its schedule comes round.
func TestScheduleOnceQueuesADueChoreOnlyOnce(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})

	s.ScheduleOnce(context.Background())
	queue.forget("task-chore-nightly-triage.yaml")
	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected the chore not to be queued again within its schedule, got %v", queue.tasks())
	}
}

func TestScheduleOnceSkipsChoreStillInTheQueue(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})
	queue.hold("task-chore-nightly-triage.yaml")

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected no chore to be queued while the previous one is in flight, got %v", queue.tasks())
	}
}

func TestScheduleOnceSkipsChoreThatIsNotDue(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})
	if err := saveRunState(s.statePath(), map[string]RunState{"Nightly Triage": {LastRun: time.Now()}}); err != nil {
		t.Fatalf("saveRunState failed: %v", err)
	}

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected a chore that ran just now not to be queued, got %v", queue.tasks())
	}
}

func TestScheduleOnceSkipsPausedSchedules(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/paused.md": agentFile("Paused Chore", "never"),
	})

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected a 'never' schedule not to be queued, got %v", queue.tasks())
	}
}

func TestScheduleOnceIgnoresAgentsWithoutASchedule(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/reviewer.md": "---\nname: Reviewer\n---\nReview things.\n",
	})

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected an agent without a schedule not to be a chore, got %v", queue.tasks())
	}
}

// TestScheduleOnceSkipsUnparsableDefinitions checks that one malformed file does
// not take the other chores down with it.
func TestScheduleOnceSkipsUnparsableDefinitions(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/broken.md": "this file has no frontmatter",
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})

	s.ScheduleOnce(context.Background())

	if _, ok := queue.tasks()["task-chore-nightly-triage.yaml"]; !ok {
		t.Errorf("expected the valid chore to still be queued, got %v", queue.tasks())
	}
}

func TestScheduleOnceDryRunQueuesNothing(t *testing.T) {
	source := &fakeSource{files: map[string]string{".agents/triage.md": agentFile("Nightly Triage", "@daily")}}
	queue := newFakeQueue()
	stateDir := t.TempDir()
	s := New(Config{StateDir: stateDir, DryRun: true}, Deps{Queue: queue, Source: source})

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected a dry run not to queue anything, got %v", queue.tasks())
	}
	if _, err := os.Stat(filepath.Join(stateDir, stateFileName)); !os.IsNotExist(err) {
		t.Errorf("expected a dry run not to record a run, stat error = %v", err)
	}
}

func TestScheduleOnceSkipsEverythingWhileDraining(t *testing.T) {
	source := &fakeSource{files: map[string]string{".agents/triage.md": agentFile("Nightly Triage", "@daily")}}
	queue := newFakeQueue()
	s := New(Config{StateDir: t.TempDir()}, Deps{
		Queue:  queue,
		Source: source,
		Paused: func() bool { return true },
	})

	s.ScheduleOnce(context.Background())

	if len(queue.tasks()) != 0 {
		t.Errorf("expected nothing to be queued while draining, got %v", queue.tasks())
	}
	if source.lists != 0 {
		t.Errorf("expected the definitions not to be fetched while draining, got %d listings", source.lists)
	}
}

func TestScheduleOnceReusesDefinitionsWithinRefreshInterval(t *testing.T) {
	s, source, _ := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})

	s.ScheduleOnce(context.Background())
	s.ScheduleOnce(context.Background())

	if source.lists != 1 {
		t.Errorf("expected the definitions to be fetched once, got %d listings", source.lists)
	}
	if source.reads != 1 {
		t.Errorf("expected the definition to be read once, got %d reads", source.reads)
	}
}

func TestScheduleOnceRefetchesDefinitionsAfterRefreshInterval(t *testing.T) {
	source := &fakeSource{files: map[string]string{".agents/triage.md": agentFile("Nightly Triage", "@daily")}}
	s := New(Config{RefreshInterval: time.Nanosecond, StateDir: t.TempDir()}, Deps{Queue: newFakeQueue(), Source: source})

	s.ScheduleOnce(context.Background())
	s.ScheduleOnce(context.Background())

	if source.lists != 2 {
		t.Errorf("expected the definitions to be re-fetched once the cache expired, got %d listings", source.lists)
	}
}

// TestDefinitionsKeepsPreviousCopyWhenRefreshFails covers a GitHub outage: the
// chores that are already known must keep firing, since evaluating their
// schedules needs no network at all.
func TestDefinitionsKeepsPreviousCopyWhenRefreshFails(t *testing.T) {
	source := &fakeSource{files: map[string]string{".agents/triage.md": agentFile("Nightly Triage", "@daily")}}
	s := New(Config{RefreshInterval: time.Nanosecond, StateDir: t.TempDir()}, Deps{Queue: newFakeQueue(), Source: source})

	if got := s.definitions(context.Background()); len(got) != 1 {
		t.Fatalf("expected one definition to be cached, got %v", got)
	}

	source.listErr = errors.New("github is unreachable")
	got := s.definitions(context.Background())

	if len(got) != 1 || got[0].Name != "Nightly Triage" {
		t.Errorf("expected the cached definition to survive a failed refresh, got %v", got)
	}
}

func TestScheduleOnceLeavesRunUnrecordedWhenEnqueueFails(t *testing.T) {
	s, _, queue := newTestScheduler(t, map[string]string{
		".agents/triage.md": agentFile("Nightly Triage", "@daily"),
	})
	queue.failWith(errors.New("queue is full"))

	s.ScheduleOnce(context.Background())

	// The chore never ran, so recording a run would skip it until tomorrow.
	if state := loadRunState(s.statePath()); len(state) != 0 {
		t.Errorf("expected no run to be recorded for a chore that failed to queue, got %v", state)
	}
}

func TestRunSchedulesImmediatelyAndStopsOnCancel(t *testing.T) {
	source := &fakeSource{files: map[string]string{".agents/triage.md": agentFile("Nightly Triage", "@daily")}}
	queue := newFakeQueue()
	s := New(Config{Interval: time.Hour, StateDir: t.TempDir()}, Deps{Queue: queue, Source: source})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// The first cycle runs before the ticker, so the chore is queued without
	// waiting out the interval.
	deadline := time.After(5 * time.Second)
	for len(queue.tasks()) == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for the first scheduling cycle")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
