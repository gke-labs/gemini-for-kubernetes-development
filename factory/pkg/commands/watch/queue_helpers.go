package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func writeTaskAtomically(dir string, filename string, task *QueueTask) error {
	data, err := yaml.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshaling task to YAML: %w", err)
	}

	tempFile := filepath.Join(dir, fmt.Sprintf(".temp-%s", filename))
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("writing temp task file: %w", err)
	}

	targetFile := filepath.Join(dir, filename)
	if err := os.Rename(tempFile, targetFile); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("renaming temp file to target %s: %w", targetFile, err)
	}

	return nil
}

func taskExists(incomingDir, processingDir, filename string) bool {
	if _, err := os.Stat(filepath.Join(incomingDir, filename)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(processingDir, filename)); err == nil {
		return true
	}
	return false
}

func writeTaskJournalEvent(dir string, filename string, task *QueueTask, action string, status time.Duration) {
	journalDir := filepath.Join(dir, "journal")
	_ = os.MkdirAll(journalDir, 0755)

	eventFile := filepath.Join(journalDir, fmt.Sprintf("%s.jsonl", strings.TrimSuffix(filename, ".yaml")))
	f, err := os.OpenFile(eventFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	event := map[string]interface{}{
		"time":   time.Now().Format(time.RFC3339),
		"action": action,
		"task":   task,
		"status": status.Seconds(),
	}

	data, err := json.Marshal(event)
	if err == nil {
		_, _ = f.Write(append(data, '\n'))
	}
}
