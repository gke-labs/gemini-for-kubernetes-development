package chores

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRunStateMissingFile(t *testing.T) {
	state := loadRunState(filepath.Join(t.TempDir(), stateFileName))

	if len(state) != 0 {
		t.Errorf("expected an empty state for a missing file, got %v", state)
	}
}

func TestLoadRunStateWithoutAStateDir(t *testing.T) {
	if state := loadRunState(""); len(state) != 0 {
		t.Errorf("expected an empty state when no file backs it, got %v", state)
	}
}

func TestSaveAndLoadRunStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	lastRun := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	if err := saveRunState(path, map[string]RunState{"Nightly Triage": {LastRun: lastRun}}); err != nil {
		t.Fatalf("saveRunState failed: %v", err)
	}

	state := loadRunState(path)
	if got := state["Nightly Triage"].LastRun; !got.Equal(lastRun) {
		t.Errorf("last run = %v, want %v", got, lastRun)
	}
}

// TestLoadRunStateCorruptFile checks that a truncated or hand-edited state file
// degrades to "no chore has ever run" rather than failing the cycle: every
// chore then runs once and the schedule re-establishes itself.
func TestLoadRunStateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("writing corrupt state failed: %v", err)
	}

	if state := loadRunState(path); len(state) != 0 {
		t.Errorf("expected an empty state for a corrupt file, got %v", state)
	}
}

// TestSaveRunStateLeavesNoTemporaryFile guards the rename: a leftover temp file
// would be read by nothing, but it would also mean the rename never happened.
func TestSaveRunStateLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, stateFileName)

	if err := saveRunState(path, map[string]RunState{"Nightly Triage": {LastRun: time.Now()}}); err != nil {
		t.Fatalf("saveRunState failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading state dir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != stateFileName {
		t.Errorf("expected only %s in the state dir, got %v", stateFileName, entries)
	}
}

func TestStatePathWithoutAStateDir(t *testing.T) {
	s := New(Config{}, Deps{})

	if got := s.statePath(); got != "" {
		t.Errorf("statePath = %q, want empty when no state dir is configured", got)
	}
}
