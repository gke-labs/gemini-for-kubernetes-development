package concurrency

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// UpdateTaskPriority updates the priority of a task in the incoming queue in memory and on disk.
func (m *TaskQueueManager) UpdateTaskPriority(filename string, priority api.TaskPriority) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.incoming.UpdatePriority(filename, priority) {
		task, _ := m.incoming.Get(filename)
		if !m.dryRun && m.incomingDir != "" {
			if err := writeTaskAtomically(m.incomingDir, filename, task); err != nil {
				return fmt.Errorf("failed to save task priority: %w", err)
			}
		}
		return nil
	}

	if m.incomingDir != "" {
		incomingPath := filepath.Join(m.incomingDir, filename)
		t, err := loadTaskFromDisk(incomingPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("task %s not found in incoming queue: %w", filename, os.ErrNotExist)
			}
			return fmt.Errorf("failed to parse task %s: %w", filename, err)
		}
		t.Priority = priority
		m.incoming.Enqueue(filename, t)
		if !m.dryRun {
			if err := writeTaskAtomically(m.incomingDir, filename, t); err != nil {
				return fmt.Errorf("failed to save task priority: %w", err)
			}
		}
		return nil
	}

	return fmt.Errorf("task %s not found in incoming queue: %w", filename, os.ErrNotExist)
}

// GetQueueResponse constructs the full QueueResponse payload served by the HTTP API directly
// from thread-safe in-memory maps in sub-millisecond time.
func (m *TaskQueueManager) GetQueueResponse() api.QueueResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	taskToItem := func(filename string, t *api.QueueTask, queueState string) api.QueueTaskItem {
		tPrio := api.TaskPriority(strings.ToLower(string(t.Priority)))
		if tPrio == "" {
			tPrio = api.PriorityMedium
		}
		var createdStr, enqueuedStr, triggerEventStr, startedStr, completedStr string
		if !t.CreatedAt.IsZero() {
			createdStr = t.CreatedAt.Format(time.RFC3339)
		}
		if !t.EnqueuedAt.IsZero() {
			enqueuedStr = t.EnqueuedAt.Format(time.RFC3339)
		}
		if !t.TriggerEventTime.IsZero() {
			triggerEventStr = t.TriggerEventTime.Format(time.RFC3339)
		}
		if !t.StartedAt.IsZero() {
			startedStr = t.StartedAt.Format(time.RFC3339)
		}
		if !t.CompletedAt.IsZero() {
			completedStr = t.CompletedAt.Format(time.RFC3339)
		}
		var durationSec float64
		if dur := t.Duration(); dur > 0 {
			durationSec = dur.Seconds()
		}
		return api.QueueTaskItem{
			FileName:         filename,
			QueueState:       queueState,
			Type:             t.Type,
			URL:              t.URL,
			Number:           t.Number,
			Priority:         tPrio,
			Phase:            t.Phase,
			CreatedAt:        createdStr,
			EnqueuedAt:       enqueuedStr,
			StartedAt:        startedStr,
			CompletedAt:      completedStr,
			DurationSeconds:  durationSec,
			TriggerEventTime: triggerEventStr,
			TriggerReason:    t.TriggerReason,
			TriggerNotes:     t.TriggerNotes,
			Assignee:         t.Assignee,
			Status:           t.Status,
			CommitSHA:        t.CommitSHA,
		}
	}

	sortedIncoming := m.incoming.ToSortedSlice()

	var incoming []api.QueueTaskItem
	for i, item := range sortedIncoming {
		qi := taskToItem(item.Filename, item.Task, "incoming")
		qi.Rank = i + 1
		incoming = append(incoming, qi)
	}

	var processing []api.QueueTaskItem
	for fn, t := range m.processing {
		processing = append(processing, taskToItem(fn, t, "processing"))
	}
	sort.SliceStable(processing, func(i, j int) bool {
		return processing[i].FileName < processing[j].FileName
	})

	type processedEntry struct {
		filename string
		task     *api.QueueTask
	}
	var processedEntries []processedEntry
	for fn, t := range m.processed {
		processedEntries = append(processedEntries, processedEntry{filename: fn, task: t})
	}
	sort.SliceStable(processedEntries, func(i, j int) bool {
		if !processedEntries[i].task.CompletedAt.Equal(processedEntries[j].task.CompletedAt) {
			return processedEntries[i].task.CompletedAt.After(processedEntries[j].task.CompletedAt)
		}
		return processedEntries[i].filename < processedEntries[j].filename
	})

	if len(processedEntries) > 20 {
		processedEntries = processedEntries[:20]
	}

	var processed []api.QueueTaskItem
	for _, pe := range processedEntries {
		processed = append(processed, taskToItem(pe.filename, pe.task, "processed"))
	}

	byPrio := make(map[api.TaskPriority]int)
	byType := make(map[api.TaskType]int)
	for _, item := range incoming {
		byPrio[item.Priority]++
		byType[item.Type]++
	}

	return api.QueueResponse{
		Summary: api.QueueSummary{
			TotalPending:    len(incoming),
			TotalProcessing: len(processing),
			TotalCompleted:  len(processed),
			ByPriority:      byPrio,
			ByType:          byType,
		},
		Incoming:   incoming,
		Processing: processing,
		Processed:  processed,
	}
}
