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

	resolve func(taskType api.TaskType, number int) string
	// runningFn, when set, answers IsTaskRunning instead of the running map, so a
	// test can vary the answer per call (e.g. a probe that fails then succeeds).
	// It is called while f.mu is held, so it must not touch the fake itself.
	runningFn    func(sandboxName string) (bool, error)
	running      map[string]bool
	completed    map[string]bool
	runningCount int
	runningErr   error
	completedErr error
	countErr     error
	deleted      []string
}

func newFakeSandboxService() *fakeSandboxService {
	return &fakeSandboxService{
		running:   map[string]bool{},
		completed: map[string]bool{},
	}
}

func (f *fakeSandboxService) ResolveName(_ context.Context, taskType api.TaskType, number int) string {
	if f.resolve != nil {
		return f.resolve(taskType, number)
	}
	return "sandbox"
}

func (f *fakeSandboxService) IsTaskRunning(_ context.Context, sandboxName string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runningFn != nil {
		return f.runningFn(sandboxName)
	}
	return f.running[sandboxName], f.runningErr
}

func (f *fakeSandboxService) IsTaskCompleted(_ context.Context, sandboxName string, _ api.TaskType) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completed[sandboxName], f.completedErr
}

func (f *fakeSandboxService) CountRunningTasks(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runningCount, f.countErr
}

func (f *fakeSandboxService) Delete(_ context.Context, sandboxName string) error {
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

// A sandbox only records the state of the last task of a given type, so a queued
// task must not be resolved from that annotation: by the time it is dispatched the
// annotation may describe some other task that ran in the same sandbox. Triage of
// a stuck task belongs to Recover, which runs before the dispatch loop.
func TestDispatchOnce_RunsRecoveredTaskDespiteCompletedSandboxAnnotation(t *testing.T) {
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

	if got := len(runner.invocations()); got != 1 {
		t.Errorf("expected the recovered task to be executed, got %d invocations", got)
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
	if outcomes := coordinator.outcomes(); len(outcomes) != 1 || outcomes[0] == nil {
		t.Errorf("expected a failure notification for a task that never got to run, got %v", outcomes)
	}
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

// startInterruptedTask dispatches a task whose runner blocks until the dispatcher is
// shut down, mimicking the watch cycle killing the supervising process while the real
// workload keeps running detached inside its sandbox.
func startInterruptedTask(t *testing.T, d *Dispatcher, queue *concurrency.TaskQueueManager, runner *fakeRunner, filename string, number int) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	runner.run = func(runCtx context.Context, _ string, _ *api.QueueTask, _ string) error {
		close(started)
		<-runCtx.Done()
		// What exec.CommandContext reports when it SIGKILLs the child factory CLI.
		return errors.New("signal: killed")
	}

	enqueueTestTask(t, queue, filename, number)
	d.DispatchOnce(ctx)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the task to start")
	}

	cancel()
	d.Wait()
}

func TestDispatchOnce_ShutdownLeavesTaskInProcessingForRecovery(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, nil)

	startInterruptedTask(t, d, queue, runner, "task-issue-9.yaml", 9)

	// The workload is detached and still running in its sandbox, so the task must stay
	// in processing for Recover to triage rather than be buried in processed as Failed.
	waitForCounts(t, queue, 0, 1, 0)

	if _, err := os.Stat(filepath.Join(tempDir, "processing", "task-issue-9.yaml")); err != nil {
		t.Errorf("expected the task file to remain in processing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processed", "task-issue-9.yaml")); err == nil {
		t.Errorf("expected the task file not to be moved to processed")
	}
	if outcomes := coordinator.outcomes(); len(outcomes) != 0 {
		t.Errorf("expected no finish notification for an interrupted task, got %v", outcomes)
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

func TestDispatchOnce_TaskTimeoutStillFailsDuringShutdown(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, sandboxes, _, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.TaskTimeout = 10 * time.Millisecond
	})
	runner.run = func(runCtx context.Context, _ string, _ *api.QueueTask, _ string) error {
		<-runCtx.Done()
		return runCtx.Err()
	}

	enqueueTestTask(t, queue, "task-issue-11.yaml", 11)

	// The parent context stays alive: only the task's own deadline elapses, so the task
	// has genuinely failed and must still be recorded and cleaned up.
	d.DispatchOnce(context.Background())
	d.Wait()

	waitForCounts(t, queue, 0, 0, 1)
	if deleted := sandboxes.deletedSandboxes(); len(deleted) != 1 || deleted[0] != "sandbox" {
		t.Errorf("expected the timed out sandbox to be deleted, got %v", deleted)
	}
}

// runDispatcher starts Run in the background and returns a channel closed when it returns.
func runDispatcher(ctx context.Context, d *Dispatcher) <-chan struct{} {
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = d.Run(ctx)
	}()
	return runDone
}

func awaitRunReturn(t *testing.T, runDone <-chan struct{}) {
	t.Helper()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

func TestRun_DrainLetsInFlightTaskFinishAfterShutdown(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.ShutdownGracePeriod = 10 * time.Second
	})

	started := make(chan struct{})
	release := make(chan struct{})
	runner.run = func(runCtx context.Context, _ string, _ *api.QueueTask, _ string) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-runCtx.Done():
			// The worker was aborted by shutdown instead of being allowed to drain.
			return runCtx.Err()
		}
	}

	enqueueTestTask(t, queue, "task-issue-12.yaml", 12)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := runDispatcher(ctx, d)

	<-started
	cancel()

	// The worker's context is detached from the dispatch loop, so the task keeps
	// running and can still finish normally.
	time.Sleep(50 * time.Millisecond)
	close(release)

	awaitRunReturn(t, runDone)

	waitForCounts(t, queue, 0, 0, 1)
	outcomes := coordinator.outcomes()
	if len(outcomes) != 1 || outcomes[0] != nil {
		t.Errorf("expected the drained task to report success, got %v", outcomes)
	}
}

func TestRun_DrainCancelsTasksThatOutlastGracePeriod(t *testing.T) {
	tempDir := t.TempDir()
	d, queue, _, coordinator, runner := testDispatcher(t, tempDir, func(cfg *Config, _ *Deps) {
		cfg.ShutdownGracePeriod = 50 * time.Millisecond
	})

	started := make(chan struct{})
	runner.run = func(runCtx context.Context, _ string, _ *api.QueueTask, _ string) error {
		close(started)
		<-runCtx.Done()
		return errors.New("signal: killed")
	}

	enqueueTestTask(t, queue, "task-issue-13.yaml", 13)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := runDispatcher(ctx, d)

	<-started
	cancel()
	awaitRunReturn(t, runDone)

	// The grace period expired and the supervisor was cancelled, but the workload may
	// still be running in its sandbox, so the task stays adoptable rather than failed.
	waitForCounts(t, queue, 0, 1, 0)
	if outcomes := coordinator.outcomes(); len(outcomes) != 0 {
		t.Errorf("expected no finish notification for a task cut short by shutdown, got %v", outcomes)
	}
}
