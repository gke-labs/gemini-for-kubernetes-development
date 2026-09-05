package concurrency

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// TaskQueueManagerConfig defines options for initializing a TaskQueueManager.
type TaskQueueManagerConfig struct {
	QueueDir         string
	IncomingDir      string
	ProcessingDir    string
	ProcessedDir     string
	ProcessingLogDir string
	ProcessedLogDir  string
	DryRun           bool
}

// TaskQueueManager coordinates queue tasks using a persistent in-memory priority queue for incoming tasks
// and thread-safe maps for processing and processed tasks, backed by write-through disk persistence.
type TaskQueueManager struct {
	mu         sync.RWMutex
	incoming   *TaskPriorityQueue
	processing map[string]*api.QueueTask
	processed  map[string]*api.QueueTask

	incomingDir      string
	processingDir    string
	processedDir     string
	processingLogDir string
	processedLogDir  string
	queueDir         string
	dryRun           bool
}

// NewTaskQueueManager creates a new TaskQueueManager instance with configured directories.
func NewTaskQueueManager(cfg TaskQueueManagerConfig) *TaskQueueManager {
	incomingDir := cfg.IncomingDir
	if incomingDir == "" && cfg.QueueDir != "" {
		incomingDir = filepath.Join(cfg.QueueDir, "incoming")
	}
	processingDir := cfg.ProcessingDir
	if processingDir == "" && cfg.QueueDir != "" {
		processingDir = filepath.Join(cfg.QueueDir, "processing")
	}
	processedDir := cfg.ProcessedDir
	if processedDir == "" && cfg.QueueDir != "" {
		processedDir = filepath.Join(cfg.QueueDir, "processed")
	}
	processingLogDir := cfg.ProcessingLogDir
	if processingLogDir == "" && cfg.QueueDir != "" {
		processingLogDir = filepath.Join(cfg.QueueDir, "logs", "processing")
	}
	processedLogDir := cfg.ProcessedLogDir
	if processedLogDir == "" && cfg.QueueDir != "" {
		processedLogDir = filepath.Join(cfg.QueueDir, "logs", "processed")
	}

	return &TaskQueueManager{
		incoming:         NewTaskPriorityQueue(),
		processing:       make(map[string]*api.QueueTask),
		processed:        make(map[string]*api.QueueTask),
		incomingDir:      incomingDir,
		processingDir:    processingDir,
		processedDir:     processedDir,
		processingLogDir: processingLogDir,
		processedLogDir:  processedLogDir,
		queueDir:         cfg.QueueDir,
		dryRun:           cfg.DryRun,
	}
}

// loadTaskFromDisk reads and parses a task YAML file from disk, applying defaults for
// Priority ("medium"), Status ("Pending"), and EnqueuedAt (derived from file modTime or CreatedAt).
func loadTaskFromDisk(filePath string) (*api.QueueTask, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var t api.QueueTask
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing task file %s: %w", filePath, err)
	}
	if t.Priority == "" {
		t.Priority = api.PriorityMedium
	}
	if t.Status == "" {
		t.Status = api.StatusPending
	}
	if t.EnqueuedAt.IsZero() {
		var modTime time.Time
		if info, statErr := os.Stat(filePath); statErr == nil {
			modTime = info.ModTime()
		}
		t.EnqueuedAt = GetEnqueueTime(&t, modTime)
	}
	return &t, nil
}

// LoadFromDisk populates the in-memory queues by scanning incomingDir, processingDir,
// and processedDir. Returns an error if directory traversal fails.
func (m *TaskQueueManager) LoadFromDisk() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.incoming.Reset()
	m.processing = make(map[string]*api.QueueTask)
	m.processed = make(map[string]*api.QueueTask)

	loadDir := func(dir string, onTask func(filename string, t *api.QueueTask)) error {
		if dir == "" {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			filePath := filepath.Join(dir, e.Name())
			t, err := loadTaskFromDisk(filePath)
			if err != nil {
				klog.Errorf("Failed to load task file %s: %v", filePath, err)
				continue
			}
			onTask(e.Name(), t)
		}
		return nil
	}

	if err := loadDir(m.incomingDir, func(fn string, t *api.QueueTask) {
		m.incoming.Enqueue(fn, t)
	}); err != nil {
		return fmt.Errorf("loading incoming queue: %w", err)
	}
	if err := loadDir(m.processingDir, func(fn string, t *api.QueueTask) {
		m.processing[fn] = t
	}); err != nil {
		return fmt.Errorf("loading processing queue: %w", err)
	}
	if err := loadDir(m.processedDir, func(fn string, t *api.QueueTask) {
		m.processed[fn] = t
	}); err != nil {
		return fmt.Errorf("loading processed queue: %w", err)
	}

	return nil
}

