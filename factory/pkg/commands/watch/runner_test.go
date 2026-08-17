package watch

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
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
		task := &QueueTask{
			Type:   "issue-fix",
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
		task := &QueueTask{
			Type:         "pr-review",
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
		task := &QueueTask{
			Type:      "agent-chore",
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
		task := &QueueTask{
			Type: "unknown-type",
		}
		args := w.buildTaskCommandArgs(task, "bot")
		if args != nil {
			t.Errorf("expected nil args for unknown type, got %v", args)
		}
	})
}
