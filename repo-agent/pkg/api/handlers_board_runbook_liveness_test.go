package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The liveness probe is executed, not grepped: it looked for runbook-*
// long after factory started writing run-*, and a string test of the
// script would have passed the whole time. A run's phase is a run
// recipe's task now, recipe-run-<phase>-*.
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
	mk("recipe-run-plan-20260928-100000-0a1b", "0", 2*time.Hour)
	mk("recipe-run-deploy-20260928-110000-2c3d", "3", time.Hour)
	// Newer than both, and not a run: a task of another kind, or one
	// left by the commands runs replaced, must not answer for the run.
	mk("run-20260928-1200", "0", time.Minute)
	mk("runbook-20260928-1200", "0", time.Minute)
	mk("recipe-fix-20260928-120000-4e5f", "0", time.Minute)
	mk("recipe-runbook-20260928-120000-6a7b", "0", time.Minute)

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
