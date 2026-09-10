package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"gopkg.in/yaml.v3"
)

func TestBuildTaskCommandArgs(t *testing.T) {
	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace:        "test-namespace",
			Image:            "custom-image:v1",
			DiskSize:         "50Gi",
			EphemeralStorage: "20Gi",
			CPURequest:       "2",
			CPULimit:         "4",
			MemoryRequest:    "4Gi",
			MemoryLimit:      "8Gi",
		},
		Flags: Flags{
			TaskTimeout: 30 * time.Minute,
		},
	}

	t.Run("issue-fix task", func(t *testing.T) {
		task := &api.QueueTask{
			Type:   api.TypeIssueFix,
			URL:    "https://github.com/test-owner/test-repo/issues/123",
			Number: 123,
		}
		args := w.buildTaskCommandArgs(task, "coder-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "fix" {
			t.Errorf("expected command 'fix', got %q", args[0])
		}

		expectedFlags := map[string]string{
			"--url":                   task.URL,
			"--instruction":           "Fix this issue",
			"--namespace":             "test-namespace",
			"--user":                  "coder-bot",
			"--image":                 "custom-image:v1",
			"--workspace-disk-size":   "50Gi",
			"--ephemeral-storage":     "20Gi",
			"--cpu-request":           "2",
			"--cpu-limit":             "4",
			"--memory-request":        "4Gi",
			"--memory-limit":          "8Gi",
			"--timeout":               "30m0s",
			"--abort-on-cancel=false": "",
		}

		for flag, expectedVal := range expectedFlags {
			found := false
			for i, a := range args {
				if a == flag {
					found = true
					if expectedVal != "" && i+1 < len(args) && args[i+1] != expectedVal {
						t.Errorf("flag %s has value %q, want %q", flag, args[i+1], expectedVal)
					}
					break
				}
			}
			if !found {
				t.Errorf("missing expected flag %s in args: %v", flag, args)
			}
		}
	})

	t.Run("pr-review task with instructions", func(t *testing.T) {
		task := &api.QueueTask{
			Type:         api.TypePRReview,
			URL:          "https://github.com/test-owner/test-repo/pull/456",
			Number:       456,
			Instructions: []string{"check security", "check unit tests"},
		}
		args := w.buildTaskCommandArgs(task, "reviewer-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "pr" || args[1] != "review" {
			t.Errorf("expected command 'pr review', got %v", args[:2])
		}

		// Verify instructions
		var instructionsFound []string
		for i, a := range args {
			if a == "--instruction" && i+1 < len(args) {
				instructionsFound = append(instructionsFound, args[i+1])
			}
		}
		if len(instructionsFound) != 2 || instructionsFound[0] != "check security" || instructionsFound[1] != "check unit tests" {
			t.Errorf("instructions = %v, want ['check security', 'check unit tests']", instructionsFound)
		}
	})

	t.Run("agent-chore task with session-id", func(t *testing.T) {
		task := &api.QueueTask{
			Type:      api.TypeAgentChore,
			URL:       "https://github.com/test-owner/test-repo/issues/789",
			Number:    789,
			AgentFile: ".agents/chore.md",
			SessionID: "issue-789",
		}
		args := w.buildTaskCommandArgs(task, "chore-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "agent" || args[1] != "create" {
			t.Errorf("expected command 'agent create', got %v", args[:2])
		}

		sessionFound := false
		for i, a := range args {
			if a == "--session-id" && i+1 < len(args) && args[i+1] == "issue-789" {
				sessionFound = true
				break
			}
		}
		if !sessionFound {
			t.Errorf("missing --session-id issue-789 in args: %v", args)
		}
	})

	t.Run("unknown task type returns nil", func(t *testing.T) {
		task := &api.QueueTask{
			Type: "unknown-type",
		}
		args := w.buildTaskCommandArgs(task, "bot")
		if args != nil {
			t.Errorf("expected nil args for unknown type, got %v", args)
		}
	})
}

func TestRunTasks_DryRun_LeavesInIncoming(t *testing.T) {
	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:   tempDir,
			DryRun:     true,
			MaxActions: 10,
			MaxPending: 10,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	fn := "task-issue-1.yaml"
	incomingDir := filepath.Join(tempDir, "incoming")
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		t.Fatalf("failed to create incoming dir: %v", err)
	}
	taskContent := "type: issue-fix\nnumber: 1\nurl: https://github.com/owner/repo/issues/1\npriority: high\n"
	if err := os.WriteFile(filepath.Join(incomingDir, fn), []byte(taskContent), 0644); err != nil {
		t.Fatalf("failed to write incoming task file: %v", err)
	}

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		URL:        "https://github.com/owner/repo/issues/1",
		Number:     1,
		Priority:   api.PriorityHigh,
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue(fn, task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	w.runTasks(context.Background())

	// Under dry run, the task should NOT move to processing. It must remain in incoming!
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s to remain in incoming: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist in processing", fn)
	}

	inc, proc, _ := w.queueMgr.GetCounts()
	if inc != 1 || proc != 0 {
		t.Errorf("expected counts (1, 0), got (%d, %d)", inc, proc)
	}
}

func TestRunTasks_DrainMode_DoesNotClaim(t *testing.T) {
	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:   tempDir,
			MaxActions: 10,
			MaxPending: 10,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	_ = os.WriteFile(filepath.Join(tempDir, ".drain"), []byte(""), 0644)

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     2,
		EnqueuedAt: time.Now(),
	}
	_ = w.queueMgr.Enqueue("task-issue-2.yaml", task)

	w.runTasks(context.Background())

	inc, proc, _ := w.queueMgr.GetCounts()
	if inc != 1 || proc != 0 {
		t.Errorf("expected counts (1, 0), got (%d, %d)", inc, proc)
	}
}

