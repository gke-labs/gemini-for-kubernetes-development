package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The liveness probe is executed, not grepped: it looked for runbook-*
// long after factory started writing run-*, and a string test of the
// script would have passed the whole time.
func TestRunLivenessScriptFindsTheRunTask(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh required")
	}
	tasks := t.TempDir()
	mk := func(name, exitCode string, age time.Duration) {
		t.Helper()
		d := filepath.Join(tasks, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "exit_code"), []byte(exitCode+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		// A pid that cannot be alive; the exit code decides anyway.
		if err := os.WriteFile(filepath.Join(d, "pid"), []byte("999999\n"), 0644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(d, at, at); err != nil {
			t.Fatal(err)
		}
	}
	mk("run-20260928-1000", "0", 2*time.Hour)
	mk("run-20260928-1100", "3", time.Hour)
	// Newer than both, and not a run: a task of another kind, or one
	// left by the command runs replaced, must not answer for the run.
	mk("runbook-20260928-1200", "0", time.Minute)
	mk("fix-20260928-1200", "0", time.Minute)

	script := strings.ReplaceAll(runLivenessScript(), "/workspaces/tasks/", tasks+"/")
	if script == runLivenessScript() {
		t.Fatal("the probe no longer reads /workspaces/tasks/; update this test")
	}
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) != 3 || parts[0] != "no" || parts[1] != "3" {
		t.Errorf("probe = %q, want the newest run task: not alive, exit 3", out)
	}
}

func TestOverseerPeekTasksScriptStartTimeAndSelfHeal(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh required")
	}
	tasksDir := t.TempDir()

	// Start a live process to test both matching start_time (Running) and mismatched start_time (recycled PID -> Crashed)
	sleepCmd := exec.Command("sleep", "30")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	defer func() {
		if sleepCmd.Process != nil {
			_ = sleepCmd.Process.Kill()
		}
	}()
	livePID := sleepCmd.Process.Pid
	lstartOut, err := exec.Command("ps", "-p", strconv.Itoa(livePID), "-o", "lstart=").Output()
	if err != nil {
		t.Fatalf("failed to read lstart for livePID %d: %v", livePID, err)
	}
	liveStart := strings.TrimSpace(string(lstartOut))

	// 1. Live task with matching start_time -> Running
	runningDir := filepath.Join(tasksDir, "address-20260929-222702")
	if err := os.MkdirAll(runningDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(runningDir, "pid"), []byte(strconv.Itoa(livePID)+"\n"), 0644)
	_ = os.WriteFile(filepath.Join(runningDir, "start_time"), []byte(liveStart+"\n"), 0644)

	// 2. Old task whose PID now points to livePID, but start_time mismatches -> Crashed (137) and self-heals exit_code
	recycledDir := filepath.Join(tasksDir, "address-20260928-211954")
	if err := os.MkdirAll(recycledDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(recycledDir, "pid"), []byte(strconv.Itoa(livePID)+"\n"), 0644)
	_ = os.WriteFile(filepath.Join(recycledDir, "start_time"), []byte("Mon Sep 28 21:19:54 2026\n"), 0644)

	script := buildOverseerPeekTasksScript(tasksDir, "overseer-kcc")
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("peek script failed: %v\n%s", err, out)
	}
	var got []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			State    string  `json:"state"`
			ExitCode *string `json:"exitCode"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal peek output: %v\n%s", err, out)
	}
	byName := map[string]string{}
	for _, item := range got {
		byName[item.Metadata.Name] = item.Status.State
	}
	if byName["address-20260929-222702"] != "Running" {
		t.Errorf("live task state = %q, want Running", byName["address-20260929-222702"])
	}
	if byName["address-20260928-211954"] != "Crashed" {
		t.Errorf("recycled-PID task state = %q, want Crashed", byName["address-20260928-211954"])
	}
	ecBytes, err := os.ReadFile(filepath.Join(recycledDir, "exit_code"))
	if err != nil || strings.TrimSpace(string(ecBytes)) != "137" {
		t.Errorf("expected self-healed exit_code=137 in recycled task dir, got %q (err: %v)", string(ecBytes), err)
	}
}
