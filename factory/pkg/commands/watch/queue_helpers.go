package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
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

func isDoNotProcess(queueDir string) bool {
	if os.Getenv("DO_NOT_PROCESS") == "true" || os.Getenv("FACTORY_DO_NOT_PROCESS") == "true" || os.Getenv("DRAIN") == "true" || os.Getenv("FACTORY_DRAIN") == "true" {
		return true
	}
	checkPaths := []string{
		filepath.Join(queueDir, ".do_not_process"),
		filepath.Join(queueDir, "do_not_process"),
		filepath.Join(queueDir, ".drain"),
		filepath.Join(queueDir, "drain"),
		"/workspaces/.do_not_process",
		"/workspaces/do_not_process",
		"/workspaces/.drain",
		"/workspaces/drain",
	}
	for _, p := range checkPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func addGitHubComment(ctx context.Context, client *githubv39.Client, owner, repo string, number int, body string) error {
	comment := &githubv39.IssueComment{Body: &body}
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, comment)
	return err
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
