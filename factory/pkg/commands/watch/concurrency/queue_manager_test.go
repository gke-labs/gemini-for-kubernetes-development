package concurrency

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"gopkg.in/yaml.v3"
)

func setupTestQueueManager(t *testing.T) (*TaskQueueManager, string) {
	t.Helper()
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	logsProcDir := filepath.Join(tempDir, "logs", "processing")
	logsDoneDir := filepath.Join(tempDir, "logs", "processed")

	for _, d := range []string{incomingDir, processingDir, processedDir, logsProcDir, logsDoneDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("failed to create test dir %s: %v", d, err)
		}
	}

	mgr := NewTaskQueueManager(TaskQueueManagerConfig{
		QueueDir:         tempDir,
		IncomingDir:      incomingDir,
		ProcessingDir:    processingDir,
		ProcessedDir:     processedDir,
		ProcessingLogDir: logsProcDir,
		ProcessedLogDir:  logsDoneDir,
		DryRun:           false,
	})

	return mgr, tempDir
}

// claimAndStartTask is a test helper that claims a candidate and starts it.
func claimAndStartTask(mgr *TaskQueueManager) (string, *api.QueueTask, error) {
	fn, task, err := mgr.ClaimNextCandidate()
	if err != nil || task == nil {
		return fn, task, err
	}
	if err := mgr.StartTask(fn, task); err != nil {
		_ = mgr.ReleaseTask(fn)
		return "", nil, err
	}
	return fn, task, nil
}

func TestTaskQueueManager_Enqueue(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	task := &api.QueueTask{
		Type:             "issue-fix",
		Number:           101,
		Priority:         "high",
		Phase:            3,
		CreatedAt:        time.Now().Add(-10 * time.Minute),
		TriggerEventTime: time.Now().Add(-5 * time.Minute),
		TriggerReason:    api.TriggerReasonIssueCreated,
		TriggerNotes:     "issue opened by user",
	}

	filename := "task-issue-101.yaml"
	if err := mgr.Enqueue(filename, task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// 1. Verify in-memory state
	if !mgr.TaskExists(filename) {
		t.Errorf("expected task to exist in memory")
	}
	incCount, procCount, doneCount := mgr.GetCounts()
	if incCount != 1 || procCount != 0 || doneCount != 0 {
		t.Errorf("expected counts (1, 0, 0), got (%d, %d, %d)", incCount, procCount, doneCount)
	}

	// 2. Verify disk write-through
	diskFile := filepath.Join(queueDir, "incoming", filename)
	if _, err := os.Stat(diskFile); err != nil {
		t.Errorf("expected task file to exist on disk at %s: %v", diskFile, err)
	}

	// 3. Verify journal.jsonl
	journalFile := filepath.Join(queueDir, "journal.jsonl")
	f, err := os.Open(journalFile)
	if err != nil {
		t.Fatalf("failed to open journal file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatalf("expected journal entry, got EOF")
	}
	var je api.JournalEvent
	if err := json.Unmarshal(scanner.Bytes(), &je); err != nil {
		t.Fatalf("failed to parse journal event: %v", err)
	}
	if je.Event != "Created" || je.TaskID != "task-issue-101" || je.Type != "issue-fix" {
		t.Errorf("unexpected journal entry: %+v", je)
	}

	// 4. Verify deduplication - duplicate enqueue should be a no-op
	if err := mgr.Enqueue(filename, task); err != nil {
		t.Fatalf("duplicate enqueue errored: %v", err)
	}
	incCount, _, _ = mgr.GetCounts()
	if incCount != 1 {
		t.Errorf("expected count to remain 1 after duplicate enqueue, got %d", incCount)
	}
}

func TestTaskQueueManager_ClaimAndStartTask(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	now := time.Now()
	task1 := &api.QueueTask{
		Type:       "issue-fix",
		Number:     10,
		Priority:   "high",
		Phase:      3,
		CreatedAt:  now,
		EnqueuedAt: now.Add(1 * time.Minute),
	}
	task2 := &api.QueueTask{
		Type:       "issue-fix",
		Number:     20,
		Priority:   "low",
		Phase:      3,
		CreatedAt:  now,
		EnqueuedAt: now.Add(2 * time.Minute),
	}

	_ = mgr.Enqueue("task-issue-10.yaml", task1)
	_ = mgr.Enqueue("task-issue-20.yaml", task2)

	fn, claimed, err := claimAndStartTask(mgr)
	if err != nil {
		t.Fatalf("claimAndStartTask failed: %v", err)
	}
	if fn != "task-issue-10.yaml" || claimed.Number != 10 {
		t.Fatalf("expected task-issue-10 to be claimed, got %s (#%d)", fn, claimed.Number)
	}
	if claimed.Status != "Running" || claimed.StartedAt.IsZero() {
		t.Errorf("expected status Running and non-zero StartedAt, got %+v", claimed)
	}

	// Verify disk file moved from incoming to processing
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", "task-issue-10.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected task-issue-10.yaml to be removed from incoming")
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", "task-issue-10.yaml")); err != nil {
		t.Errorf("expected task-issue-10.yaml to exist in processing: %v", err)
	}

	// Claim next candidate - should claim task 20
	fn2, claimed2, err := claimAndStartTask(mgr)
	if err != nil {
		t.Fatalf("claimAndStartTask failed: %v", err)
	}
	if fn2 != "task-issue-20.yaml" || claimed2.Number != 20 {
		t.Fatalf("expected task-issue-20 to be claimed, got %s", fn2)
	}

	// Queue should now have 0 incoming, 2 processing
	incCount, procCount, _ := mgr.GetCounts()
	if incCount != 0 || procCount != 2 {
		t.Errorf("expected counts (0, 2), got (%d, %d)", incCount, procCount)
	}

	// Another claim should return empty
	fn3, claimed3, err := claimAndStartTask(mgr)
	if err != nil || fn3 != "" || claimed3 != nil {
		t.Errorf("expected empty claim, got fn=%q, task=%v, err=%v", fn3, claimed3, err)
	}
}

func TestTaskQueueManager_ReleaseTask_Requeue(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	task := &api.QueueTask{
		Type:       "issue-fix",
		Number:     50,
		Priority:   "high",
		Phase:      3,
		CreatedAt:  time.Now(),
		EnqueuedAt: time.Now(),
	}
	filename := "task-issue-50.yaml"
	_ = mgr.Enqueue(filename, task)

	// Claim it to put in processing
	_, _, _ = claimAndStartTask(mgr)
	_, procCount, _ := mgr.GetCounts()
	if procCount != 1 {
		t.Fatalf("expected 1 task in processing, got %d", procCount)
	}

	// Requeue it via ReleaseTask
	if err := mgr.ReleaseTask(filename); err != nil {
		t.Fatalf("ReleaseTask failed: %v", err)
	}

	incCount, procCount, _ := mgr.GetCounts()
	if incCount != 1 || procCount != 0 {
		t.Errorf("expected counts (1, 0) after requeue, got (%d, %d)", incCount, procCount)
	}

	// Disk check
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", filename)); err != nil {
		t.Errorf("expected file in incoming: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", filename)); !os.IsNotExist(err) {
		t.Errorf("expected file removed from processing")
	}
}

