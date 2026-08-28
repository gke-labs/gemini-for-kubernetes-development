package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

// getEnqueueTime returns the timestamp when a task was enqueued, falling back to
// file modification time or task creation time if EnqueuedAt is unset.
func getEnqueueTime(t *QueueTask, modTime time.Time) time.Time {
	if !t.EnqueuedAt.IsZero() {
		return t.EnqueuedAt
	}
	if !modTime.IsZero() {
		return modTime
	}
	return t.CreatedAt
}

// getEntityKey returns the unique key representing the target entity (PR, issue, or chore)
// for a given queue task.
func getEntityKey(t *QueueTask) string {
	if t.Number > 0 {
		return fmt.Sprintf("%d", t.Number)
	}
	if t.AgentFile != "" {
		return fmt.Sprintf("chore:%s", t.AgentFile)
	}
	if t.URL != "" {
		return fmt.Sprintf("url:%s", t.URL)
	}
	if t.Type != "" {
		return fmt.Sprintf("type:%s", t.Type)
	}
	return "default"
}

// priorityRankValue converts a priority string into an integer rank where lower numbers indicate higher priority.
func priorityRankValue(p string) int {
	priorityRank := map[string]int{
		"critical":  1,
		"urgent":    2,
		"important": 3,
		"high":      4,
		"medium":    5,
		"low":       6,
	}
	if r, ok := priorityRank[strings.ToLower(p)]; ok {
		return r
	}
	return 5
}

// isLessTask reports whether task a should be ordered before task b based on priority rank,
// phase rank, enqueue timestamp (FIFO), creation timestamp, and filename tiebreaking.
func isLessTask(a, b taskItem) bool {
	rankA := priorityRankValue(a.task.Priority)
	rankB := priorityRankValue(b.task.Priority)
	if rankA != rankB {
		return rankA < rankB
	}

	phaseA := a.task.Phase
	if phaseA == 0 {
		phaseA = 3
	}
	phaseB := b.task.Phase
	if phaseB == 0 {
		phaseB = 3
	}
	if phaseA != phaseB {
		return phaseA < phaseB
	}

	if !a.task.EnqueuedAt.Equal(b.task.EnqueuedAt) {
		return a.task.EnqueuedAt.Before(b.task.EnqueuedAt)
	}

	if !a.task.CreatedAt.Equal(b.task.CreatedAt) {
		return a.task.CreatedAt.Before(b.task.CreatedAt)
	}

	return a.filename < b.filename
}

// sortTasksFairly sorts queue tasks using a hybrid round-robin algorithm across entity buckets
// while maintaining FIFO arrival order, priority ranks, and phase dependencies within each entity.
func sortTasksFairly(items []taskItem) []taskItem {
	if len(items) <= 1 {
		return items
	}

	bucketsMap := make(map[string][]taskItem)

	for _, item := range items {
		key := getEntityKey(item.task)
		bucketsMap[key] = append(bucketsMap[key], item)
	}

	for key, bucket := range bucketsMap {
		sort.SliceStable(bucket, func(i, j int) bool {
			return isLessTask(bucket[i], bucket[j])
		})
		bucketsMap[key] = bucket
	}

	var result []taskItem

	for len(bucketsMap) > 0 {
		var activeKeys []string
		for key := range bucketsMap {
			activeKeys = append(activeKeys, key)
		}

		sort.SliceStable(activeKeys, func(i, j int) bool {
			headI := bucketsMap[activeKeys[i]][0]
			headJ := bucketsMap[activeKeys[j]][0]
			if isLessTask(headI, headJ) {
				return true
			}
			if isLessTask(headJ, headI) {
				return false
			}
			return activeKeys[i] < activeKeys[j]
		})

		for _, key := range activeKeys {
			bucket := bucketsMap[key]
			result = append(result, bucket[0])
			if len(bucket) == 1 {
				delete(bucketsMap, key)
			} else {
				bucketsMap[key] = bucket[1:]
			}
		}
	}

	return result
}

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

func getIssuePriority(issue *githubv39.Issue) string {
	for _, l := range issue.Labels {
		name := l.GetName()
		if strings.HasPrefix(name, "priority/") {
			return strings.TrimPrefix(name, "priority/")
		}
	}
	return "medium"
}

