package envd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runShell(t *testing.T, script string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), err
}

func TestBuildDetachedLaunchCmd(t *testing.T) {
	tmpDir := t.TempDir()
	taskDir := filepath.Join(tmpDir, "task-123")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create task dir: %v", err)
	}

	files := NewTaskFiles(taskDir)
	launchCmd := BuildDetachedLaunchCmd(files, "(echo 'hello resilient' && echo 'line 2')")

	// Execute detached command
	if _, err := runShell(t, launchCmd); err != nil {
		t.Fatalf("failed to execute launch command: %v", err)
	}

	// Poll until exit code file is populated
	var exitCodeBytes []byte
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		exitCodeBytes, err = os.ReadFile(files.ExitCodeFile)
		if err == nil && len(strings.TrimSpace(string(exitCodeBytes))) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || len(strings.TrimSpace(string(exitCodeBytes))) == 0 {
		t.Fatalf("timeout waiting for exit code file to be written: %v", err)
	}

	if strings.TrimSpace(string(exitCodeBytes)) != "0" {
		t.Errorf("expected exit code '0', got %q", string(exitCodeBytes))
	}

	// Check PID file
	pidBytes, err := os.ReadFile(files.PIDFile)
	if err != nil || len(strings.TrimSpace(string(pidBytes))) == 0 {
		t.Fatalf("failed to read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Errorf("invalid pid written: %q", string(pidBytes))
	}

	// Check log file
	logBytes, err := os.ReadFile(files.LogFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	logContent := string(logBytes)
	if !strings.Contains(logContent, "hello resilient") || !strings.Contains(logContent, "line 2") {
		t.Errorf("log file content missing expected text: %q", logContent)
	}
}

func TestBuildCheckPidCmd(t *testing.T) {
	tmpDir := t.TempDir()
	files := NewTaskFiles(tmpDir)

	// 1. Missing PID file -> outputs nothing
	out, _ := runShell(t, BuildCheckPidCmd(files.PIDFile))
	if out != "" {
		t.Errorf("expected empty output for missing PID file, got %q", out)
	}

	// 2. Empty PID file -> outputs nothing
	_ = os.WriteFile(files.PIDFile, []byte(""), 0644)
	out, _ = runShell(t, BuildCheckPidCmd(files.PIDFile))
	if out != "" {
		t.Errorf("expected empty output for empty PID file, got %q", out)
	}

	// 3. Dead/non-existent PID -> outputs nothing
	_ = os.WriteFile(files.PIDFile, []byte("9999999"), 0644)
	out, _ = runShell(t, BuildCheckPidCmd(files.PIDFile))
	if out != "" {
		t.Errorf("expected empty output for non-existent PID, got %q", out)
	}

	// 4. Live process -> outputs "alive"
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	livePID := cmd.Process.Pid
	_ = os.WriteFile(files.PIDFile, []byte(strconv.Itoa(livePID)), 0644)

	out, err := runShell(t, BuildCheckPidCmd(files.PIDFile))
	if err != nil || out != "alive" {
		t.Errorf("expected 'alive', got %q (err: %v)", out, err)
	}
}

func TestBuildAbortKillCmd(t *testing.T) {
	t.Run("kills process", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := NewTaskFiles(tmpDir)

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start process: %v", err)
		}
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()

		livePID := cmd.Process.Pid
		_ = os.WriteFile(files.PIDFile, []byte(strconv.Itoa(livePID)), 0644)

		killScript := BuildAbortKillCmd(files.PIDFile, files.ExitCodeFile)
		if _, err := runShell(t, killScript); err != nil {
			t.Fatalf("failed to run abort kill script: %v", err)
		}

		// Verify exit_code file contains 143
		ecBytes, err := os.ReadFile(files.ExitCodeFile)
		if err != nil || strings.TrimSpace(string(ecBytes)) != "143" {
			t.Errorf("expected exit_code 143, got %q (err: %v)", string(ecBytes), err)
		}

		// Wait and verify process was terminated
		time.Sleep(200 * time.Millisecond)
		if err := cmd.Process.Signal(os.Signal(nil)); err == nil {
			// Check ps output
			out, psErr := exec.Command("ps", "-p", strconv.Itoa(livePID), "-o", "stat=").Output()
			if psErr == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
				t.Errorf("expected process %d to be killed", livePID)
			}
		}
	})

	t.Run("does not overwrite existing exit_code", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := NewTaskFiles(tmpDir)

		_ = os.WriteFile(files.ExitCodeFile, []byte("0"), 0644)
		_ = os.WriteFile(files.PIDFile, []byte("9999999"), 0644)

		killScript := BuildAbortKillCmd(files.PIDFile, files.ExitCodeFile)
		if _, err := runShell(t, killScript); err != nil {
			t.Fatalf("failed to run abort kill script: %v", err)
		}

		ecBytes, _ := os.ReadFile(files.ExitCodeFile)
		if strings.TrimSpace(string(ecBytes)) != "0" {
			t.Errorf("expected existing exit_code 0 to remain unchanged, got %q", string(ecBytes))
		}
	})
}