// SyncIncomingFromDisk scans incomingDir on disk and updates the in-memory incoming queue:
// adds new files discovered on disk and removes memory tasks that no longer exist on disk.
func (m *TaskQueueManager) SyncIncomingFromDisk() error {
	if m.incomingDir == "" {
		return nil
	}

	entries, err := os.ReadDir(m.incomingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	diskFiles := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		filename := e.Name()
		diskFiles[filename] = true

		if m.incoming.Contains(filename) {
			continue
		}
		if _, inProcessing := m.processing[filename]; inProcessing {
			continue
		}

		filePath := filepath.Join(m.incomingDir, filename)
		t, err := loadTaskFromDisk(filePath)
		if err != nil {
			klog.Errorf("Failed to load task file %s during sync: %v", filePath, err)
			continue
		}

		m.incoming.Enqueue(filename, t)
		m.writeJournalEvent(filename, t, "Created", 0)
	}

	if !m.dryRun {
		m.incoming.RemoveMatching(func(filename string, _ *api.QueueTask) bool {
			return !diskFiles[filename]
		})
	}

	return nil
}

// Enqueue adds a task to the incoming queue with write-through disk persistence and journal logging.
// If the task already exists in incoming (or in processing without recovered flag), Enqueue is a no-op.
func (m *TaskQueueManager) Enqueue(filename string, task *api.QueueTask) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Handle recovered tasks moving back from processing to incoming
	if task.Recovered {
		delete(m.processing, filename)
		if !m.dryRun && m.processingDir != "" {
			_ = os.Remove(filepath.Join(m.processingDir, filename))
		}
	} else {
		if m.incoming.Contains(filename) {
			return nil
		}
		if _, exists := m.processing[filename]; exists {
			return nil
		}
	}

	taskCopy := *task
	if taskCopy.EnqueuedAt.IsZero() {
		taskCopy.EnqueuedAt = time.Now()
	}
	if taskCopy.Priority == "" {
		taskCopy.Priority = api.PriorityMedium
	}
	if taskCopy.Status == "" {
		taskCopy.Status = api.StatusPending
	}

	if !m.dryRun && m.incomingDir != "" {
		if err := writeTaskAtomically(m.incomingDir, filename, &taskCopy); err != nil {
			return fmt.Errorf("failed to write task atomically to incoming: %w", err)
		}
	}

	m.incoming.Enqueue(filename, &taskCopy)
	*task = taskCopy

	m.writeJournalEvent(filename, &taskCopy, "Created", 0)

	return nil
}

// ClaimNextCandidate selects the highest-priority candidate task from incoming
// that is not currently in visibility cooldown, reserving it in memory without
// moving it to processing or modifying disk files.
func (m *TaskQueueManager) ClaimNextCandidate() (string, *api.QueueTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filename, task := m.incoming.ClaimCandidate()
	if task == nil {
		return "", nil, nil
	}
	return filename, task, nil
}

// StartTask moves a claimed candidate task from incoming to processing:
// updates status to Running, records StartedAt, atomically moves the task file
// on disk, updates in-memory queues, and writes a Started event to the journal.
func (m *TaskQueueManager) StartTask(filename string, task *api.QueueTask) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	t := task
	if t == nil {
		var ok bool
		t, ok = m.incoming.Get(filename)
		if !ok {
			t, ok = m.processing[filename]
			if !ok {
				return fmt.Errorf("task %s not found in incoming or processing queue", filename)
			}
		}
	}

	t.Status = api.StatusRunning
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}

	if !m.dryRun {
		if err := moveTaskFile(m.incomingDir, m.processingDir, filename, t); err != nil {
			return err
		}
	}

	m.incoming.Remove(filename)
	m.processing[filename] = t
	m.writeJournalEvent(filename, t, "Started", 0)

	return nil
}

