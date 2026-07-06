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

	// 2. Test isDoNotProcess
	// Scenario A: default no drain
	if isDoNotProcess(tempDir) {
		t.Errorf("expected isDoNotProcess to be false")
	}

	// Scenario B: environment variable trigger
	t.Setenv("DRAIN", "true")
	if !isDoNotProcess(tempDir) {
		t.Errorf("expected isDoNotProcess to be true when DRAIN=true env var is set")
	}
	t.Setenv("DRAIN", "") // clear

	// Scenario C: check file trigger (.drain)
	drainFile := filepath.Join(tempDir, ".drain")
	_ = os.WriteFile(drainFile, []byte(""), 0644)
	if !isDoNotProcess(tempDir) {
		t.Errorf("expected isDoNotProcess to be true when .drain file exists")
	}
	_ = os.Remove(drainFile)

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

	// 3. isDoNotProcess other environment variables
	envVars := []string{"DO_NOT_PROCESS", "FACTORY_DO_NOT_PROCESS", "FACTORY_DRAIN"}
	for _, env := range envVars {
		t.Setenv(env, "true")
		if !isDoNotProcess(tempDir) {
			t.Errorf("expected isDoNotProcess to be true when %s=true", env)
		}
		t.Setenv(env, "")
	}

	// 4. isDoNotProcess multiple drain file paths
	subPaths := []string{
		".do_not_process",
		"do_not_process",
		".drain",
		"drain",
	}

	for _, sub := range subPaths {
		p := filepath.Join(tempDir, sub)
		err := os.MkdirAll(filepath.Dir(p), 0755)
		if err != nil {
			t.Fatalf("failed to create dir for %s: %v", p, err)
		}
		err = os.WriteFile(p, []byte(""), 0644)
		if err != nil {
			t.Fatalf("failed to write drain file %s: %v", p, err)
		}

		if !isDoNotProcess(tempDir) {
			t.Errorf("expected isDoNotProcess to be true when drain file %s exists", p)
		}

		err = os.Remove(p)
		if err != nil {
			t.Fatalf("failed to remove drain file %s: %v", p, err)
		}
	}
}