func TestBuildQuotaKillCmd(t *testing.T) {
	t.Run("kills process and sets exit code 137", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := NewTaskFiles(tmpDir)

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start process: %v", err)
		}
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()

		livePID := cmd.Process.Pid
		_ = os.WriteFile(files.PIDFile, []byte(strconv.Itoa(livePID)), 0644)

		killScript := BuildQuotaKillCmd(files.PIDFile, files.ExitCodeFile)
		if _, err := runShell(t, killScript); err != nil {
			t.Fatalf("failed to run quota kill script: %v", err)
		}

		ecBytes, err := os.ReadFile(files.ExitCodeFile)
		if err != nil || strings.TrimSpace(string(ecBytes)) != "137" {
			t.Errorf("expected exit_code 137, got %q (err: %v)", string(ecBytes), err)
		}

		time.Sleep(200 * time.Millisecond)
		out, psErr := exec.Command("ps", "-p", strconv.Itoa(livePID), "-o", "stat=").Output()
		if psErr == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			t.Errorf("expected process %d to be killed by SIGKILL", livePID)
		}
	})
}

func TestBuildTailLogCmd(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "execution.log")

	// 1. Missing log file -> empty output
	out, _ := runShell(t, BuildTailLogCmd(logFile, 0))
	if out != "" {
		t.Errorf("expected empty output for missing log file, got %q", out)
	}

	// 2. Full log read from offset 0
	_ = os.WriteFile(logFile, []byte("Hello World\nLine 2\n"), 0644)
	out, err := runShell(t, BuildTailLogCmd(logFile, 0))
	if err != nil || out != "Hello World\nLine 2" {
		t.Errorf("expected full log read, got %q (err: %v)", out, err)
	}

	// 3. Tail delta from offset 12 ("Line 2\n")
	out, err = runShell(t, BuildTailLogCmd(logFile, 12))
	if err != nil || out != "Line 2" {
		t.Errorf("expected tail delta 'Line 2', got %q (err: %v)", out, err)
	}
}

func TestBuildCheckExitCodeCmd(t *testing.T) {
	tmpDir := t.TempDir()
	ecFile := filepath.Join(tmpDir, "exit_code")

	// Missing file
	out, _ := runShell(t, BuildCheckExitCodeCmd(ecFile))
	if out != "" {
		t.Errorf("expected empty output for missing exit_code file, got %q", out)
	}

	// Empty file
	_ = os.WriteFile(ecFile, []byte(""), 0644)
	out, _ = runShell(t, BuildCheckExitCodeCmd(ecFile))
	if out != "" {
		t.Errorf("expected empty output for empty exit_code file, got %q", out)
	}

	// Valid exit code
	_ = os.WriteFile(ecFile, []byte("0\n"), 0644)
	out, _ = runShell(t, BuildCheckExitCodeCmd(ecFile))
	if out != "0" {
		t.Errorf("expected '0', got %q", out)
	}
}

