package dispatcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
)

// fakeSandboxService is an in-memory SandboxService for dispatcher tests.
type fakeSandboxService struct {
	mu sync.Mutex

	resolve      func(taskType api.TaskType, number int) string
	running      map[string]bool
	completed    map[string]bool
	runningCount int
	runningErr   error
	countErr     error
	deleted      []string
}

func newFakeSandboxService() *fakeSandboxService {
	return &fakeSandboxService{
		running:   map[string]bool{},
		completed: map[string]bool{},
	}
}

func (f *fakeSandboxService) ResolveSandboxName(_ context.Context, taskType api.TaskType, number int) string {
	if f.resolve != nil {
		return f.resolve(taskType, number)
	}
	return "sandbox"
}

func (f *fakeSandboxService) IsTaskRunning(_ context.Context, sandboxName string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[sandboxName], f.runningErr
}

func (f *fakeSandboxService) IsTaskCompleted(_ context.Context, sandboxName string, _ api.TaskType) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completed[sandboxName], nil
}

func (f *fakeSandboxService) CountRunningTasks(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runningCount, f.countErr
}

func (f *fakeSandboxService) DeleteSandbox(_ context.Context, sandboxName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, sandboxName)
	return nil
}

func (f *fakeSandboxService) deletedSandboxes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// fakeCoordinator is a TaskCoordinator that records the lifecycle callbacks it receives.
type fakeCoordinator struct {
	mu sync.Mutex

	cancel   func(task *api.QueueTask) (bool, string)
	user     string
	userErr  error
	started  []int
	finished []error
}

func (f *fakeCoordinator) ShouldCancelTask(_ context.Context, task *api.QueueTask) (bool, string) {
	if f.cancel != nil {
		return f.cancel(task)
	}
	return false, ""
}

func (f *fakeCoordinator) SelectUser(context.Context, *api.QueueTask) (string, error) {
	return f.user, f.userErr
}

func (f *fakeCoordinator) NotifyTaskStarted(_ context.Context, task *api.QueueTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, task.Number)
}

func (f *fakeCoordinator) NotifyTaskFinished(_ context.Context, _ *api.QueueTask, taskErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, taskErr)
}

func (f *fakeCoordinator) outcomes() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.finished...)
}

// fakeRunner is a TaskRunner that records invocations instead of spawning processes.
type fakeRunner struct {
	mu sync.Mutex

	run   func(ctx context.Context, taskFilename string, task *api.QueueTask, selectedUser string) error
	calls []fakeRunnerCall
}

type fakeRunnerCall struct {
	filename string
	user     string
}

func (f *fakeRunner) Run(ctx context.Context, taskFilename string, task *api.QueueTask, selectedUser string) error {
	f.mu.Lock()
	f.calls = append(f.calls, fakeRunnerCall{filename: taskFilename, user: selectedUser})
	f.mu.Unlock()

	if f.run != nil {
		return f.run(ctx, taskFilename, task, selectedUser)
	}
	return nil
}

func (f *fakeRunner) invocations() []fakeRunnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRunnerCall(nil), f.calls...)
}

