package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueHelpers(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Test writeTaskAtomically and taskExists
	task := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/test/repo/issues/1",
		Number:    1,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
	}

	filename := "task-issue-1.yaml"
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)

	if taskExists(incomingDir, processingDir, filename) {
		t.Errorf("task should not exist initially")
	}

	err := writeTaskAtomically(incomingDir, filename, task)
	if err != nil {
		t.Fatalf("failed to write task atomically: %v", err)
	}

	if !taskExists(incomingDir, processingDir, filename) {
		t.Errorf("task should exist in incoming directory after writing")
	}

	// 3. Test writeTaskJournalEvent
	journalDir := filepath.Join(tempDir, "journal")
	writeTaskJournalEvent(tempDir, filename, task, "Created", 10*time.Second)

	journalFile := filepath.Join(journalDir, "task-issue-1.jsonl")
	data, err := os.ReadFile(journalFile)
	if err != nil {
		t.Fatalf("failed to read journal file: %v", err)
	}

	var journalEvent map[string]interface{}
	if err := json.Unmarshal(data, &journalEvent); err != nil {
		t.Fatalf("failed to unmarshal journal event: %v", err)
	}

	if journalEvent["action"] != "Created" {
		t.Errorf("expected action 'Created', got %v", journalEvent["action"])
	}
	if statusVal, ok := journalEvent["status"].(float64); !ok || statusVal != 10.0 {
		t.Errorf("expected status duration 10.0 seconds, got %v", journalEvent["status"])
	}
}

func TestQueueHelpersErrorsAndDrains(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)

	task := &QueueTask{
		Type:   "issue-fix",
		Number: 2,
		Status: "Pending",
	}

	// 1. writeTaskAtomically with invalid directory (error path)
	err := writeTaskAtomically("/nonexistent-dir-path/invalid", "task.yaml", task)
	if err == nil {
		t.Errorf("expected error when writing to invalid directory")
	}

	// 2. taskExists checking processingDir
	filename := "task-issue-2.yaml"
	_ = writeTaskAtomically(processingDir, filename, task)
	if !taskExists(incomingDir, processingDir, filename) {
		t.Errorf("expected taskExists to be true when task is in processing directory")
	}

}