func TestTaskQueueManager_CompleteAndFailTask(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	// Task 1: Complete
	task1 := &api.QueueTask{Type: "issue-fix", Number: 1, Priority: "high"}
	fn1 := "task-issue-1.yaml"
	_ = mgr.Enqueue(fn1, task1)
	_, claimed1, _ := claimAndStartTask(mgr)

	// Create a dummy log file in processing
	logProcFile := filepath.Join(queueDir, "logs", "processing", "task-issue-1.log")
	_ = os.WriteFile(logProcFile, []byte("log data 1"), 0644)

	if err := mgr.CompleteTask(fn1, claimed1); err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}

	// Verify disk moves for task 1
	if _, err := os.Stat(filepath.Join(queueDir, "processed", fn1)); err != nil {
		t.Errorf("expected task file in processed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn1)); !os.IsNotExist(err) {
		t.Errorf("expected task file removed from processing")
	}
	logDoneFile := filepath.Join(queueDir, "logs", "processed", "task-issue-1.log")
	if _, err := os.Stat(logDoneFile); err != nil {
		t.Errorf("expected log file moved to processed: %v", err)
	}

	// Task 2: Fail
	task2 := &api.QueueTask{Type: "issue-fix", Number: 2, Priority: "low"}
	fn2 := "task-issue-2.yaml"
	_ = mgr.Enqueue(fn2, task2)
	_, claimed2, _ := claimAndStartTask(mgr)

	logProcFile2 := filepath.Join(queueDir, "logs", "processing", "task-issue-2.log")
	_ = os.WriteFile(logProcFile2, []byte("log data 2"), 0644)

	if err := mgr.FailTask(fn2, claimed2, "sandbox failed with exit code 1"); err != nil {
		t.Fatalf("FailTask failed: %v", err)
	}

	// Verify disk moves for task 2
	if _, err := os.Stat(filepath.Join(queueDir, "processed", fn2)); err != nil {
		t.Errorf("expected failed task in processed: %v", err)
	}
	logDoneFile2 := filepath.Join(queueDir, "logs", "processed", "task-issue-2.log")
	if _, err := os.Stat(logDoneFile2); err != nil {
		t.Errorf("expected failed task log in processed: %v", err)
	}

	// Memory counts
	inc, proc, done := mgr.GetCounts()
	if inc != 0 || proc != 0 || done != 2 {
		t.Errorf("expected counts (0, 0, 2), got (%d, %d, %d)", inc, proc, done)
	}

	// TaskExists should return false now because it's no longer in incoming or processing
	if mgr.TaskExists(fn1) || mgr.TaskExists(fn2) {
		t.Errorf("expected TaskExists to be false for completed/failed tasks")
	}
}

func TestTaskQueueManager_HasActivePRTask(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	prNum := 77
	if mgr.HasActivePRTask(prNum) {
		t.Errorf("expected false for empty queue")
	}

	task := &api.QueueTask{
		Type:     "pr-comments",
		Number:   prNum,
		Priority: "medium",
	}
	fn := fmt.Sprintf("task-pr-%d-comments.yaml", prNum)
	_ = mgr.Enqueue(fn, task)

	if !mgr.HasActivePRTask(prNum) {
		t.Errorf("expected true when PR task is incoming")
	}

	_, claimed, _ := claimAndStartTask(mgr)
	if !mgr.HasActivePRTask(prNum) {
		t.Errorf("expected true when PR task is processing")
	}

	_ = mgr.CompleteTask(fn, claimed)
	if mgr.HasActivePRTask(prNum) {
		t.Errorf("expected false when PR task is completed")
	}
}

func TestTaskQueueManager_RemoveTaskAndPendingForNumber(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	_ = mgr.Enqueue("task-issue-10.yaml", &api.QueueTask{Type: "issue-fix", Number: 10})
	_ = mgr.Enqueue("task-pr-10-comments.yaml", &api.QueueTask{Type: "pr-comments", Number: 10})
	_ = mgr.Enqueue("task-issue-20.yaml", &api.QueueTask{Type: "issue-fix", Number: 20})

	// Remove pending tasks for number 10
	if err := mgr.RemovePendingTasksForNumber(10); err != nil {
		t.Fatalf("RemovePendingTasksForNumber failed: %v", err)
	}

	if mgr.TaskExists("task-issue-10.yaml") {
		t.Errorf("expected task-issue-10 to be removed")
	}
	if mgr.TaskExists("task-pr-10-comments.yaml") {
		t.Errorf("expected task-pr-10-comments to be removed")
	}
	if !mgr.TaskExists("task-issue-20.yaml") {
		t.Errorf("expected task-issue-20 to remain")
	}

	// Remove single task
	if err := mgr.RemoveTask("task-issue-20.yaml"); err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}
	if mgr.TaskExists("task-issue-20.yaml") {
		t.Errorf("expected task-issue-20 to be removed")
	}
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", "task-issue-20.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected file removed from incoming")
	}
}