// testDispatcher builds a Dispatcher backed by a queue rooted at queueDir.
func testDispatcher(t *testing.T, queueDir string, mutate func(cfg *Config, deps *Deps)) (*Dispatcher, *concurrency.TaskQueueManager, *fakeSandboxService, *fakeCoordinator, *fakeRunner) {
	t.Helper()

	for _, d := range []string{"incoming", "processing", "processed", "logs/processing", "logs/processed"} {
		if err := os.MkdirAll(filepath.Join(queueDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	queue := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir:         queueDir,
		IncomingDir:      filepath.Join(queueDir, "incoming"),
		ProcessingDir:    filepath.Join(queueDir, "processing"),
		ProcessedDir:     filepath.Join(queueDir, "processed"),
		ProcessingLogDir: filepath.Join(queueDir, "logs", "processing"),
		ProcessedLogDir:  filepath.Join(queueDir, "logs", "processed"),
	})

	sandboxes := newFakeSandboxService()
	coordinator := &fakeCoordinator{}
	runner := &fakeRunner{}

	cfg := Config{
		Interval:    10 * time.Millisecond,
		MaxActions:  10,
		MaxPending:  10,
		TaskTimeout: 30 * time.Minute,
	}
	deps := Deps{
		Queue:        queue,
		SandboxLocks: concurrency.NewSandboxLockRegistry(),
		Sandboxes:    sandboxes,
		Coordinator:  coordinator,
		Runner:       runner,
	}
	if mutate != nil {
		mutate(&cfg, &deps)
	}

	return New(cfg, deps), queue, sandboxes, coordinator, runner
}

func enqueueTestTask(t *testing.T, queue *concurrency.TaskQueueManager, filename string, number int) *api.QueueTask {
	t.Helper()
	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     number,
		URL:        "https://github.com/test-owner/test-repo/issues/1",
		EnqueuedAt: time.Now(),
	}
	if err := queue.Enqueue(filename, task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	return task
}

// waitForCounts polls the queue until the expected counts are observed or the deadline passes.
func waitForCounts(t *testing.T, queue *concurrency.TaskQueueManager, incoming, processing, processed int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inc, proc, done := queue.GetCounts()
		if inc == incoming && proc == processing && done == processed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	inc, proc, done := queue.GetCounts()
	t.Fatalf("expected counts (%d, %d, %d), got (%d, %d, %d)", incoming, processing, processed, inc, proc, done)
}

func TestDispatchOnce_ExecutesTaskAndCompletesIt(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, nil)
	coordinator.user = "coder-bot"

	enqueueTestTask(t, queue, "task-issue-1.yaml", 1)

	d.DispatchOnce(context.Background())
	d.Wait()

	calls := runner.invocations()
	if len(calls) != 1 || calls[0].filename != "task-issue-1.yaml" {
		t.Fatalf("expected runner to be invoked once for the task, got %+v", calls)
	}
	if calls[0].user != "coder-bot" {
		t.Errorf("expected task to run as 'coder-bot', got %q", calls[0].user)
	}
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected a single successful completion notification, got %v", outcomes)
	}
	waitForCounts(t, queue, 0, 0, 1)
}

func TestDispatchOnce_DryRunLeavesTaskInIncoming(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.DryRun = true
	})

	fn := "task-issue-1.yaml"
	enqueueTestTask(t, queue, fn, 1)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no task execution under dry run")
	}

	// Under dry run, the task should NOT move to processing. It must remain in incoming!
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s to remain in incoming: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist in processing", fn)
	}
	waitForCounts(t, queue, 1, 0, 0)
}

func TestDispatchOnce_DrainModeDoesNotClaim(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, _, runner := testDispatcher(t, tempDir, nil)

	if err := os.WriteFile(filepath.Join(tempDir, ".drain"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to write drain file: %v", err)
	}

	enqueueTestTask(t, queue, "task-issue-2.yaml", 2)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no task execution while draining")
	}
	waitForCounts(t, queue, 1, 0, 0)
}

func TestDispatchOnce_SkipsTaskWhileSandboxIsLeased(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, _ := testDispatcher(t, tempDir, nil)
	sandboxes.resolve = func(_ api.TaskType, number int) string {
		return "fix-test-repo-42"
	}

	if !d.sandboxLocks.TryAcquire("fix-test-repo-42", "existing-task.yaml") {
		t.Fatalf("failed to pre-acquire lease")
	}

	fn := "task-issue-42.yaml"
	enqueueTestTask(t, queue, fn, 42)

	d.DispatchOnce(context.Background())
	d.Wait()
	waitForCounts(t, queue, 1, 0, 0)

	if !d.sandboxLocks.Release("fix-test-repo-42", "existing-task.yaml") {
		t.Fatalf("failed to release pre-acquired lease")
	}

	d.DispatchOnce(context.Background())
	d.Wait()
	waitForCounts(t, queue, 0, 0, 1)

	if d.sandboxLocks.IsBusy("fix-test-repo-42") {
		t.Errorf("expected sandbox lease to be released after the worker completed")
	}
}

func TestDispatchOnce_SkipsTaskWhileSandboxIsRunning(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, nil)
	sandboxes.running["sandbox"] = true

	enqueueTestTask(t, queue, "task-issue-3.yaml", 3)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no task execution while the sandbox is running another task")
	}
	waitForCounts(t, queue, 1, 0, 0)
}

func TestDispatchOnce_RemovesCancelledTask(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, nil)
	coordinator.cancel = func(*api.QueueTask) (bool, string) {
		return true, "target #4 is closed"
	}

	enqueueTestTask(t, queue, "task-issue-4.yaml", 4)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no task execution for a cancelled task")
	}
	waitForCounts(t, queue, 0, 0, 0)
}

