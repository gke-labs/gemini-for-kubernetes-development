package concurrency

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

	// cooldowns are the tasks the issue scanner is holding back, by task
	// file name (NoteCooldown), served with the queue until they expire.
	cooldowns map[string]api.Cooldown
	// drops are the tasks the dispatcher dropped before starting them
	// (DropTask), newest last, the last maxDrops of them, served with the
	// queue.
	drops []api.Drop

	// failures are the tasks that failed and have not succeeded since,
	// newest last, the last maxFailures of them, served with the queue.
	failures []api.Failure

	// runs are the runs that ended (journalled Completed or Failed), when
	// each started and whether it failed, by task ID, the last runsKeptFor of them: what RunsSince
	// counts. Loaded from journal.jsonl on first use and kept up to date as
	// runs end, under runsMu (not mu: the journal is written with mu held).
	runsMu     sync.Mutex
	runs       map[string][]api.EndedRun
	runsLoaded bool
}

// runsKeptFor is how far back RunsSince can count: the give-up it serves
// resets on a commit or a human comment, which come long before this.
const runsKeptFor = 30 * 24 * time.Hour

// maxDrops bounds the drops the queue remembers for its readers.
const maxDrops = 50

// maxFailures bounds the failures the queue remembers for its readers.
const maxFailures = 50

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
//
// The file's modification time is returned alongside the task because it is the
// only record of when a hand-written or pre-timestamp task file was last
// touched, and the caller knows which directory the file came from - and so
// what that timestamp means.
func loadTaskFromDisk(filePath string) (*api.QueueTask, time.Time, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, time.Time{}, err
	}
	var t api.QueueTask
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, time.Time{}, fmt.Errorf("parsing task file %s: %w", filePath, err)
	}
	if t.Priority == "" {
		t.Priority = api.PriorityMedium
	}
	if t.Status == "" {
		t.Status = api.StatusPending
	}

	var modTime time.Time
	if info, statErr := os.Stat(filePath); statErr == nil {
		modTime = info.ModTime()
	}
	if t.EnqueuedAt.IsZero() {
		t.EnqueuedAt = GetEnqueueTime(&t, modTime)
	}
	return &t, modTime, nil
}

// EnsureDirs creates the directories the queue stores tasks and task logs in.
//
// The queue creates them because it owns them: it is the only component that
// reads or writes what is inside, so leaving the caller to lay them out would
// hand a second component a say in the queue's storage layout.
//
// Creating a directory that already exists is not an error, so this is safe to
// call on every start-up. A dry-run queue touches no files at all and so
// creates nothing.
func (m *TaskQueueManager) EnsureDirs() error {
	if m.dryRun {
		return nil
	}
	for _, dir := range []struct {
		path string
		name string
	}{
		{m.incomingDir, "incoming queue"},
		{m.processingDir, "processing queue"},
		{m.processedDir, "processed queue"},
		{m.processingLogDir, "processing log"},
		{m.processedLogDir, "processed log"},
	} {
		if dir.path == "" {
			continue
		}
		if err := os.MkdirAll(dir.path, 0755); err != nil {
			return fmt.Errorf("failed to create %s dir: %w", dir.name, err)
		}
	}
	return nil
}

