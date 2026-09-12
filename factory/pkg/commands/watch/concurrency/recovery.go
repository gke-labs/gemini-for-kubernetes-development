package concurrency

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// ProcessingTasks returns a snapshot of the tasks currently in the processing queue, keyed by filename.
// The returned tasks are the live task pointers, suitable for passing back to CompleteTask/FailTask.
func (m *TaskQueueManager) ProcessingTasks() map[string]*api.QueueTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make(map[string]*api.QueueTask, len(m.processing))
	for filename, t := range m.processing {
		tasks[filename] = t
	}
	return tasks
}

// SyncProcessingFromDisk loads any task files found in processingDir that are not
// yet tracked in memory. It is used on startup to discover tasks left behind by a
// previous run before they are reconciled.
func (m *TaskQueueManager) SyncProcessingFromDisk() error {
	if m.processingDir == "" {
		return nil
	}

	entries, err := os.ReadDir(m.processingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, tracked := m.processing[e.Name()]; tracked {
			continue
		}

		filePath := filepath.Join(m.processingDir, e.Name())
		t, err := loadTaskFromDisk(filePath)
		if err != nil {
			klog.Errorf("Failed to load task file %s during processing sync: %v", filePath, err)
			continue
		}
		m.processing[e.Name()] = t
	}

	return nil
}

// RequeueUntrackedProcessingFiles moves task files that exist in processingDir but
// are not tracked in memory (e.g. files whose YAML could not be parsed) back to
// incomingDir, so they are not orphaned in processing forever.
func (m *TaskQueueManager) RequeueUntrackedProcessingFiles() error {
	if m.processingDir == "" || m.incomingDir == "" || m.dryRun {
		return nil
	}

	entries, err := os.ReadDir(m.processingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "task-") || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if _, tracked := m.processing[e.Name()]; tracked {
			continue
		}

		src := filepath.Join(m.processingDir, e.Name())
		dst := filepath.Join(m.incomingDir, e.Name())
		if err := os.Rename(src, dst); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		klog.Infof("Recovered untracked task file %s from processing to incoming", e.Name())
	}

	if len(errs) > 0 {
		return fmt.Errorf("requeueing untracked processing files: %s", strings.Join(errs, "; "))
	}
	return nil
}
