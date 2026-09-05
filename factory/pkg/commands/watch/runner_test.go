package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func TestBuildTaskCommandArgs(t *testing.T) {
	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace:        "test-namespace",
			Image:            "custom-image:v1",
			DiskSize:         "50Gi",
			EphemeralStorage: "20Gi",
			CPURequest:       "2",
			CPULimit:         "4",
			MemoryRequest:    "4Gi",
			MemoryLimit:      "8Gi",
		},
		Flags: Flags{
			TaskTimeout: 30 * time.Minute,
		},
	}

	t.Run("issue-fix task", func(t *testing.T) {
		task := &api.QueueTask{
			Type:   api.TypeIssueFix,
			URL:    "https://github.com/test-owner/test-repo/issues/123",
			Number: 123,
		}
		args := w.buildTaskCommandArgs(task, "coder-bot")
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
		args := w.buildTaskCommandArgs(task, "reviewer-bot")
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
		args := w.buildTaskCommandArgs(task, "chore-bot")
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
		args := w.buildTaskCommandArgs(task, "bot")
		if args != nil {
			t.Errorf("expected nil args for unknown type, got %v", args)
		}
	})
}

func TestRunTasks_DryRun_LeavesInIncoming(t *testing.T) {
	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:   tempDir,
			DryRun:     true,
			MaxActions: 10,
			MaxPending: 10,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	fn := "task-issue-1.yaml"
	incomingDir := filepath.Join(tempDir, "incoming")
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		t.Fatalf("failed to create incoming dir: %v", err)
	}
	taskContent := "type: issue-fix\nnumber: 1\nurl: https://github.com/owner/repo/issues/1\npriority: high\n"
	if err := os.WriteFile(filepath.Join(incomingDir, fn), []byte(taskContent), 0644); err != nil {
		t.Fatalf("failed to write incoming task file: %v", err)
	}

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		URL:        "https://github.com/owner/repo/issues/1",
		Number:     1,
		Priority:   api.PriorityHigh,
		EnqueuedAt: time.Now(),
	}
	if err := w.queueMgr.Enqueue(fn, task); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	w.runTasks(context.Background())

	// Under dry run, the task should NOT move to processing. It must remain in incoming!
	if _, err := os.Stat(filepath.Join(tempDir, "incoming", fn)); err != nil {
		t.Errorf("expected %s to remain in incoming: %v", fn, err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "processing", fn)); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist in processing", fn)
	}

	inc, proc, _ := w.queueMgr.GetCounts()
	if inc != 1 || proc != 0 {
		t.Errorf("expected counts (1, 0), got (%d, %d)", inc, proc)
	}
}

func TestRunTasks_DrainMode_DoesNotClaim(t *testing.T) {
	tempDir := t.TempDir()
	w := &Watcher{
		Flags: Flags{
			QueueDir:   tempDir,
			MaxActions: 10,
			MaxPending: 10,
		},
		kubeClient: newTestKubeClient(),
	}
	w.initQueueManager()

	_ = os.WriteFile(filepath.Join(tempDir, ".drain"), []byte(""), 0644)

	task := &api.QueueTask{
		Type:       api.TypeIssueFix,
		Number:     2,
		EnqueuedAt: time.Now(),
	}
	_ = w.queueMgr.Enqueue("task-issue-2.yaml", task)

	w.runTasks(context.Background())

	inc, proc, _ := w.queueMgr.GetCounts()
	if inc != 1 || proc != 0 {
		t.Errorf("expected counts (1, 0), got (%d, %d)", inc, proc)
	}
}