func getPRPriority(prIssue *githubv39.Issue) string {
	return getIssuePriority(prIssue)
}

func writeTaskJournalEvent(queueDir string, taskFilename string, task *QueueTask, event string, duration time.Duration) {
	journalPath := filepath.Join(queueDir, "journal.jsonl")
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		klog.Errorf("Failed to open journal file: %v", err)
		return
	}
	defer f.Close()

	je := JournalEvent{
		Timestamp:        time.Now(),
		TaskID:           strings.TrimSuffix(taskFilename, ".yaml"),
		Event:            event,
		Type:             task.Type,
		URL:              task.URL,
		Priority:         task.Priority,
		TriggerEventTime: task.TriggerEventTime,
		TriggerReason:    task.TriggerReason,
		TriggerNotes:     task.TriggerNotes,
		StartedAt:        task.StartedAt,
		CompletedAt:      task.CompletedAt,
		Error:            task.Error,
	}
	if duration > 0 {
		je.DurationSecond = duration.Seconds()
	}

	data, err := json.Marshal(je)
	if err != nil {
		klog.Errorf("Failed to marshal journal event: %v", err)
		return
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		klog.Errorf("Failed to write journal event: %v", err)
	}
}

func parseProcessedPRTask(filePath string, name string, fInfo os.FileInfo, state prWatchState) prWatchState {
	isComments := strings.HasSuffix(name, "-comments")
	isInvestigate := strings.HasSuffix(name, "-investigate")
	isReview := strings.HasSuffix(name, "-review")
	isIterate := strings.HasSuffix(name, "-iterate")

	var t QueueTask
	hasTask := false
	if data, err := os.ReadFile(filePath); err == nil {
		if err := yaml.Unmarshal(data, &t); err == nil {
			hasTask = true
			if strings.EqualFold(t.Status, "Failed") {
				return state
			}
		}
	}

	if fInfo != nil {
		tTime := fInfo.ModTime()
		if hasTask && !t.CompletedAt.IsZero() {
			tTime = t.CompletedAt
		}
		if isComments {
			if tTime.After(state.lastCommentAddressedTime) {
				state.lastCommentAddressedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastCommentAddressedSHA = t.CommitSHA
			}
		} else if isInvestigate {
			if tTime.After(state.lastInvestigatedTime) {
				state.lastInvestigatedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastInvestigatedSHA = t.CommitSHA
			}
		} else if isReview {
			if hasTask && t.CommitSHA != "" {
				state.lastReviewedSHA = t.CommitSHA
			}
		} else if isIterate {
			if tTime.After(state.lastIteratedTime) {
				state.lastIteratedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastIteratedSHA = t.CommitSHA
			}
		}
	}
	return state
}

func removePendingTasksForNumber(incomingDir string, number int) {
	files, err := os.ReadDir(incomingDir)
	if err != nil {
		return
	}
	pattern1 := fmt.Sprintf("-issue-%d.yaml", number)
	pattern2 := fmt.Sprintf("-pr-%d-", number)
	for _, f := range files {
		if !f.IsDir() && (strings.Contains(f.Name(), pattern1) || strings.Contains(f.Name(), pattern2)) {
			_ = os.Remove(filepath.Join(incomingDir, f.Name()))
		}
	}
}

// hasActivePRTask reports whether any task for the given PR number is currently
// pending in the incoming queue or executing in the processing directory.
func hasActivePRTask(incomingDir, processingDir string, number int) bool {
	prefix := fmt.Sprintf("task-pr-%d-", number)
	for _, dir := range []string{incomingDir, processingDir} {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasPrefix(f.Name(), prefix) && strings.HasSuffix(f.Name(), ".yaml") {
				return true
			}
		}
	}
	return false
}

func isPRTask(taskType string) bool {
	return taskType == "pr-investigate" || taskType == "pr-comments" || taskType == "pr-iterate"
}