func TestDispatchOnce_CompletesRecoveredTaskAlreadyDoneInSandbox(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, nil)
	sandboxes.completed["sandbox"] = true

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     5,
		Recovered:  true,
		EnqueuedAt: time.Now(),
	}
	if err := queue.Enqueue("task-issue-5.yaml", task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no re-execution of a task that already completed in its sandbox")
	}
	waitForCounts(t, queue, 0, 0, 1)
}

func TestDispatchOnce_RespectsMaxActions(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.MaxActions = 1
	})
	sandboxes.resolve = func(_ api.TaskType, number int) string {
		return "sandbox-" + string(rune('a'+number))
	}

	enqueueTestTask(t, queue, "task-issue-1.yaml", 1)
	enqueueTestTask(t, queue, "task-issue-2.yaml", 2)

	d.DispatchOnce(context.Background())
	d.Wait()

	if got := len(runner.invocations()); got != 1 {
		t.Errorf("expected exactly 1 dispatched task with MaxActions=1, got %d", got)
	}
	waitForCounts(t, queue, 1, 0, 1)
}

func TestDispatchOnce_RespectsMaxPending(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.MaxPending = 1
	})
	sandboxes.runningCount = 1

	enqueueTestTask(t, queue, "task-issue-1.yaml", 1)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no dispatch while the pending sandbox limit is reached")
	}
	waitForCounts(t, queue, 1, 0, 0)
}

func TestDispatchOnce_FailedTaskIsMarkedFailed(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, nil)
	runner.run = func(context.Context, string, *api.QueueTask, string) error {
		return errors.New("task blew up")
	}

	enqueueTestTask(t, queue, "task-issue-6.yaml", 6)

	d.DispatchOnce(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	outcomes := coordinator.outcomes()
	if len(outcomes) != 1 || outcomes[0] == nil {
		t.Errorf("expected a single failure notification, got %v", outcomes)
	}
}

func TestDispatchOnce_TimedOutTaskDeletesSandbox(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.TaskTimeout = 10 * time.Millisecond
	})
	runner.run = func(ctx context.Context, _ string, _ *api.QueueTask, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}

	enqueueTestTask(t, queue, "task-issue-7.yaml", 7)

	d.DispatchOnce(context.Background())
	d.Wait()

	if deleted := sandboxes.deletedSandboxes(); len(deleted) != 1 || deleted[0] != "sandbox" {
		t.Errorf("expected the timed out sandbox to be deleted, got %v", deleted)
	}
	waitForCounts(t, queue, 0, 0, 1)
}

func TestDispatchOnce_UserSelectionFailureFailsTask(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, nil)
	coordinator.userErr = errors.New("no eligible bot")

	enqueueTestTask(t, queue, "task-issue-8.yaml", 8)

	d.DispatchOnce(context.Background())
	d.Wait()

	if len(runner.invocations()) != 0 {
		t.Errorf("expected no execution when user selection fails")
	}
	waitForCounts(t, queue, 0, 0, 1)
}

func TestRun_DispatchesTaskEnqueuedAfterStartup(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, _, _ := testDispatcher(t, tempDir, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		_ = d.Run(ctx)
	}()

	// Enqueue only after the initial dispatch cycle has already run
	time.Sleep(20 * time.Millisecond)
	enqueueTestTask(t, queue, "task-issue-99.yaml", 99)

	waitForCounts(t, queue, 0, 0, 1)

	cancel()
	<-dispatcherDone
}

func TestRun_WaitsForInFlightWorkers(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, _, runner := testDispatcher(t, tempDir, nil)
	// The worker intentionally ignores cancellation so we can observe that Run drains it.
	runner.run = func(context.Context, string, *api.QueueTask, string) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	enqueueTestTask(t, queue, "task-issue-88.yaml", 88)

	ctx, cancel := context.WithCancel(context.Background())
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		_ = d.Run(ctx)
	}()

	// Allow the dispatcher to claim the task and start the worker
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-dispatcherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}

	// Run only returns once in-flight workers are done, so the task is already recorded as processed.
	inc, proc, done := queue.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1) after Run returned, got (%d, %d, %d)", inc, proc, done)
	}
}

func TestNewTaskDispatcher_DefaultsInterval(t *testing.T) {
	d := New(Config{}, Deps{})
	if d.cfg.Interval != DefaultInterval {
		t.Errorf("expected interval to default to %s, got %s", DefaultInterval, d.cfg.Interval)
	}
}