// ReleaseTask returns a task back to incoming with Pending status and ready for claiming.
// If the task is already in incoming (e.g. a claimed candidate), it returns it to the ready heap.
func (m *TaskQueueManager) ReleaseTask(filename string) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	task, inProcessing := m.processing[filename]
	if !inProcessing {
		var inIncoming bool
		_, inIncoming = m.incoming.Get(filename)
		if !inIncoming {
			return fmt.Errorf("task %s not found in processing or incoming queue", filename)
		}
		m.incoming.Release(filename)
		return nil
	}

	task.Status = api.StatusPending
	task.StartedAt = time.Time{}

	if !m.dryRun {
		if err := moveTaskFile(m.processingDir, m.incomingDir, filename, task); err != nil {
			return err
		}
	}

	delete(m.processing, filename)
	m.incoming.Enqueue(filename, task)

	return nil
}

// finishTask consolidates terminal state transitions (Completed or Failed) in memory and on disk.
func (m *TaskQueueManager) finishTask(filename string, task *api.QueueTask, status api.TaskStatus, errMsg string) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	t := task
	if t == nil {
		var exists bool
		t, exists = m.processing[filename]
		if !exists {
			t, _ = m.incoming.Get(filename)
		}
	}
	if t == nil {
		return fmt.Errorf("task %s not found", filename)
	}

	t.Status = status
	if t.CompletedAt.IsZero() {
		t.CompletedAt = time.Now()
	}
	if errMsg != "" {
		t.Error = errMsg
	}

	duration := t.Duration()

	if !m.dryRun {
		srcDir := m.processingDir
		if m.processingDir != "" {
			if _, err := os.Stat(filepath.Join(m.processingDir, filename)); err != nil {
				srcDir = m.incomingDir
			}
		}
		if err := moveTaskFile(srcDir, m.processedDir, filename, t); err != nil {
			return err
		}

		if m.processingLogDir != "" && m.processedLogDir != "" {
			baseName := strings.TrimSuffix(filename, ".yaml")
			logSrc := filepath.Join(m.processingLogDir, baseName+".log")
			logDst := filepath.Join(m.processedLogDir, baseName+".log")
			if _, err := os.Stat(logSrc); err == nil {
				_ = os.MkdirAll(m.processedLogDir, 0755)
				_ = os.Rename(logSrc, logDst)
			}
		}
	}

	m.incoming.Remove(filename)
	delete(m.processing, filename)
	m.processed[filename] = t

	m.writeJournalEvent(filename, t, string(status), duration)
	return nil
}

// CompleteTask marks a task as Completed, moving it from processing to processed in memory and on disk.
func (m *TaskQueueManager) CompleteTask(filename string, task *api.QueueTask) error {
	return m.finishTask(filename, task, api.StatusCompleted, "")
}

// FailTask marks a task as Failed, recording its error and moving it to processed in memory and on disk.
func (m *TaskQueueManager) FailTask(filename string, task *api.QueueTask, errMsg string) error {
	return m.finishTask(filename, task, api.StatusFailed, errMsg)
}

// TaskExists checks whether a task with the given filename exists in the incoming or processing queue.
// Consults in-memory maps first under RLock, falling back to disk checks if not found.
func (m *TaskQueueManager) TaskExists(filename string) bool {
	filename = filepath.Base(filename)
	m.mu.RLock()
	if m.incoming.Contains(filename) {
		m.mu.RUnlock()
		return true
	}
	if _, exists := m.processing[filename]; exists {
		m.mu.RUnlock()
		return true
	}
	m.mu.RUnlock()

	if m.incomingDir != "" {
		if _, err := os.Stat(filepath.Join(m.incomingDir, filename)); err == nil {
			return true
		}
	}
	if m.processingDir != "" {
		if _, err := os.Stat(filepath.Join(m.processingDir, filename)); err == nil {
			return true
		}
	}
	return false
}

