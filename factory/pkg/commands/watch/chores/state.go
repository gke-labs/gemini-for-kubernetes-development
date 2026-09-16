package chores

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
)

// stateFileName is the file under StateDir recording when each chore last ran.
const stateFileName = "chores_state.json"

// RunState records when a chore agent last ran. It is the on-disk schema of
// chores_state.json, keyed by agent name.
type RunState struct {
	LastRun time.Time `json:"lastRun"`
}

// statePath returns the file backing the run state, or an empty string when the
// scheduler is configured without a state directory.
func (s *Scheduler) statePath() string {
	if s.cfg.StateDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.StateDir, stateFileName)
}

// runState returns when each chore last ran, reading the state file on first
// use and serving the in-memory copy from then on. The scheduler is the only
// writer of that file, so the copy cannot go stale behind its back.
func (s *Scheduler) runState() map[string]RunState {
	if s.state == nil {
		s.state = loadRunState(s.statePath())
	}
	return s.state
}

// loadRunState reads the recorded last run of every chore from path.
//
// A missing, unreadable or malformed file yields an empty state rather than an
// error: the worst case is that every chore is treated as never having run,
// which costs one extra run each and then re-establishes the schedule.
func loadRunState(path string) map[string]RunState {
	state := make(map[string]RunState)
	if path == "" {
		return state
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			klog.Warningf("Failed to read chore run state %s: %v", path, err)
		}
		return state
	}

	if err := json.Unmarshal(data, &state); err != nil {
		klog.Warningf("Failed to parse chore run state %s: %v", path, err)
		return make(map[string]RunState)
	}
	return state
}

// saveRunState writes the run state to path via a temporary file and a rename,
// so a crash mid-write leaves the previous state intact instead of a truncated
// file that would read back as "no chore has ever run".
func saveRunState(path string, state map[string]RunState) error {
	if path == "" {
		return nil
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling chore run state: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	tempFile := filepath.Join(dir, "."+stateFileName+".tmp")
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("writing temp chore run state: %w", err)
	}
	if err := os.Rename(tempFile, path); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("renaming temp file to %s: %w", path, err)
	}
	return nil
}
