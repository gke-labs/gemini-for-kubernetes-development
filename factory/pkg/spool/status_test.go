package spool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
)

func awaitExited(t *testing.T, taskDir string) TaskStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := ReadStatus(taskDir); ok && st.State == Exited {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: no exit recorded", taskDir)
	return TaskStatus{}
}

func TestStartRecordsStartAndEnd(t *testing.T) {
	taskDir := t.TempDir()
	if err := Start(context.Background(), taskDir, "(sleep 0.2; exit 3)", taskDir, nil); err != nil {
		t.Fatal(err)
	}
	st, ok := ReadStatus(taskDir)
	if !ok || st.State != Running || st.PID == 0 || st.StartedAt.IsZero() {
		t.Fatalf("after Start: %+v, %v", st, ok)
	}
	st = awaitExited(t, taskDir)
	if st.ExitCode != "3" || st.EndedAt.IsZero() || st.Reason != "" {
		t.Fatalf("after the end: %+v", st)
	}
}

func TestSignalledTaskGetsTheShellsCode(t *testing.T) {
	taskDir := t.TempDir()
	// The script is killed before it can write an exit code.
	if err := Start(context.Background(), taskDir, "kill -9 $$", taskDir, nil); err != nil {
		t.Fatal(err)
	}
	st := awaitExited(t, taskDir)
	if st.ExitCode != "137" {
		t.Fatalf("exit code = %q, want 137", st.ExitCode)
	}
	if code := readCode(envd.NewTaskFiles(taskDir).ExitCodeFile); code != "137" {
		t.Fatalf("exit_code file = %q, want 137", code)
	}
}

func TestMarkInterrupted(t *testing.T) {
	tasks := t.TempDir()
	running, exited, plain := filepath.Join(tasks, "a"), filepath.Join(tasks, "b"), filepath.Join(tasks, "c")
	for _, d := range []string{running, exited, plain} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := writeStatus(running, TaskStatus{State: Running, PID: 1, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := writeStatus(exited, TaskStatus{State: Exited, ExitCode: "0", StartedAt: now, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	marked := MarkInterrupted(tasks)
	if len(marked) != 1 || marked[0] != "a" {
		t.Fatalf("marked %v, want [a]", marked)
	}
	st, _ := ReadStatus(running)
	if st.State != Exited || st.ExitCode != "137" || !strings.Contains(st.Reason, "restarted") {
		t.Fatalf("interrupted task: %+v", st)
	}
	if st, _ := ReadStatus(exited); st.ExitCode != "0" {
		t.Fatalf("an ended task changed: %+v", st)
	}
	if _, ok := ReadStatus(plain); ok {
		t.Fatalf("a task without a status was given one")
	}
}

func TestFailAndKillAreRecorded(t *testing.T) {
	taskDir := t.TempDir()
	Fail(taskDir, errors.New("no such binary"))
	st, ok := ReadStatus(taskDir)
	if !ok || st.State != Exited || st.ExitCode != "127" || !strings.Contains(st.Reason, "no such binary") {
		t.Fatalf("failed task: %+v, %v", st, ok)
	}

	done := t.TempDir()
	now := time.Now().UTC()
	if err := writeStatus(done, TaskStatus{State: Exited, ExitCode: "0", StartedAt: now, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	RecordKill(done)
	if st, _ := ReadStatus(done); st.ExitCode != "137" || st.Reason != "killed" {
		t.Fatalf("killed task: %+v", st)
	}
}

func TestOverlayTrustsTheStatus(t *testing.T) {
	taskDir := t.TempDir()
	now := time.Now().UTC()
	// The plain files say running (a pid, no exit code); the daemon
	// recorded the end.
	e := Entry{State: Running}
	if err := writeStatus(taskDir, TaskStatus{State: Exited, ExitCode: "2", StartedAt: now, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	Overlay(&e, taskDir)
	if e.State != Exited || e.ExitCode != "2" || !e.Ended.Equal(now) {
		t.Fatalf("overlaid: %+v", e)
	}
	plain := Entry{State: Exited, ExitCode: "1"}
	Overlay(&plain, t.TempDir())
	if plain.State != Exited || plain.ExitCode != "1" {
		t.Fatalf("an entry without a status changed: %+v", plain)
	}
}
