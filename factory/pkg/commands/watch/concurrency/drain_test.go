package concurrency

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDrainMode(t *testing.T) {
	queueDir := t.TempDir()

	if IsDrainMode(queueDir) {
		t.Errorf("expected IsDrainMode to be false for an empty dir")
	}

	drainFile := filepath.Join(queueDir, ".drain")
	if err := os.WriteFile(drainFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write drain file: %v", err)
	}

	if !IsDrainMode(queueDir) {
		t.Errorf("expected IsDrainMode to be true when a .drain file exists")
	}
}

func TestTaskQueueManager_IsDrainMode(t *testing.T) {
	queueDir := t.TempDir()
	m := NewTaskQueueManager(TaskQueueManagerConfig{QueueDir: queueDir})

	if m.IsDrainMode() {
		t.Errorf("expected IsDrainMode to be false for an empty queue dir")
	}

	if err := os.WriteFile(filepath.Join(queueDir, "do_not_process"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to write drain marker: %v", err)
	}

	if !m.IsDrainMode() {
		t.Errorf("expected IsDrainMode to be true when a do_not_process file exists")
	}
}

func TestIsDrainMode_Env(t *testing.T) {
	t.Setenv("FACTORY_DRAIN", "true")
	if !IsDrainMode(t.TempDir()) {
		t.Errorf("expected IsDrainMode to be true when FACTORY_DRAIN=true")
	}
}
