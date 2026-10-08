package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
)

// StatusFile is a task's record of how it runs and how it ended. The
// daemon, the task's parent, is its only writer: it records the start
// when it launches the task and the end when it reaps it, so nobody has
// to infer either from pid files. Tasks the daemon did not start (envd's
// classic tasks, images older than this file) have none.
const StatusFile = "status.json"

// TaskStatus is what StatusFile holds.
type TaskStatus struct {
	State     State     `json:"state"` // Running or Exited
	PID       int       `json:"pid,omitempty"`
	ExitCode  string    `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// Reason says why a task ended without its own exit code: it could
	// not start, or the container it ran in restarted.
	Reason string `json:"reason,omitempty"`
}

// ReadStatus is the task's status, false when it has none.
func ReadStatus(taskDir string) (TaskStatus, bool) {
	data, err := os.ReadFile(filepath.Join(taskDir, StatusFile))
	if err != nil {
		return TaskStatus{}, false
	}
	var st TaskStatus
	if json.Unmarshal(data, &st) != nil || st.State == "" {
		return TaskStatus{}, false
	}
	return st, true
}

// writeStatus replaces the task's status whole, so a reader never sees
// half of one.
func writeStatus(taskDir string, st TaskStatus) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(taskDir, "."+StatusFile)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(taskDir, StatusFile))
}

// recordExit records how the task's process ended: the exit code its
// script wrote, or a cancel wrote; failing both, the one its wait status
// gives (128+signal for a process signalled before it could write one,
// as a shell reports it), written to exit_code too, so that readers of
// the plain files agree.
func recordExit(taskDir string, st TaskStatus, waitErr error, ps *os.ProcessState) TaskStatus {
	tf := envd.NewTaskFiles(taskDir)
	code := readCode(tf.ExitCodeFile)
	if code == "" {
		code = "1"
		if ps != nil {
			if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = strconv.Itoa(128 + int(ws.Signal()))
			} else {
				code = strconv.Itoa(ps.ExitCode())
			}
		} else {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = strconv.Itoa(exitErr.ExitCode())
			}
		}
		_ = os.WriteFile(tf.ExitCodeFile, []byte(code+"\n"), 0o644)
	}
	st.State, st.ExitCode, st.EndedAt = Exited, code, time.Now().UTC()
	return st
}

// MarkInterrupted records the end of every task the status of which says
// it still runs: on the daemon's start, those ran in a container that is
// gone, and their processes with it.
func MarkInterrupted(tasksDir string) []string {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil
	}
	var marked []string
	for _, e := range entries {
		taskDir := filepath.Join(tasksDir, e.Name())
		st, ok := ReadStatus(taskDir)
		if !e.IsDir() || !ok || st.State == Exited {
			continue
		}
		tf := envd.NewTaskFiles(taskDir)
		code := readCode(tf.ExitCodeFile)
		if code == "" {
			code = "137"
			_ = os.WriteFile(tf.ExitCodeFile, []byte(code+"\n"), 0o644)
			st.Reason = "the sandbox's container restarted"
		}
		st.State, st.ExitCode, st.EndedAt = Exited, code, time.Now().UTC()
		if writeStatus(taskDir, st) == nil {
			marked = append(marked, e.Name())
		}
	}
	return marked
}

// RecordKill records a task killed (exit code 137) after its end was
// recorded: a kill overrides the task's own code, as the quota kill
// through envd does. A task not yet reaped needs nothing: its end is
// recorded from the exit code the kill wrote.
func RecordKill(taskDir string) {
	st, ok := ReadStatus(taskDir)
	if !ok || st.State != Exited || st.ExitCode == "137" {
		return
	}
	st.ExitCode, st.Reason = "137", "killed"
	_ = writeStatus(taskDir, st)
}

// failStatus records a task that never started.
func failStatus(taskDir string, cause error) {
	now := time.Now().UTC()
	_ = writeStatus(taskDir, TaskStatus{State: Exited, ExitCode: "127", StartedAt: now, EndedAt: now, Reason: fmt.Sprintf("could not start: %v", cause)})
}

func readCode(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Overlay corrects an entry listed from the plain task files with the
// task's status, where it has one: the daemon's word on whether it runs
// and how it ended. It adds the engine's retries too.
func Overlay(e *Entry, taskDir string) {
	e.EngineRetries = engineRetries(taskDir)
	st, ok := ReadStatus(taskDir)
	if !ok {
		return
	}
	switch st.State {
	case Running:
		e.State, e.ExitCode = Running, ""
	case Exited:
		e.State, e.ExitCode = Exited, st.ExitCode
	}
	if !st.EndedAt.IsZero() {
		e.Ended = st.EndedAt
	}
	e.Reason = st.Reason
}