func TestTaskQueueManager_LoadFromDisk(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")

	for _, d := range []string{incomingDir, processingDir, processedDir} {
		_ = os.MkdirAll(d, 0755)
	}

	// Seed files
	_ = os.WriteFile(filepath.Join(incomingDir, "task-issue-1.yaml"), []byte("type: issue-fix\nnumber: 1\npriority: high\n"), 0644)
	_ = os.WriteFile(filepath.Join(processingDir, "task-issue-2.yaml"), []byte("type: issue-fix\nnumber: 2\npriority: low\nstatus: Running\n"), 0644)
	_ = os.WriteFile(filepath.Join(processedDir, "task-issue-3.yaml"), []byte("type: issue-fix\nnumber: 3\npriority: medium\nstatus: Completed\n"), 0644)

	mgr := NewTaskQueueManager(TaskQueueManagerConfig{
		QueueDir:      tempDir,
		IncomingDir:   incomingDir,
		ProcessingDir: processingDir,
		ProcessedDir:  processedDir,
	})

	if err := mgr.LoadFromDisk(); err != nil {
		t.Fatalf("LoadFromDisk failed: %v", err)
	}

	inc, proc, done := mgr.GetCounts()
	if inc != 1 || proc != 1 || done != 1 {
		t.Errorf("expected counts (1, 1, 1), got (%d, %d, %d)", inc, proc, done)
	}

	if !mgr.TaskExists("task-issue-1.yaml") {
		t.Errorf("expected task-issue-1 to exist")
	}
	if !mgr.TaskExists("task-issue-2.yaml") {
		t.Errorf("expected task-issue-2 to exist")
	}
}

func TestTaskQueueManager_ConcurrentStress(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	const numWorkers = 20
	const tasksPerWorker = 30

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Concurrently enqueue, claim, complete, check exists, query response
	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < tasksPerWorker; i++ {
				fn := fmt.Sprintf("task-worker-%d-%d.yaml", workerID, i)
				task := &api.QueueTask{
					Type:     "issue-fix",
					Number:   workerID*1000 + i,
					Priority: "high",
				}
				_ = mgr.Enqueue(fn, task)
				_ = mgr.TaskExists(fn)

				// Attempt to claim
				claimFn, claimed, _ := claimAndStartTask(mgr)
				if claimFn != "" && claimed != nil {
					if i%2 == 0 {
						_ = mgr.CompleteTask(claimFn, claimed)
					} else {
						_ = mgr.FailTask(claimFn, claimed, "intentional test failure")
					}
				}

				_ = mgr.GetQueueResponse()
			}
		}()
	}

	wg.Wait()
}

func TestWriteTaskAtomically(t *testing.T) {
	tempDir := t.TempDir()
	task := &api.QueueTask{
		Type:     "pr-review",
		Number:   123,
		Priority: "high",
	}

	filename := "task-pr-123-review.yaml"
	if err := writeTaskAtomically(tempDir, filename, task); err != nil {
		t.Fatalf("writeTaskAtomically failed: %v", err)
	}

	filePath := filepath.Join(tempDir, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read written task file: %v", err)
	}

	var readTask api.QueueTask
	if err := yaml.Unmarshal(data, &readTask); err != nil {
		t.Fatalf("failed to unmarshal task yaml: %v", err)
	}
	if readTask.Number != 123 || readTask.Type != "pr-review" || readTask.Priority != "high" {
		t.Errorf("unexpected task data: %+v", readTask)
	}
}

func TestMoveTaskFileWithDest_WriteError(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	task := &api.QueueTask{
		Type:     "pr-review",
		Number:   123,
		Priority: "high",
	}

	srcFilename := "task-pr-123-review.yaml"
	dstFilename := "task-pr-123-review.yaml"

	// Write initial task to srcDir
	if err := writeTaskAtomically(srcDir, srcFilename, task); err != nil {
		t.Fatalf("failed to write initial task: %v", err)
	}

	// Create a directory where writeTaskAtomically expects to write its temp file in dstDir
	tempFilePath := filepath.Join(dstDir, ".temp-"+dstFilename)
	if err := os.MkdirAll(tempFilePath, 0755); err != nil {
		t.Fatalf("failed to create temp directory to block write: %v", err)
	}

	// Attempt to move the file. os.Rename should succeed but writeTaskAtomically should fail.
	err := moveTaskFileWithDest(srcDir, dstDir, srcFilename, dstFilename, task)
	if err == nil {
		t.Errorf("expected error when writing task state fails, but got nil")
	} else if !strings.Contains(err.Error(), "failed to write updated task state") {
		t.Errorf("expected 'failed to write updated task state' error, got: %v", err)
	}
}

func TestMoveTaskFileWithDest_RemoveError(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	task := &api.QueueTask{
		Type:     "pr-review",
		Number:   124,
		Priority: "high",
	}

	srcFilename := "not-removable-dir"
	dstFilename := "task-pr-124-review.yaml"

	// Create a non-empty directory at srcPath to block os.Remove (returns EISDIR/ENOTEMPTY, not IsNotExist)
	srcPath := filepath.Join(srcDir, srcFilename)
	if err := os.MkdirAll(filepath.Join(srcPath, "subdir"), 0755); err != nil {
		t.Fatalf("failed to create non-empty directory: %v", err)
	}

	// Pre-create dstPath as a regular file so that renaming the src directory to a file fails (ENOTDIR),
	// but writeTaskAtomically (which renames a temp file to dstPath) still succeeds.
	dstPath := filepath.Join(dstDir, dstFilename)
	if err := os.WriteFile(dstPath, []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to pre-create dst file: %v", err)
	}

	// Call moveTaskFileWithDest. Since srcFilename is a directory and dstFilename is a file, os.Rename will fail.
	// writeTaskAtomically will succeed to write to dstFilename.
	// But os.Remove(srcPath) will fail because it's a directory with contents.
	err := moveTaskFileWithDest(srcDir, dstDir, srcFilename, dstFilename, task)
	if err == nil {
		t.Errorf("expected error when removing source file fails, but got nil")
	} else if !strings.Contains(err.Error(), "failed to remove source task file") {
		t.Errorf("expected 'failed to remove source task file' error, got: %v", err)
	}
}

