package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileInterruptedTasks(t *testing.T) {
	ctx := context.Background()
	tasksDir := t.TempDir()

	// 1. Interrupted task with pid and start_time, no exit_code -> should get exit_code = 137
	interruptedDir := filepath.Join(tasksDir, "address-20260928-211954")
	if err := os.MkdirAll(interruptedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interruptedDir, "pid"), []byte("36\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interruptedDir, "start_time"), []byte("Mon Sep 28 21:19:54 2026\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Interrupted task with empty exit_code and pid -> should get exit_code = 137
	emptyExitDir := filepath.Join(tasksDir, "investigate-20260926-083023")
	if err := os.MkdirAll(emptyExitDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyExitDir, "pid"), []byte("540760\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyExitDir, "exit_code"), []byte("  \n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Completed task with exit_code = 0 -> should remain 0
	completedDir := filepath.Join(tasksDir, "address-20260926-024106")
	if err := os.MkdirAll(completedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(completedDir, "pid"), []byte("39\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(completedDir, "exit_code"), []byte("0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Unstarted task directory (no pid, no start_time) -> should not get exit_code
	unstartedDir := filepath.Join(tasksDir, "pending-task")
	if err := os.MkdirAll(unstartedDir, 0755); err != nil {
		t.Fatal(err)
	}

	reconcileInterruptedTasks(ctx, tasksDir)

	checkExitCode := func(dir, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "exit_code"))
		if err != nil {
			t.Fatalf("expected exit_code in %s, got err: %v", dir, err)
		}
		if got := strings.TrimSpace(string(data)); got != want {
			t.Errorf("exit_code in %s = %q, want %q", dir, got, want)
		}
	}

	checkExitCode(interruptedDir, "137")
	checkExitCode(emptyExitDir, "137")
	checkExitCode(completedDir, "0")

	if _, err := os.Stat(filepath.Join(unstartedDir, "exit_code")); !os.IsNotExist(err) {
		t.Errorf("expected no exit_code in unstarted task dir, got err=%v", err)
	}

	// 5. Non-existent directory should not panic or fail
	reconcileInterruptedTasks(ctx, filepath.Join(tasksDir, "does-not-exist"))
}
