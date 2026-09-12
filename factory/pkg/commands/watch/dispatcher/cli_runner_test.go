package dispatcher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func testCLIRunner(logDir string) *CLIRunner {
	return NewCLIRunner(CLIRunnerConfig{
		Namespace:        "test-namespace",
		Image:            "custom-image:v1",
		DiskSize:         "50Gi",
		EphemeralStorage: "20Gi",
		CPURequest:       "2",
		CPULimit:         "4",
		MemoryRequest:    "4Gi",
		MemoryLimit:      "8Gi",
		TaskTimeout:      30 * time.Minute,
		ProcessingLogDir: logDir,
	})
}

func TestCLIRunner_BuildArgs(t *testing.T) {
	r := testCLIRunner(t.TempDir())

	t.Run("issue-fix task", func(t *testing.T) {
		task := &api.QueueTask{
			Type:   api.TypeIssueFix,
			URL:    "https://github.com/test-owner/test-repo/issues/123",
			Number: 123,
		}
		args := r.BuildArgs(task, "coder-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "fix" {
			t.Errorf("expected command 'fix', got %q", args[0])
		}

		expectedFlags := map[string]string{
			"--url":                   task.URL,
			"--instruction":           "Fix this issue",
			"--namespace":             "test-namespace",
			"--user":                  "coder-bot",
			"--image":                 "custom-image:v1",
			"--workspace-disk-size":   "50Gi",
			"--ephemeral-storage":     "20Gi",
			"--cpu-request":           "2",
			"--cpu-limit":             "4",
			"--memory-request":        "4Gi",
			"--memory-limit":          "8Gi",
			"--timeout":               "30m0s",
			"--abort-on-cancel=false": "",
		}

		for flag, expectedVal := range expectedFlags {
			found := false
			for i, a := range args {
				if a == flag {
					found = true
					if expectedVal != "" && i+1 < len(args) && args[i+1] != expectedVal {
						t.Errorf("flag %s has value %q, want %q", flag, args[i+1], expectedVal)
					}
					break
				}
			}
			if !found {
				t.Errorf("missing expected flag %s in args: %v", flag, args)
			}
		}
	})

	t.Run("pr-review task with instructions", func(t *testing.T) {
		task := &api.QueueTask{
			Type:         api.TypePRReview,
			URL:          "https://github.com/test-owner/test-repo/pull/456",
			Number:       456,
			Instructions: []string{"check security", "check unit tests"},
		}
		args := r.BuildArgs(task, "reviewer-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "pr" || args[1] != "review" {
			t.Errorf("expected command 'pr review', got %v", args[:2])
		}

		// Verify instructions
		var instructionsFound []string
		for i, a := range args {
			if a == "--instruction" && i+1 < len(args) {
				instructionsFound = append(instructionsFound, args[i+1])
			}
		}
		if len(instructionsFound) != 2 || instructionsFound[0] != "check security" || instructionsFound[1] != "check unit tests" {
			t.Errorf("instructions = %v, want ['check security', 'check unit tests']", instructionsFound)
		}
	})

	t.Run("agent-chore task with session-id", func(t *testing.T) {
		task := &api.QueueTask{
			Type:      api.TypeAgentChore,
			URL:       "https://github.com/test-owner/test-repo/issues/789",
			Number:    789,
			AgentFile: ".agents/chore.md",
			SessionID: "issue-789",
		}
		args := r.BuildArgs(task, "chore-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		if args[0] != "agent" || args[1] != "create" {
			t.Errorf("expected command 'agent create', got %v", args[:2])
		}

		sessionFound := false
		for i, a := range args {
			if a == "--session-id" && i+1 < len(args) && args[i+1] == "issue-789" {
				sessionFound = true
				break
			}
		}
		if !sessionFound {
			t.Errorf("missing --session-id issue-789 in args: %v", args)
		}
	})

	t.Run("unknown task type returns nil", func(t *testing.T) {
		task := &api.QueueTask{
			Type: "unknown-type",
		}
		args := r.BuildArgs(task, "bot")
		if args != nil {
			t.Errorf("expected nil args for unknown type, got %v", args)
		}
	})
}

func TestCLIRunner_Run_ExecutesChildProcessAndWritesLog(t *testing.T) {
	logDir := t.TempDir()
	r := testCLIRunner(logDir)
	r.resolveExecutable = func() (string, error) { return "/usr/bin/factory", nil }

	var gotName string
	var gotArgs []string
	r.execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		return exec.CommandContext(ctx, "echo", "hello from the task")
	}

	task := &api.QueueTask{
		Type:   api.TypeIssueFix,
		Number: 10,
		URL:    "https://github.com/test-owner/test-repo/issues/10",
	}
	if err := r.Run(context.Background(), "task-10.yaml", task, "coder-bot"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if gotName != "/usr/bin/factory" {
		t.Errorf("expected the factory executable to be invoked, got %q", gotName)
	}
	if len(gotArgs) == 0 || gotArgs[0] != "fix" {
		t.Errorf("expected 'fix' args, got %v", gotArgs)
	}

	logContents, err := os.ReadFile(filepath.Join(logDir, "task-10.log"))
	if err != nil {
		t.Fatalf("expected task log file to be written: %v", err)
	}
	if string(logContents) != "hello from the task\n" {
		t.Errorf("unexpected log contents: %q", string(logContents))
	}
}

func TestCLIRunner_Run_ReturnsErrorOnFailure(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	r.resolveExecutable = func() (string, error) { return "/usr/bin/factory", nil }
	r.execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	task := &api.QueueTask{Type: api.TypeIssueFix, Number: 11}
	if err := r.Run(context.Background(), "task-11.yaml", task, ""); err == nil {
		t.Error("expected an error when the child process exits non-zero")
	}
}

func TestCLIRunner_Run_RejectsUnknownTaskType(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	called := false
	r.execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "true")
	}

	task := &api.QueueTask{Type: "unknown-type", Number: 12}
	if err := r.Run(context.Background(), "task-12.yaml", task, ""); err == nil {
		t.Error("expected an error for an unknown task type")
	}
	if called {
		t.Error("expected no child process for an unknown task type")
	}
}

func TestCLIRunner_Run_PropagatesExecutableResolutionError(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	r.resolveExecutable = func() (string, error) { return "", errors.New("boom") }

	task := &api.QueueTask{Type: api.TypeIssueFix, Number: 13}
	if err := r.Run(context.Background(), "task-13.yaml", task, ""); err == nil {
		t.Error("expected an error when the factory executable cannot be resolved")
	}
}