func TestMoveTaskFileWithDest_IgnoreNotExist(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	task := &api.QueueTask{
		Type:     "pr-review",
		Number:   125,
		Priority: "high",
	}

	srcFilename := "non-existent-task.yaml"
	dstFilename := "task-pr-125-review.yaml"

	// Call moveTaskFileWithDest. Since srcFilename does not exist, os.Rename will fail.
	// writeTaskAtomically will succeed to write to dstFilename.
	// os.Remove(srcPath) will fail because srcPath does not exist, but this is ignored.
	err := moveTaskFileWithDest(srcDir, dstDir, srcFilename, dstFilename, task)
	if err != nil {
		t.Errorf("expected no error when source file does not exist (IsNotExist is ignored), but got: %v", err)
	}

	// Verify that dstFilename was indeed successfully written
	dstPath := filepath.Join(dstDir, dstFilename)
	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("expected destination file %s to be written, but got error: %v", dstPath, err)
	}
}

func TestMoveTaskFileWithDest_RenameAndWriteError(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	task := &api.QueueTask{
		Type:     "pr-review",
		Number:   126,
		Priority: "high",
	}

	srcFilename := "non-existent-src-task.yaml"
	dstFilename := "task-pr-126-review.yaml"

	// Create a directory where writeTaskAtomically expects to write its temp file in dstDir
	// to block the fallback write from succeeding.
	tempFilePath := filepath.Join(dstDir, ".temp-"+dstFilename)
	if err := os.MkdirAll(tempFilePath, 0755); err != nil {
		t.Fatalf("failed to create temp directory to block write: %v", err)
	}

	// Call moveTaskFileWithDest. Since srcFilename does not exist, os.Rename will fail.
	// Since .temp-dstFilename is a directory, fallback writeTaskAtomically will fail.
	err := moveTaskFileWithDest(srcDir, dstDir, srcFilename, dstFilename, task)
	if err == nil {
		t.Fatalf("expected error, but got nil")
	}

	// Verify the error contains the rename error ("no such file or directory")
	// and wraps the fallback write error ("writing temp task file").
	errStr := err.Error()
	if !strings.Contains(errStr, "failed to move task to") {
		t.Errorf("expected 'failed to move task to' in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "rename error:") {
		t.Errorf("expected 'rename error:' in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "no such file") {
		t.Errorf("expected 'no such file' (rename error detail) in error, got: %s", errStr)
	}
	if !strings.Contains(errStr, "writing temp task file") {
		t.Errorf("expected 'writing temp task file' (fallback write error detail) in error, got: %s", errStr)
	}
}

func TestTaskQueueManager_SyncIncomingFromDisk(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	// 1. Direct drop onto disk (simulating manual operator copy)
	incomingDir := filepath.Join(queueDir, "incoming")
	manualFile := "task-pr-99-comments.yaml"
	manualTask := &api.QueueTask{
		Type:     "pr-comments",
		Number:   99,
		Priority: "high",
	}
	if err := writeTaskAtomically(incomingDir, manualFile, manualTask); err != nil {
		t.Fatalf("failed to write manual task to disk: %v", err)
	}

	// Before sync, in-memory map doesn't have it
	incCount, _, _ := mgr.GetCounts()
	if incCount != 0 {
		t.Errorf("expected 0 incoming tasks in memory before sync, got %d", incCount)
	}

	// Call SyncIncomingFromDisk
	if err := mgr.SyncIncomingFromDisk(); err != nil {
		t.Fatalf("SyncIncomingFromDisk failed: %v", err)
	}

	// Verify task is now in memory
	if !mgr.TaskExists(manualFile) {
		t.Errorf("expected %s to exist in memory after sync", manualFile)
	}
	item, ok := getTaskFromQueueResponse(mgr, manualFile)
	if !ok || item.QueueState != "incoming" || item.Number != 99 {
		t.Errorf("expected task to be retrieved in incoming state, got ok=%v, item=%+v", ok, item)
	}

	// 2. Sync new disk file via SyncIncomingFromDisk
	manualFile2 := "task-issue-88.yaml"
	manualTask2 := &api.QueueTask{
		Type:     "issue-fix",
		Number:   88,
		Priority: "critical",
	}
	if err := writeTaskAtomically(incomingDir, manualFile2, manualTask2); err != nil {
		t.Fatalf("failed to write manual task 2: %v", err)
	}

	if err := mgr.SyncIncomingFromDisk(); err != nil {
		t.Fatalf("SyncIncomingFromDisk failed: %v", err)
	}
	if !mgr.TaskExists(manualFile2) {
		t.Errorf("expected SyncIncomingFromDisk to sync and find %s", manualFile2)
	}

	// 3. Deletion sync: remove manualFile and manualFile2 from disk
	if err := os.Remove(filepath.Join(incomingDir, manualFile)); err != nil {
		t.Fatalf("failed to remove %s from disk: %v", manualFile, err)
	}
	if err := os.Remove(filepath.Join(incomingDir, manualFile2)); err != nil {
		t.Fatalf("failed to remove %s from disk: %v", manualFile2, err)
	}
	if err := mgr.SyncIncomingFromDisk(); err != nil {
		t.Fatalf("SyncIncomingFromDisk failed: %v", err)
	}
	if mgr.TaskExists(manualFile) {
		t.Errorf("expected %s to be removed from in-memory queue after disk deletion", manualFile)
	}
	if mgr.TaskExists(manualFile2) {
		t.Errorf("expected %s to be removed from in-memory queue after disk deletion", manualFile2)
	}

	// 4. Do not re-ingest tasks that are currently in processing
	taskProc := &api.QueueTask{Type: "issue-fix", Number: 77, Priority: "high"}
	procFn := "task-issue-77.yaml"
	_ = mgr.Enqueue(procFn, taskProc)
	claimedFn, claimed, err := claimAndStartTask(mgr)
	if err != nil || claimed == nil || claimedFn != procFn {
		t.Fatalf("failed to claim task: %v", err)
	}
	// Re-create the file in incoming directory while it is still in processing
	_ = writeTaskAtomically(incomingDir, procFn, taskProc)
	if err := mgr.SyncIncomingFromDisk(); err != nil {
		t.Fatalf("SyncIncomingFromDisk failed: %v", err)
	}
	item77, ok77 := getTaskFromQueueResponse(mgr, procFn)
	if !ok77 || item77.QueueState != "processing" {
		t.Errorf("expected task 77 to remain in processing, got %+v", item77)
	}

	// 5. Dry-run mode preserves in-memory tasks without deleting them
	dryMgr := NewTaskQueueManager(TaskQueueManagerConfig{
		QueueDir:    queueDir,
		IncomingDir: incomingDir,
		DryRun:      true,
	})
	dryTask := &api.QueueTask{Type: "issue-fix", Number: 55, Priority: "medium"}
	dryFn := "task-issue-55.yaml"
	_ = dryMgr.Enqueue(dryFn, dryTask)
	// Task is only in memory, not on disk
	if err := dryMgr.SyncIncomingFromDisk(); err != nil {
		t.Fatalf("SyncIncomingFromDisk in dry run failed: %v", err)
	}
	if !dryMgr.TaskExists(dryFn) {
		t.Errorf("expected dry run in-memory task to not be removed by sync")
	}
}

func TestLoadTaskFromDisk(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("valid task file with explicit values", func(t *testing.T) {
		fixedTime := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
		fn := filepath.Join(tempDir, "valid-task.yaml")
		content := fmt.Sprintf("type: issue-fix\nnumber: 101\npriority: high\nstatus: Running\nenqueuedAt: %s\n", fixedTime.Format(time.RFC3339))
		if err := os.WriteFile(fn, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write task file: %v", err)
		}

		task, _, err := loadTaskFromDisk(fn)
		if err != nil {
			t.Fatalf("unexpected error loading task: %v", err)
		}
		if task.Type != "issue-fix" || task.Number != 101 {
			t.Errorf("unexpected task fields: %+v", task)
		}
		if task.Priority != "high" {
			t.Errorf("expected priority high, got %q", task.Priority)
		}
		if task.Status != "Running" {
			t.Errorf("expected status Running, got %q", task.Status)
		}
		if !task.EnqueuedAt.Equal(fixedTime) {
			t.Errorf("expected enqueuedAt %v, got %v", fixedTime, task.EnqueuedAt)
		}
	})

	t.Run("default priority, status, and enqueuedAt from file modTime", func(t *testing.T) {
		fn := filepath.Join(tempDir, "defaults-task.yaml")
		content := "type: chore\nagentFile: chore.md\n"
		if err := os.WriteFile(fn, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write task file: %v", err)
		}

		task, _, err := loadTaskFromDisk(fn)
		if err != nil {
			t.Fatalf("unexpected error loading task: %v", err)
		}
		if task.Priority != "medium" {
			t.Errorf("expected default priority medium, got %q", task.Priority)
		}
		if task.Status != "Pending" {
			t.Errorf("expected default status Pending, got %q", task.Status)
		}
		if task.EnqueuedAt.IsZero() {
			t.Errorf("expected enqueuedAt to be set from file modTime")
		}
	})

	t.Run("enqueuedAt derived from file modTime via os.Stat", func(t *testing.T) {
		fn := filepath.Join(tempDir, "modtime-task.yaml")
		content := "type: issue-fix\nnumber: 102\n"
		if err := os.WriteFile(fn, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write task file: %v", err)
		}

		customModTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(fn, customModTime, customModTime); err != nil {
			t.Fatalf("failed to set modTime: %v", err)
		}

		task, modTime, err := loadTaskFromDisk(fn)
		if err != nil {
			t.Fatalf("unexpected error loading task: %v", err)
		}
		if !task.EnqueuedAt.Equal(customModTime) {
			t.Errorf("expected enqueuedAt %v, got %v", customModTime, task.EnqueuedAt)
		}
		if !modTime.Equal(customModTime) {
			t.Errorf("expected reported modTime %v, got %v", customModTime, modTime)
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, _, err := loadTaskFromDisk(filepath.Join(tempDir, "does-not-exist.yaml"))
		if err == nil {
			t.Fatalf("expected error for non-existent file, got nil")
		}
		if !os.IsNotExist(err) {
			t.Errorf("expected os.IsNotExist(err) to be true, got %v", err)
		}
	})

	t.Run("invalid yaml syntax", func(t *testing.T) {
		fn := filepath.Join(tempDir, "invalid.yaml")
		if err := os.WriteFile(fn, []byte(":::invalid: yaml:::"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		_, _, err := loadTaskFromDisk(fn)
		if err == nil {
			t.Fatalf("expected error for invalid YAML, got nil")
		}
		if os.IsNotExist(err) {
			t.Errorf("expected os.IsNotExist(err) to be false for yaml error")
		}
	})
}

func TestTaskQueueManager_ReleaseTask_CandidateReclaim(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	fnA := "task-issue-1.yaml"
	taskA := &api.QueueTask{Type: "issue-fix", Number: 1, Priority: "high"}
	_ = mgr.Enqueue(fnA, taskA)

	fnB := "task-issue-2.yaml"
	taskB := &api.QueueTask{Type: "issue-fix", Number: 2, Priority: "medium"}
	_ = mgr.Enqueue(fnB, taskB)

	// Claim task A (highest priority)
	claimedFn, _, err := mgr.ClaimNextCandidate()
	if err != nil || claimedFn != fnA {
		t.Fatalf("expected to claim task A, got %s, err: %v", claimedFn, err)
	}

	// While task A is claimed as candidate, next claim gets task B
	nextFn, nextTask, err := mgr.ClaimNextCandidate()
	if err != nil || nextFn != fnB {
		t.Fatalf("expected to claim task B while A is candidate, got %s, err: %v", nextFn, err)
	}
	if nextTask.Number != 2 {
		t.Errorf("expected task B (number 2), got %d", nextTask.Number)
	}

	// While both are claimed, claiming again yields nothing
	emptyFn, emptyTask, err := mgr.ClaimNextCandidate()
	if err != nil || emptyFn != "" || emptyTask != nil {
		t.Errorf("expected no eligible task while both claimed, got %s", emptyFn)
	}

	// Release task A back to incoming
	if err := mgr.ReleaseTask(fnA); err != nil {
		t.Fatalf("ReleaseTask failed: %v", err)
	}

	// Now task A should be claimable again
	reclaimedFn, reclaimedTask, err := mgr.ClaimNextCandidate()
	if err != nil || reclaimedFn != fnA {
		t.Fatalf("expected to reclaim task A after release, got %s, err: %v", reclaimedFn, err)
	}
	if reclaimedTask.Number != 1 {
		t.Errorf("expected task A (number 1), got %d", reclaimedTask.Number)
	}
}

func TestTaskQueueManager_ReleaseTask_DirectIncoming(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	fn := "task-issue-10.yaml"
	task := &api.QueueTask{Type: "issue-fix", Number: 10, Priority: "high"}
	_ = mgr.Enqueue(fn, task)

	// Claim candidate
	cFn, _, err := mgr.ClaimNextCandidate()
	if err != nil || cFn != fn {
		t.Fatalf("expected to claim candidate, got %s, err: %v", cFn, err)
	}

	// Release task directly while in incoming
	if err := mgr.ReleaseTask(fn); err != nil {
		t.Fatalf("ReleaseTask failed: %v", err)
	}

	// ClaimNextCandidate should claim task again
	cFn2, _, _ := mgr.ClaimNextCandidate()
	if cFn2 != fn {
		t.Errorf("expected to claim task after release, got %s", cFn2)
	}
}

func TestTaskQueueManager_ReleaseTask_CleanupOnTerminalEvents(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	fn1 := "task-issue-11.yaml"
	_ = mgr.Enqueue(fn1, &api.QueueTask{Type: "issue-fix", Number: 11, Priority: "high"})
	_, _, _ = mgr.ClaimNextCandidate()
	_ = mgr.ReleaseTask(fn1)

	// RemoveTask should remove task
	_ = mgr.RemoveTask(fn1)
	if mgr.TaskExists(fn1) {
		t.Errorf("expected RemoveTask to remove task")
	}

	fn2 := "task-issue-12.yaml"
	_ = mgr.Enqueue(fn2, &api.QueueTask{Type: "issue-fix", Number: 12, Priority: "high"})
	_, _, _ = mgr.ClaimNextCandidate()
	_ = mgr.ReleaseTask(fn2)

	// CompleteTask should transition task to processed
	_ = mgr.CompleteTask(fn2, nil)
	item2, _ := getTaskFromQueueResponse(mgr, fn2)
	if item2.QueueState != "processed" {
		t.Errorf("expected CompleteTask to mark task processed")
	}

	fn3 := "task-issue-13.yaml"
	_ = mgr.Enqueue(fn3, &api.QueueTask{Type: "issue-fix", Number: 13, Priority: "high"})
	_, _, _ = mgr.ClaimNextCandidate()
	_ = mgr.ReleaseTask(fn3)

	// FailTask should transition task to processed
	_ = mgr.FailTask(fn3, nil, "some failure")
	item3, _ := getTaskFromQueueResponse(mgr, fn3)
	if item3.QueueState != "processed" {
		t.Errorf("expected FailTask to mark task processed")
	}
}

func TestTaskQueueManager_RemovePendingTasksForNumber_MatchByNumber(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	// Non-standard filename that doesn't follow strict -issue-10.yaml or -pr-10- format, but has task.Number = 10
	fnCustom := "custom-task-special-10.yaml"
	_ = mgr.Enqueue(fnCustom, &api.QueueTask{Type: "issue-fix", Number: 10})

	fnOther := "task-issue-99.yaml"
	_ = mgr.Enqueue(fnOther, &api.QueueTask{Type: "issue-fix", Number: 99})

	if !mgr.TaskExists(fnCustom) {
		t.Fatalf("expected custom task to exist")
	}

	if err := mgr.RemovePendingTasksForNumber(10); err != nil {
		t.Fatalf("RemovePendingTasksForNumber failed: %v", err)
	}

	if mgr.TaskExists(fnCustom) {
		t.Errorf("expected custom task with Number=10 to be removed")
	}
	if !mgr.TaskExists(fnOther) {
		t.Errorf("expected task 99 to remain")
	}
}

func TestTaskQueueManager_TwoPhase_ClaimCandidate_StartTask(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	task := &api.QueueTask{
		Type:       "issue-fix",
		Number:     42,
		Priority:   "high",
		Phase:      3,
		CreatedAt:  time.Now(),
		EnqueuedAt: time.Now(),
	}
	fn := "task-issue-42.yaml"
	if err := mgr.Enqueue(fn, task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// 1. Claim candidate
	claimedFn, candidate, err := mgr.ClaimNextCandidate()
	if err != nil {
		t.Fatalf("ClaimNextCandidate failed: %v", err)
	}
	if claimedFn != fn || candidate.Number != 42 {
		t.Fatalf("expected task 42, got %s (#%d)", claimedFn, candidate.Number)
	}

	// The candidate file must STILL reside in incoming, NOT processing
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s to still exist in incoming: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist in processing yet", fn)
	}

	// Status must remain Pending and StartedAt must be zero
	if candidate.Status != api.StatusPending {
		t.Errorf("expected status Pending, got %s", candidate.Status)
	}
	if !candidate.StartedAt.IsZero() {
		t.Errorf("expected StartedAt to be zero before StartTask, got %v", candidate.StartedAt)
	}

	// Queue counts: incoming=1, processing=0
	incCount, procCount, doneCount := mgr.GetCounts()
	if incCount != 1 || procCount != 0 || doneCount != 0 {
		t.Errorf("expected counts (1, 0, 0), got (%d, %d, %d)", incCount, procCount, doneCount)
	}

	// Second claim should return empty because candidate is reserved
	if cFn, cTask, _ := mgr.ClaimNextCandidate(); cFn != "" || cTask != nil {
		t.Errorf("expected empty claim, got fn=%s", cFn)
	}

	// 2. Release candidate (simulating sandbox busy check failure)
	if err := mgr.ReleaseTask(fn); err != nil {
		t.Fatalf("ReleaseTask failed: %v", err)
	}
	// File must STILL be in incoming
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s to still exist in incoming after release: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist in processing after release", fn)
	}

	// Re-claim candidate
	claimedFn, candidate, err = mgr.ClaimNextCandidate()
	if err != nil || claimedFn != fn {
		t.Fatalf("expected re-claim to succeed, got fn=%s, err=%v", claimedFn, err)
	}

	// 3. Now simulate all checks passing: call StartTask
	if err := mgr.StartTask(fn, candidate); err != nil {
		t.Fatalf("StartTask failed: %v", err)
	}

	// Verify disk move: file removed from incoming, exists in processing
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed from incoming after StartTask", fn)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn)); err != nil {
		t.Errorf("expected %s to exist in processing after StartTask: %v", fn, err)
	}

	// Status and StartedAt should now be updated
	if candidate.Status != api.StatusRunning {
		t.Errorf("expected status Running, got %s", candidate.Status)
	}
	if candidate.StartedAt.IsZero() {
		t.Errorf("expected non-zero StartedAt after StartTask")
	}

	// Counts should now be (0, 1, 0)
	incCount, procCount, doneCount = mgr.GetCounts()
	if incCount != 0 || procCount != 1 || doneCount != 0 {
		t.Errorf("expected counts (0, 1, 0), got (%d, %d, %d)", incCount, procCount, doneCount)
	}

	// 4. Complete the task
	if err := mgr.CompleteTask(fn, candidate); err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processed", fn)); err != nil {
		t.Errorf("expected %s to exist in processed: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s removed from processing after CompleteTask", fn)
	}
}

func TestTaskQueueManager_CompleteDirectlyFromCandidate(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	task := &api.QueueTask{
		Type:       "issue-fix",
		Number:     99,
		Priority:   "medium",
		Recovered:  true,
		EnqueuedAt: time.Now(),
	}
	fn := "task-issue-99.yaml"
	_ = mgr.Enqueue(fn, task)

	// Claim candidate
	claimedFn, candidate, err := mgr.ClaimNextCandidate()
	if err != nil || claimedFn != fn {
		t.Fatalf("ClaimNextCandidate failed: %v", err)
	}

	// Simulate recovered task already completed in sandbox: complete directly without StartTask
	if err := mgr.CompleteTask(fn, candidate); err != nil {
		t.Fatalf("CompleteTask from candidate failed: %v", err)
	}

	// File moved directly from incoming to processed
	if _, err := os.Stat(filepath.Join(queueDir, "incoming", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s removed from incoming", fn)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processed", fn)); err != nil {
		t.Errorf("expected %s in processed: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s never entered processing", fn)
	}
}

// TestProcessedTaskAccessors covers the read side the scanners now depend on.
// They used to open the processed directory themselves; these accessors are
// what replaced that, so they have to answer for both the tasks recovered from
// disk and the ones that finished while the daemon was up.
func TestProcessedTaskAccessors(t *testing.T) {
	t.Run("recovers tasks from disk and dates undated ones", func(t *testing.T) {
		mgr, tempDir := setupTestQueueManager(t)
		processedDir := filepath.Join(tempDir, "processed")

		datedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
		dated := filepath.Join(processedDir, "task-pr-200-comments.yaml")
		if err := os.WriteFile(dated, []byte("type: pr-comments\ncommitSHA: sha200\nstatus: Completed\ncompletedAt: \"2026-08-01T10:00:00Z\"\n"), 0644); err != nil {
			t.Fatalf("writing dated task: %v", err)
		}

		// A task file that never recorded when it finished is dated by its own
		// timestamp, so that a reader never has to go back to the file for it.
		undated := filepath.Join(processedDir, "task-issue-7.yaml")
		if err := os.WriteFile(undated, []byte("type: issue-fix\n"), 0644); err != nil {
			t.Fatalf("writing undated task: %v", err)
		}
		modTime := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
		if err := os.Chtimes(undated, modTime, modTime); err != nil {
			t.Fatalf("setting mtime: %v", err)
		}

		if err := mgr.LoadFromDisk(); err != nil {
			t.Fatalf("LoadFromDisk: %v", err)
		}

		got := mgr.GetProcessedTask("task-pr-200-comments.yaml")
		if got == nil {
			t.Fatal("dated task missing from the processed set")
		}
		if got.CommitSHA != "sha200" || !got.CompletedAt.Equal(datedAt) {
			t.Errorf("dated task recovered as %+v, want sha200 at %v", got, datedAt)
		}

		got = mgr.GetProcessedTask("task-issue-7.yaml")
		if got == nil {
			t.Fatal("undated task missing from the processed set")
		}
		if !got.CompletedAt.Equal(modTime) {
			t.Errorf("undated task dated %v, want the file timestamp %v", got.CompletedAt, modTime)
		}

		if all := mgr.ListProcessedTasks(); len(all) != 2 {
			t.Errorf("listed %d processed tasks, want 2: %v", len(all), all)
		}
	})

	t.Run("reports tasks finished since start-up", func(t *testing.T) {
		mgr, _ := setupTestQueueManager(t)

		task := &api.QueueTask{Type: api.TypeIssueFix, Number: 42}
		if err := mgr.Enqueue("task-issue-42.yaml", task); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if mgr.GetProcessedTask("task-issue-42.yaml") != nil {
			t.Error("a merely queued task was reported as finished")
		}

		if _, _, err := claimAndStartTask(mgr); err != nil {
			t.Fatalf("starting task: %v", err)
		}
		if err := mgr.CompleteTask("task-issue-42.yaml", task); err != nil {
			t.Fatalf("CompleteTask: %v", err)
		}

		got := mgr.GetProcessedTask("task-issue-42.yaml")
		if got == nil {
			t.Fatal("completed task missing from the processed set")
		}
		if got.Status != api.StatusCompleted {
			t.Errorf("completed task has status %q, want %q", got.Status, api.StatusCompleted)
		}
		if got.CompletedAt.IsZero() {
			t.Error("completed task was handed back with no completion time")
		}
	})

	t.Run("hands back copies", func(t *testing.T) {
		mgr, _ := setupTestQueueManager(t)

		task := &api.QueueTask{Type: api.TypeIssueFix, Number: 9, Instructions: []string{"first"}}
		if err := mgr.Enqueue("task-issue-9.yaml", task); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if _, _, err := claimAndStartTask(mgr); err != nil {
			t.Fatalf("starting task: %v", err)
		}
		if err := mgr.CompleteTask("task-issue-9.yaml", task); err != nil {
			t.Fatalf("CompleteTask: %v", err)
		}

		// The queue owns the task files and the state derived from them, so a
		// caller must not be able to edit that state by writing to what it read.
		mgr.GetProcessedTask("task-issue-9.yaml").Status = api.StatusFailed
		mgr.ListProcessedTasks()["task-issue-9.yaml"].CommitSHA = "tampered"

		// A struct copy would share the Instructions backing array, so writing
		// to an element of what was read back would reach queue state too.
		mgr.GetProcessedTask("task-issue-9.yaml").Instructions[0] = "tampered"
		mgr.ListProcessedTasks()["task-issue-9.yaml"].Instructions[0] = "tampered"

		// Nor may the task the caller handed in stay a handle on queue state.
		task.Instructions[0] = "tampered"
		task.CommitSHA = "tampered"

		got := mgr.GetProcessedTask("task-issue-9.yaml")
		if got.Status != api.StatusCompleted {
			t.Errorf("queue state was mutated through GetProcessedTask: status is %q", got.Status)
		}
		if got.CommitSHA != "" {
			t.Errorf("queue state was mutated: commitSHA is %q", got.CommitSHA)
		}
		if got.Instructions[0] != "first" {
			t.Errorf("queue state was mutated through the instructions slice: %q", got.Instructions[0])
		}
	})

	t.Run("unknown task", func(t *testing.T) {
		mgr, _ := setupTestQueueManager(t)
		if got := mgr.GetProcessedTask("task-issue-404.yaml"); got != nil {
			t.Errorf("expected nil for a task that never finished, got %+v", got)
		}
	})
}

func TestTaskQueueManager_FailedCommentsLogUniqueTimestamp(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)

	prNum := 99
	task := &api.QueueTask{
		Type:     "pr-comments",
		Number:   prNum,
		Priority: "high",
	}
	fn := fmt.Sprintf("task-pr-%d-comments.yaml", prNum)
	_ = mgr.Enqueue(fn, task)
	_, claimed, _ := claimAndStartTask(mgr)

	// Create dummy log in processing
	logProcFile := filepath.Join(queueDir, "logs", "processing", fmt.Sprintf("task-pr-%d-comments.log", prNum))
	_ = os.WriteFile(logProcFile, []byte("log failed contents"), 0644)

	if err := mgr.FailTask(fn, claimed, "comments failed"); err != nil {
		t.Fatalf("FailTask failed: %v", err)
	}

	// Verify that the failed log file exists in processed and contains the timestamp suffix
	files, err := os.ReadDir(filepath.Join(queueDir, "logs", "processed"))
	if err != nil {
		t.Fatalf("failed to read processed logs dir: %v", err)
	}

	foundFailedLog := false
	for _, file := range files {
		name := file.Name()
		if strings.HasPrefix(name, "task-pr-99-comments.failed.") && strings.HasSuffix(name, ".log") {
			foundFailedLog = true
			break
		}
	}

	if !foundFailedLog {
		t.Errorf("expected failed comment log with timestamp suffix, none found in processed logs directory")
	}
}

func TestTaskQueueManager_IsRetryableTask(t *testing.T) {
	mgr := NewTaskQueueManager(TaskQueueManagerConfig{})

	tests := []struct {
		filename string
		task     *api.QueueTask
		want     bool
	}{
		{"task-pr-10-comments.yaml", nil, true},
		{"task-pr-999-comments.yaml", nil, true},
		{"task-pr-10-investigate.yaml", nil, false},
		{"task-issue-42.yaml", nil, false},
		{"some-other-file.yaml", nil, false},
		// Check using task property when registered
		{"any-name.yaml", &api.QueueTask{Type: api.TypePRComments}, true},
		{"any-name.yaml", &api.QueueTask{Type: api.TypePRInvestigate}, false},
		// Check task with empty Type falling back to filename-based matching
		{"task-pr-10-comments.yaml", &api.QueueTask{Type: ""}, true},
		{"task-pr-10-investigate.yaml", &api.QueueTask{Type: ""}, false},
	}

	for _, tt := range tests {
		got := mgr.isRetryableTask(tt.filename, tt.task)
		if got != tt.want {
			t.Errorf("isRetryableTask(%q, %+v) = %v, want %v", tt.filename, tt.task, got, tt.want)
		}
	}

	// Test dynamic registration
	mgr.RegisterRetryableTaskType(api.TypePRInvestigate)
	got := mgr.isRetryableTask("any-name.yaml", &api.QueueTask{Type: api.TypePRInvestigate})
	if !got {
		t.Errorf("expected TypePRInvestigate to be retryable after registration")
	}
}