// LoadFromDisk populates the in-memory queues by scanning incomingDir, processingDir,
// and processedDir. Returns an error if directory traversal fails.
func (m *TaskQueueManager) LoadFromDisk() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.incoming.Reset()
	m.processing = make(map[string]*api.QueueTask)
	m.processed = make(map[string]*api.QueueTask)

	loadDir := func(dir string, onTask func(filename string, t *api.QueueTask, modTime time.Time)) error {
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
			t, modTime, err := loadTaskFromDisk(filePath)
			if err != nil {
				klog.Errorf("Failed to load task file %s: %v", filePath, err)
				continue
			}
			onTask(m.migrateTaskFile(dir, e.Name(), t), t, modTime)
		}
		return nil
	}

	if err := loadDir(m.incomingDir, func(fn string, t *api.QueueTask, _ time.Time) {
		m.incoming.Enqueue(fn, t)
	}); err != nil {
		return fmt.Errorf("loading incoming queue: %w", err)
	}
	if err := loadDir(m.processingDir, func(fn string, t *api.QueueTask, _ time.Time) {
		m.processing[fn] = t
	}); err != nil {
		return fmt.Errorf("loading processing queue: %w", err)
	}
	if err := loadDir(m.processedDir, func(fn string, t *api.QueueTask, modTime time.Time) {
		// Everything in the processed directory has finished, whatever its
		// recorded status says. A file that did not record when it finished is
		// dated by its own timestamp, so that readers of the processed set
		// never have to open the file to find out.
		if t.CompletedAt.IsZero() {
			t.CompletedAt = modTime
		}
		m.processed[fn] = t
	}); err != nil {
		return fmt.Errorf("loading processed queue: %w", err)
	}

	// The failures served with the queue, from the processed tasks that
	// failed last, so that a restart does not hide them.
	m.failures = nil
	for fn, t := range m.processed {
		if t.Status == api.StatusFailed {
			m.failures = append(m.failures, api.Failure{FileName: fn, Number: t.Number, URL: t.URL, Type: t.Type, Rule: t.Rule, Recipe: t.Recipe, Error: t.Error, At: t.CompletedAt})
		}
	}
	slices.SortFunc(m.failures, func(a, b api.Failure) int { return a.At.Compare(b.At) })
	if len(m.failures) > maxFailures {
		m.failures = m.failures[len(m.failures)-maxFailures:]
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
		t, _, err := loadTaskFromDisk(filePath)
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

	// The queue takes its own copy on the way in, for the same reason it hands
	// out copies on the way out: the caller must not keep a reference into
	// queue state through a shared slice.
	taskCopy := *task.DeepCopy()
	if taskCopy.EnqueuedAt.IsZero() {
		taskCopy.EnqueuedAt = time.Now()
	}
	if taskCopy.RunName == "" {
		taskCopy.RunName = api.RunNameOf(&taskCopy)
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
	// Recovered says "this task is on its way back from processing", so it belongs to
	// that move and not to the task itself. Starting the task ends the move: leaving
	// the flag set would let a later Enqueue treat a live processing entry as one to
	// be pulled back to incoming.
	t.Recovered = false
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
	// The processed set keeps the queue's own copy. Holding the caller's task
	// would leave it able to edit finished state after the fact, which is the
	// same leak the read accessors guard against.
	m.processed[filename] = t.DeepCopy()
	m.noteOutcome(filename, t, status)

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

// HasActiveIssueTask reports whether any task for issue issueNumber is
// queued or running: a fix (task-issue-N) or a workflow step
// (task-workflow-<name>-issue-N), by the task's number or its file name.
// The issue scanner queues nothing more for an issue while one is, as the
// pull request scanner does for a pull request (HasActivePRTask).
func (m *TaskQueueManager) HasActiveIssueTask(issueNumber int) bool {
	if issueNumber <= 0 {
		return false
	}
	isFor := func(filename string, t *api.QueueTask) bool {
		if t != nil && t.Number == issueNumber && !api.IsPRTask(t.Type) {
			return true
		}
		return issueTaskFile(filename, issueNumber)
	}
	m.mu.RLock()
	if m.incoming.HasMatching(isFor) {
		m.mu.RUnlock()
		return true
	}
	for fn, t := range m.processing {
		if isFor(fn, t) {
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
			if !f.IsDir() && issueTaskFile(f.Name(), issueNumber) {
				return true
			}
		}
	}
	return false
}

// issueTaskFile reports whether filename names a task for issue n: a
// rule's (api.RuleTaskFile, the fix's included) or a workflow step.
func issueTaskFile(filename string, n int) bool {
	return strings.HasPrefix(filename, fmt.Sprintf("task-issue-%d-rule-", n)) ||
		(strings.HasPrefix(filename, "task-workflow-") && strings.HasSuffix(filename, fmt.Sprintf("-issue-%d.yaml", n)))
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

// GetProcessedTask returns the finished task recorded under filename, or nil
// when nothing by that name has finished.
//
// The task is returned as a deep copy. The queue owns the task files and the
// state derived from them, so a caller that could mutate what it reads back
// would be editing queue state behind the queue's back.
func (m *TaskQueueManager) GetProcessedTask(filename string) *api.QueueTask {
	filename = filepath.Base(filename)
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.processed[filename]
	if !ok {
		return nil
	}
	return t.DeepCopy()
}

// ListProcessedTasks returns a snapshot of every finished task, keyed by task
// file name. Each task in the snapshot is a deep copy, for the same reason
// GetProcessedTask returns one.
//
// This is how the scanners recover what has already been done for an issue or
// pull request across a restart. They used to read the processed directory
// themselves; routing it through the queue keeps the task files under a single
// owner, and hands them the tasks that finished since start-up as well as the
// ones recovered from disk.
//
// Every task in the snapshot carries a non-zero CompletedAt: the queue records
// one when it finishes a task, and dates the files it recovers by their own
// timestamp. Callers can therefore order the processed set without going back
// to the filesystem for it.
func (m *TaskQueueManager) ListProcessedTasks() map[string]*api.QueueTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := make(map[string]*api.QueueTask, len(m.processed))
	for filename, t := range m.processed {
		snapshot[filename] = t.DeepCopy()
	}
	return snapshot
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
	if event == string(api.StatusCompleted) || event == string(api.StatusFailed) {
		m.noteRun(strings.TrimSuffix(taskFilename, ".yaml"), api.EndedRun{StartedAt: runStart(task.StartedAt, time.Now()), Failed: event == string(api.StatusFailed)})
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

// migrateTaskFile brings a task file of a legacy type over (api.MigrateTask)
// - renamed to the rule task's file it is now, rewritten as one - and is
// the name it goes by: a watch upgraded from one runs it, and finds its
// runs, as the rule task it is. Any other task file stands as it is.
func (m *TaskQueueManager) migrateTaskFile(dir, name string, t *api.QueueTask) string {
	id := strings.TrimSuffix(name, ".yaml")
	if !api.MigrateTask(t, id) {
		return name
	}
	newID, _ := api.MigratedTaskID(id)
	newName := newID + ".yaml"
	if m.dryRun {
		return newName
	}
	if err := writeTaskAtomically(dir, newName, t); err != nil {
		klog.Errorf("Migrating task %s to %s: %v", name, newName, err)
		return name
	}
	if newName != name {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			klog.Warningf("Migrating task %s: removing it: %v", name, err)
		}
	}
	klog.Infof("Migrated task %s to %s (%s, rule %s)", name, newName, t.Type, t.Rule)
	return newName
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

// noteOutcome keeps the failures list: a failed task replaces any earlier
// failure of the same task, a completed one clears it. Called with m.mu
// held.
func (m *TaskQueueManager) noteOutcome(filename string, t *api.QueueTask, status api.TaskStatus) {
	kept := m.failures[:0]
	for _, f := range m.failures {
		if f.FileName != filename {
			kept = append(kept, f)
		}
	}
	m.failures = kept
	if status != api.StatusFailed {
		return
	}
	m.failures = append(m.failures, api.Failure{
		FileName: filename,
		Number:   t.Number,
		URL:      t.URL,
		Type:     t.Type,
		Rule:     t.Rule,
		Recipe:   t.Recipe,
		Error:    t.Error,
		At:       t.CompletedAt,
	})
	if len(m.failures) > maxFailures {
		m.failures = m.failures[len(m.failures)-maxFailures:]
	}
}

// DropTask removes a queued task the dispatcher will not start because a
// fact it was decided on no longer holds (reason, e.g. "head moved
// abc1234→def5678"). Unlike a failure it leaves no processed task, so the
// drop counts toward no cooldown, retry or give-up, and the task's name is
// free for the next scan to queue it again if it still applies. It is
// journalled as Dropped and remembered for the queue's readers.
func (m *TaskQueueManager) DropTask(filename string, task *api.QueueTask, reason string) error {
	filename = filepath.Base(filename)
	if err := m.RemoveTask(filename); err != nil {
		return err
	}
	dropped := task.DeepCopy()
	dropped.Error = reason
	m.writeJournalEvent(filename, dropped, "Dropped", 0)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.drops = append(m.drops, api.Drop{
		FileName: filename,
		Number:   task.Number,
		URL:      task.URL,
		Type:     task.Type,
		Rule:     task.Rule,
		Recipe:   task.Recipe,
		Reason:   reason,
		At:       time.Now(),
	})
	if len(m.drops) > maxDrops {
		m.drops = m.drops[len(m.drops)-maxDrops:]
	}
	return nil
}

// NoteCooldown records that the issue scanner is holding c's task back
// until c.Until, for the queue's readers (the overseer's work view), and
// drops the cooldowns that have expired. It reports whether this is news:
// the first time the task is held back until that time, which is when the
// scanner says so in its log.
func (m *TaskQueueManager) NoteCooldown(c api.Cooldown) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for fn, held := range m.cooldowns {
		if !held.Until.After(now) {
			delete(m.cooldowns, fn)
		}
	}
	if m.cooldowns == nil {
		m.cooldowns = map[string]api.Cooldown{}
	}
	if held, ok := m.cooldowns[c.FileName]; ok && held.Until.Equal(c.Until) {
		return false
	}
	m.cooldowns[c.FileName] = c
	return true
}

// RunsSince counts the runs of the task named filename that ended (Completed
// or Failed, in the journal) and started after since: how many attempts a rule
// has made on an item since something reset its patience. A task dropped
// before it started (DropTask) never ran and does not count, and neither does
// one still queued or running.
func (m *TaskQueueManager) RunsSince(filename string, since time.Time) int {
	m.runsMu.Lock()
	defer m.runsMu.Unlock()
	m.loadRunsLocked()
	n := 0
	for _, r := range m.runs[strings.TrimSuffix(filename, ".yaml")] {
		if r.StartedAt.After(since) {
			n++
		}
	}
	return n
}

// EndedRuns are the runs of the task named filename that ended, each
// with its outcome, oldest first, as far back as RunsSince counts: the
// rules record's history of an item.
func (m *TaskQueueManager) EndedRuns(filename string) []api.EndedRun {
	m.runsMu.Lock()
	defer m.runsMu.Unlock()
	m.loadRunsLocked()
	out := slices.Clone(m.runs[strings.TrimSuffix(filename, ".yaml")])
	slices.SortStableFunc(out, func(a, b api.EndedRun) int { return a.StartedAt.Compare(b.StartedAt) })
	return out
}

// noteRun records a run of taskID that ended, once the journal has been read
// (until then, reading it will find this run there).
func (m *TaskQueueManager) noteRun(taskID string, run api.EndedRun) {
	m.runsMu.Lock()
	defer m.runsMu.Unlock()
	if m.runsLoaded {
		m.runs[taskID] = append(m.runs[taskID], run)
	}
}

// loadRunsLocked reads the ended runs from journal.jsonl, once. A journal that
// cannot be read, or a line that does not parse, is skipped: the count is then
// low, never high, and the comment count the give-up also takes stands.
func (m *TaskQueueManager) loadRunsLocked() {
	if m.runsLoaded {
		return
	}
	m.runsLoaded = true
	m.runs = map[string][]api.EndedRun{}
	if m.queueDir == "" {
		return
	}
	f, err := os.Open(filepath.Join(m.queueDir, "journal.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	cutoff := time.Now().Add(-runsKeptFor)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var je api.JournalEvent
		if json.Unmarshal(scanner.Bytes(), &je) != nil {
			continue
		}
		if je.Event != string(api.StatusCompleted) && je.Event != string(api.StatusFailed) {
			continue
		}
		started := runStart(je.StartedAt, je.Timestamp)
		if started.Before(cutoff) {
			continue
		}
		// A run journalled under a legacy task's name is the rule task's
		// it is now (api.MigratedTaskID), so it counts toward its
		// cooldown and give-up.
		id, _ := api.MigratedTaskID(je.TaskID)
		m.runs[id] = append(m.runs[id], api.EndedRun{StartedAt: started, Failed: je.Event == string(api.StatusFailed)})
	}
}

// runStart is when a run started, or else when its end was recorded.
func runStart(startedAt, fallback time.Time) time.Time {
	if startedAt.IsZero() {
		return fallback
	}
	return startedAt
}