func loadProcessedTasks(processedDir string) (map[int]time.Time, map[int]prWatchState) {
	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)
	files, err := os.ReadDir(processedDir)
	if err != nil {
		return processedIssues, processedPRs
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		filePath := filepath.Join(processedDir, f.Name())
		if strings.HasPrefix(f.Name(), "task-issue-") {
			trimmed := strings.TrimPrefix(f.Name(), "task-issue-")
			trimmed = strings.TrimSuffix(trimmed, ".yaml")
			if num, err := strconv.Atoi(trimmed); err == nil {
				var t QueueTask
				hasTask := false
				if data, err := os.ReadFile(filePath); err == nil {
					if err := yaml.Unmarshal(data, &t); err == nil {
						hasTask = true
					}
				}
				if hasTask && strings.EqualFold(t.Status, "Failed") {
					continue
				}
				if info, err := f.Info(); err == nil {
					tTime := info.ModTime()
					if hasTask && !t.CompletedAt.IsZero() {
						tTime = t.CompletedAt
					}
					processedIssues[num] = tTime
				}
			}
		} else if strings.HasPrefix(f.Name(), "task-pr-") {
			name := strings.TrimPrefix(f.Name(), "task-pr-")
			name = strings.TrimSuffix(name, ".yaml")

			isComments := strings.HasSuffix(name, "-comments")
			isInvestigate := strings.HasSuffix(name, "-investigate")
			isReview := strings.HasSuffix(name, "-review")
			isIterate := strings.HasSuffix(name, "-iterate")

			var numStr string
			if isComments {
				numStr = strings.TrimSuffix(name, "-comments")
			} else if isInvestigate {
				numStr = strings.TrimSuffix(name, "-investigate")
			} else if isReview {
				numStr = strings.TrimSuffix(name, "-review")
			} else if isIterate {
				numStr = strings.TrimSuffix(name, "-iterate")
			}

			if numStr != "" {
				if num, err := strconv.Atoi(numStr); err == nil {
					state := processedPRs[num]
					info, _ := f.Info()
					processedPRs[num] = parseProcessedPRTask(filePath, name, info, state)
				}
			}
		}
	}
	return processedIssues, processedPRs
}

func (w *Watcher) recoverStuckTasks(ctx context.Context) {
	files, err := os.ReadDir(w.processingDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), "task-") && strings.HasSuffix(f.Name(), ".yaml") {
			processingPath := filepath.Join(w.processingDir, f.Name())

			// Read the task
			if data, err := os.ReadFile(processingPath); err == nil {
				var t QueueTask
				if err := yaml.Unmarshal(data, &t); err == nil {
					sandboxName := w.resolveSandboxName(ctx, t.Type, t.Number)
					if w.kubeClient != nil && sandboxName != "" {
						running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
						if err == nil && running {
							klog.Infof("Task %s is still actively running in sandbox %s. Leaving in processing.", f.Name(), sandboxName)
							continue
						}
						completed, err := isSandboxTaskCompleted(ctx, w.kubeClient, w.Namespace, sandboxName, t.Type)
						if err == nil && completed {
							klog.Infof("Task %s already completed in sandbox %s. Moving from processing to processed.", f.Name(), sandboxName)
							t.Status = "Completed"
							if t.StartedAt.IsZero() {
								t.StartedAt = t.EnqueuedAt
							}
							if t.CompletedAt.IsZero() {
								t.CompletedAt = time.Now()
							}
							duration := time.Duration(0)
							if !t.StartedAt.IsZero() && t.CompletedAt.After(t.StartedAt) {
								duration = t.CompletedAt.Sub(t.StartedAt)
							}
							if err := writeTaskAtomically(w.processedDir, f.Name(), &t); err == nil {
								_ = os.Remove(processingPath)
								writeTaskJournalEvent(w.QueueDir, f.Name(), &t, "Completed", duration)
								continue
							}
						}
					}

					t.Status = "Pending"
					t.Recovered = true
					if err := writeTaskAtomically(w.incomingDir, f.Name(), &t); err == nil {
						_ = os.Remove(processingPath)
						klog.Infof("Recovered stuck task %s from processing to incoming", f.Name())
						continue
					}
				}
			}

			// Fallback to simple rename if parsing fails
			incomingPath := filepath.Join(w.incomingDir, f.Name())
			if err := os.Rename(processingPath, incomingPath); err == nil {
				klog.Infof("Recovered stuck task %s (fallback rename) to incoming", f.Name())
			} else {
				klog.Errorf("Failed to recover stuck task %s: %v", f.Name(), err)
			}
		}
	}
}
