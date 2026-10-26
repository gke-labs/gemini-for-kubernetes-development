package whyclosed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// StateFileName is the filename used to persist why-closed state in StateDir.
const StateFileName = "why_closed_state.json"

// StateStore manages persistence and thread-safe access to PRStates.
type StateStore struct {
	mu        sync.RWMutex
	statePath string
	prs       map[int]PRState
}

// NewStateStore creates a new StateStore, loading existing state if stateDir is non-empty.
func NewStateStore(stateDir string) *StateStore {
	var statePath string
	if stateDir != "" {
		statePath = filepath.Join(stateDir, StateFileName)
	}
	s := &StateStore{
		statePath: statePath,
		prs:       make(map[int]PRState),
	}
	if statePath != "" {
		s.load()
	}
	return s
}

func (s *StateStore) load() {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			klog.Warningf("Failed to read why-closed state %s: %v", s.statePath, err)
		}
		return
	}
	var loaded map[int]PRState
	if err := json.Unmarshal(data, &loaded); err != nil {
		klog.Warningf("Failed to parse why-closed state %s: %v", s.statePath, err)
		return
	}
	s.prs = loaded
}

func (s *StateStore) saveLocked() {
	if s.statePath == "" {
		return
	}
	data, err := json.MarshalIndent(s.prs, "", "  ")
	if err != nil {
		klog.Errorf("Failed to marshal why-closed state: %v", err)
		return
	}

	dir := filepath.Dir(s.statePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		klog.Errorf("Failed to create directory for why-closed state %s: %v", dir, err)
		return
	}

	tmpFile := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", StateFileName, time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		klog.Errorf("Failed to write temporary why-closed state %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, s.statePath); err != nil {
		_ = os.Remove(tmpFile)
		klog.Errorf("Failed to rename temporary why-closed state to %s: %v", s.statePath, err)
	}
}

// RecordAsked records that a prompt was sent or found for the PR.
func (s *StateStore) RecordAsked(pr int, closer string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.prs[pr]
	st.PR = pr
	st.Asked = true
	if st.AskedAt.IsZero() {
		st.AskedAt = at
	}
	if closer != "" {
		st.Closer = closer
	}
	s.prs[pr] = st
	s.saveLocked()
}

// IsAsked reports whether the PR has already been recorded as asked.
func (s *StateStore) IsAsked(pr int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.prs[pr].Asked
}

// Get returns the PRState for a given PR number.
func (s *StateStore) Get(pr int) (PRState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.prs[pr]
	return st, ok
}

// GetCloseReason returns the recorded close reason for a PR, if any.
func (s *StateStore) GetCloseReason(pr int) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.prs[pr]
	if !ok || st.CloseReason == "" {
		return "", false
	}
	return st.CloseReason, true
}

// SetCloseReason stores the captured close reason on the PR state.
func (s *StateStore) SetCloseReason(pr int, reason string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.prs[pr]
	st.PR = pr
	st.CloseReason = reason
	st.AnsweredAt = at
	s.prs[pr] = st
	s.saveLocked()
}

// ActivePRs returns PRs that were asked, have no close reason yet, and are within maxAge.
func (s *StateStore) ActivePRs(maxAge time.Duration) []PRState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []PRState
	now := time.Now()
	for _, st := range s.prs {
		if !st.Asked || st.CloseReason != "" {
			continue
		}
		if !st.AskedAt.IsZero() && now.Sub(st.AskedAt) > maxAge {
			continue
		}
		active = append(active, st)
	}
	return active
}

// All returns a slice of all stored PRStates.
func (s *StateStore) All() []PRState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make([]PRState, 0, len(s.prs))
	for _, st := range s.prs {
		all = append(all, st)
	}
	return all
}