// HasActivePRTask reports whether any task for the given PR number is currently
// pending in the incoming queue or executing in the processing queue.
func (m *TaskQueueManager) HasActivePRTask(prNumber int) bool {
	prefix := fmt.Sprintf("task-pr-%d-", prNumber)

	m.mu.RLock()
	if m.incoming.HasActivePRTask(prNumber) {
		m.mu.RUnlock()
		return true
	}
	for fn, t := range m.processing {
		if (t.Number == prNumber && api.IsPRTask(t.Type)) || strings.HasPrefix(fn, prefix) {
			m.mu.RUnlock()
			return true
		}
	}
	m.mu.RUnlock()

	for _, dir := range []string{m.incomingDir, m.processingDir} {
		if dir == "" {
			continue
		}
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

// RemoveTask removes a task from incoming and processing in memory and deletes its file from disk.
func (m *TaskQueueManager) RemoveTask(filename string) error {
	filename = filepath.Base(filename)
	m.mu.Lock()
	defer m.mu.Unlock()

	m.incoming.Remove(filename)
	delete(m.processing, filename)

	if !m.dryRun {
		if m.incomingDir != "" {
			_ = os.Remove(filepath.Join(m.incomingDir, filename))
		}
		if m.processingDir != "" {
			_ = os.Remove(filepath.Join(m.processingDir, filename))
		}
	}

	return nil
}

// RemovePendingTasksForNumber removes all tasks matching the specified issue or PR number
// from the incoming queue in memory and deletes their files from disk in a single pass.
func (m *TaskQueueManager) RemovePendingTasksForNumber(number int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pattern1 := fmt.Sprintf("-issue-%d.yaml", number)
	pattern2 := fmt.Sprintf("-pr-%d-", number)

	removed := m.incoming.RemoveMatching(func(filename string, t *api.QueueTask) bool {
		return t.Number == number || strings.Contains(filename, pattern1) || strings.Contains(filename, pattern2)
	})

	for _, filename := range removed {
		if !m.dryRun && m.incomingDir != "" {
			_ = os.Remove(filepath.Join(m.incomingDir, filename))
		}
	}

	return nil
}

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

// GetCounts returns the current counts of tasks across incoming, processing, and processed queues.
func (m *TaskQueueManager) GetCounts() (incoming, processing, processed int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.incoming.Len(), len(m.processing), len(m.processed)
}

func (m *TaskQueueManager) writeJournalEvent(taskFilename string, task *api.QueueTask, event string, duration time.Duration) {
	if m.dryRun || m.queueDir == "" {
		return
	}
	journalPath := filepath.Join(m.queueDir, "journal.jsonl")
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		klog.Errorf("Failed to open journal file: %v", err)
		return
	}
	defer f.Close()

	dur := duration
	if dur <= 0 {
		dur = task.Duration()
	}

	je := api.JournalEvent{
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
	if dur > 0 {
		je.DurationSecond = dur.Seconds()
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

// writeTaskAtomically writes a task struct into dir as filename atomically.
func writeTaskAtomically(dir string, filename string, task *api.QueueTask) error {
	data, err := yaml.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshaling task to YAML: %w", err)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	tempFile := filepath.Join(dir, fmt.Sprintf(".temp-%s", filename))
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("writing temp task file: %w", err)
	}

	targetFile := filepath.Join(dir, filename)
	if err := os.Rename(tempFile, targetFile); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("renaming temp file to target %s: %w", targetFile, err)
	}

	return nil
}

// moveTaskFile moves a task file from srcDir to dstDir, ensuring the updated task state
// is atomically written to the destination and the source file is removed.
func moveTaskFile(srcDir, dstDir, filename string, task *api.QueueTask) error {
	if srcDir == "" || dstDir == "" {
		return nil
	}
	srcPath := filepath.Join(srcDir, filename)
	dstPath := filepath.Join(dstDir, filename)

	if err := os.Rename(srcPath, dstPath); err != nil {
		if writeErr := writeTaskAtomically(dstDir, filename, task); writeErr != nil {
			return fmt.Errorf("failed to move task to %s: %w", dstDir, err)
		}
		_ = os.Remove(srcPath)
	} else {
		_ = writeTaskAtomically(dstDir, filename, task)
	}
	return nil
}