func TestBuildWriteExitCodeCmds(t *testing.T) {
	tmpDir := t.TempDir()
	ecFile := filepath.Join(tmpDir, "exit_code")

	// Write if missing when missing -> writes 1
	_, _ = runShell(t, BuildWriteExitCodeIfMissingCmd(ecFile, 1))
	b, _ := os.ReadFile(ecFile)
	if strings.TrimSpace(string(b)) != "1" {
		t.Errorf("expected '1', got %q", string(b))
	}

	// Write if missing when exists -> does not overwrite
	_, _ = runShell(t, BuildWriteExitCodeIfMissingCmd(ecFile, 2))
	b, _ = os.ReadFile(ecFile)
	if strings.TrimSpace(string(b)) != "1" {
		t.Errorf("expected '1' not overwritten, got %q", string(b))
	}

	// Unconditional write -> overwrites with 137
	_, _ = runShell(t, BuildWriteExitCodeCmd(ecFile, 137))
	b, _ = os.ReadFile(ecFile)
	if strings.TrimSpace(string(b)) != "137" {
		t.Errorf("expected '137', got %q", string(b))
	}
}

func TestBuildCheckLatestTaskStatusCmd(t *testing.T) {
	tasksDir := t.TempDir()

	// 1. No task directory exists -> "NOTASKS"
	out, err := runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "NOTASKS" {
		t.Errorf("expected NOTASKS for empty tasks parent dir, got %q (err: %v)", out, err)
	}

	// 2. Task directory exists but exit_code and pid files are missing -> "NOTASKS"
	t1 := filepath.Join(tasksDir, "task-1")
	_ = os.MkdirAll(t1, 0755)
	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "NOTASKS" {
		t.Errorf("expected NOTASKS for task dir with no files, got %q (err: %v)", out, err)
	}

	// 3. Task directory exists, exit_code is missing, and pid file is empty -> "NOTASKS"
	_ = os.WriteFile(filepath.Join(t1, "pid"), []byte(""), 0644)
	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "NOTASKS" {
		t.Errorf("expected NOTASKS for missing exit_code and empty pid file, got %q (err: %v)", out, err)
	}

	// 4. Task directory has exit_code -> returns exit_code
	_ = os.WriteFile(filepath.Join(t1, "exit_code"), []byte("0\n"), 0644)
	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "0" {
		t.Errorf("expected '0', got %q (err: %v)", out, err)
	}

	_ = os.WriteFile(filepath.Join(t1, "exit_code"), []byte("1\n"), 0644)
	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "1" {
		t.Errorf("expected '1', got %q (err: %v)", out, err)
	}

	// 5. exit_code file is missing, but dead PID exists -> falls back to process check and returns "137"
	_ = os.Remove(filepath.Join(t1, "exit_code"))
	_ = os.WriteFile(filepath.Join(t1, "pid"), []byte("9999999\n"), 0644)
	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "137" {
		t.Errorf("expected '137' for missing exit_code with dead PID, got %q (err: %v)", out, err)
	}

	// 6. exit_code file is missing, but live PID exists -> returns "RUNNING"
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start live process: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	livePID := cmd.Process.Pid
	_ = os.WriteFile(filepath.Join(t1, "pid"), []byte(strconv.Itoa(livePID)), 0644)

	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "RUNNING" {
		t.Errorf("expected 'RUNNING' for missing exit_code with live process, got %q (err: %v)", out, err)
	}

	// 7. Multiple task directories: ensure the latest created/modified task directory is evaluated
	time.Sleep(10 * time.Millisecond)
	t2 := filepath.Join(tasksDir, "task-2")
	_ = os.MkdirAll(t2, 0755)
	_ = os.WriteFile(filepath.Join(t2, "exit_code"), []byte("42\n"), 0644)

	out, err = runShell(t, BuildCheckLatestTaskStatusCmd(tasksDir))
	if err != nil || out != "42" {
		t.Errorf("expected latest task dir exit_code '42', got %q (err: %v)", out, err)
	}
}