func TestRunDispatcher_DispatchesEnqueuedTask(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	executed := false
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		executed = true
		return exec.CommandContext(ctx, "true")
	}

	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:    tempDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     10,
		URL:        "https://github.com/test-owner/test-repo/issues/10",
		EnqueuedAt: time.Now(),
	}

	if err := w.queueMgr.Enqueue("task-10.yaml", task); err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		_ = w.RunDispatcher(ctx)
	}()

	// Wait for the task to be processed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, done := w.queueMgr.GetCounts()
		if done == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-dispatcherDone

	if !executed {
		t.Errorf("expected execCommand to be called")
	}
	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1), got (%d, %d, %d)", inc, proc, done)
	}
}

func TestRunTasks_SandboxLockRegistry_PreventsConcurrentSameSandbox(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:    tempDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	// Given Repo "test-repo", an issue-fix task for #42 resolves to "fix-test-repo-42"
	w.Repo = RepoFlag{Owner: "test-owner", Repo: "test-repo"}
	sandboxName := "fix-test-repo-42"

	// Pre-acquire the lease
	if !w.sandboxLocks.TryAcquire(sandboxName, "existing-task.yaml") {
		t.Fatalf("failed to pre-acquire lease")
	}

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     42,
		EnqueuedAt: time.Now(),
	}
	fn := "task-issue-42.yaml"
	if err := w.queueMgr.Enqueue(fn, task); err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	// Run tasks - should be skipped because the sandbox is leased
	w.runTasks(context.Background())

	inc, proc, _ := w.queueMgr.GetCounts()
	if inc != 1 || proc != 0 {
		t.Errorf("expected task to remain in incoming while sandbox is leased, got inc=%d, proc=%d", inc, proc)
	}

	// Release the lease
	if !w.sandboxLocks.Release(sandboxName, "existing-task.yaml") {
		t.Fatalf("failed to release pre-acquired lease")
	}

	// Next run: should be claimed and started
	w.runTasks(context.Background())
	w.Wait()

	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected task to complete after lease released, got inc=%d, proc=%d, done=%d", inc, proc, done)
	}

	// Sandbox lock should now be released
	if w.sandboxLocks.IsBusy(sandboxName) {
		t.Errorf("expected sandbox lock to be free after worker completed")
	}
}

func TestWatcher_RunTasks_Standalone(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	executed := false
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		executed = true
		return exec.CommandContext(ctx, "true")
	}

	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:    tempDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     77,
		URL:        "https://github.com/test-owner/test-repo/issues/77",
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue("task-77.yaml", task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	w.runTasks(context.Background())
	w.Wait()

	if !executed {
		t.Errorf("expected execCommand to be called")
	}
	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1), got (%d, %d, %d)", inc, proc, done)
	}
}

func TestWatcher_RunDispatcher_WaitsForWorkers(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	executed := false
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		executed = true
		return exec.CommandContext(ctx, "sleep", "0.05")
	}

	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:    tempDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     88,
		URL:        "https://github.com/test-owner/test-repo/issues/88",
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue("task-88.yaml", task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneChan := make(chan struct{})
	go func() {
		defer close(doneChan)
		_ = w.RunDispatcher(ctx)
	}()

	// Allow dispatcher to pick up task and start worker
	time.Sleep(50 * time.Millisecond)

	// Cancel context while worker is running
	cancel()

	// Wait for RunDispatcher() to return via doneChan
	select {
	case <-doneChan:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for w.RunDispatcher() to return")
	}

	if !executed {
		t.Errorf("expected execCommand to be called")
	}

	// Because RunDispatcher() waited for worker goroutine to complete before returning, task is done
	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1) after RunDispatcher() returned, got (%d, %d, %d)", inc, proc, done)
	}

	// Verify the task actually completed successfully and was not killed
	processedTaskPath := filepath.Join(tempDir, "processed", "task-88.yaml")
	data, err := os.ReadFile(processedTaskPath)
	if err != nil {
		t.Fatalf("failed to read processed task file: %v", err)
	}
	var completedTask api.QueueTask
	if err := yaml.Unmarshal(data, &completedTask); err != nil {
		t.Fatalf("failed to unmarshal processed task: %v", err)
	}
	if completedTask.Status != api.StatusCompleted {
		t.Errorf("expected task status to be %q, got %q", api.StatusCompleted, completedTask.Status)
	}
}

func TestWatcher_RunDispatcher_PeriodicDispatch(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()

	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:    tempDir,
			MaxActions:  10,
			MaxPending:  10,
			TaskTimeout: 30 * time.Minute,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	for _, d := range []string{"incoming", "processing", "processed"} {
		if err := os.MkdirAll(filepath.Join(tempDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		_ = w.RunDispatcher(ctx, WithDispatcherInterval(10*time.Millisecond))
	}()

	// Wait slightly for first dispatch to pass
	time.Sleep(20 * time.Millisecond)

	// Now enqueue a task AFTER initial runTasks has already executed
	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     99,
		URL:        "https://github.com/test-owner/test-repo/issues/99",
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue("task-99.yaml", task); err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	// Poll until the periodic ticker picks up and completes the task
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, done := w.queueMgr.GetCounts()
		if done == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-dispatcherDone

	inc, proc, done := w.queueMgr.GetCounts()
	if inc != 0 || proc != 0 || done != 1 {
		t.Errorf("expected counts (0, 0, 1), got (%d, %d, %d)", inc, proc, done)
	}
}
