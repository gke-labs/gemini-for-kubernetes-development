// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dispatcher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func testCLIRunner(logDir string) *CLIRunner {
	return NewCLIRunner(CLIRunnerConfig{
		Namespace:        "test-namespace",
		Image:            "custom-image:v1",
		DiskSize:         "50Gi",
		StorageClass:     "premium-rwo",
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

	t.Run("issue-fix task runs the fix recipe", func(t *testing.T) {
		task := &api.QueueTask{
			Type:   api.TypeIssueRule,
			URL:    "https://github.com/test-owner/test-repo/issues/123",
			Recipe: "fix",
			Rule:   "fix", Apply: "open-pr", PRLabels: true, Inputs: map[string]string{"draft": "false"},
			Number:     123,
			EnqueuedAt: time.Unix(1760000000, 0),
		}
		args := r.BuildArgs(task, "coder-bot")
		// Its PR opens ready for review, labelled for the watch.
		want := []string{"recipe", "fix", "--url", task.URL, "--run-name", "fix-123-head-1760000000", "--apply", "--action", "open-pr", "--pr-labels", "--input", "draft=false"}
		if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
			t.Fatalf("args = %v, want them to start %v", args, want)
		}

		expectedFlags := map[string]string{
			"--namespace":               "test-namespace",
			"--user":                    "coder-bot",
			"--image":                   "custom-image:v1",
			"--workspace-disk-size":     "50Gi",
			"--workspace-storage-class": "premium-rwo",
			"--ephemeral-storage":       "20Gi",
			"--cpu-request":             "2",
			"--cpu-limit":               "4",
			"--memory-request":          "4Gi",
			"--memory-limit":            "8Gi",
			"--timeout":                 "30m0s",
			"--abort-on-cancel=false":   "",
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
			Type:         api.TypePRRule,
			URL:          "https://github.com/test-owner/test-repo/pull/456",
			Number:       456,
			CommitSHA:    "0123456789abcdef",
			EnqueuedAt:   time.Unix(1760000000, 0),
			Instructions: []string{"check security", "check unit tests"},
			Recipe:       "review",
			Rule:         "review", Apply: "submit-review",
		}
		args := r.BuildArgs(task, "reviewer-bot")
		if len(args) == 0 {
			t.Fatalf("expected args, got empty slice")
		}
		want := []string{"recipe", "review", "--url", task.URL, "--run-name", "review-456-0123456-1760000000", "--apply", "--action", "submit-review"}
		if !slices.Equal(args[:len(want)], want) {
			t.Errorf("args = %v, want them to start %v", args, want)
		}
		for i, a := range args {
			if a == "--user" && (i+1 >= len(args) || args[i+1] != "reviewer-bot") {
				t.Errorf("--user is not the reviewer: %v", args)
			}
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

	t.Run("pr-investigate task runs care-ci", func(t *testing.T) {
		task := &api.QueueTask{
			Recipe: "care-ci",
			Rule:   "ci", Apply: "post-replies",
			Type:       api.TypePRRule,
			URL:        "https://github.com/test-owner/test-repo/pull/456",
			Number:     456,
			CommitSHA:  "0123456789abcdef",
			EnqueuedAt: time.Unix(1760000000, 0),
		}
		args := r.BuildArgs(task, "coder-bot")
		want := []string{"recipe", "care-ci", "--url", task.URL, "--run-name", "ci-456-0123456-1760000000", "--apply", "--action", "post-replies"}
		if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
			t.Errorf("args = %v, want them to start %v", args, want)
		}
	})

	t.Run("pr-comments task runs care-comments", func(t *testing.T) {
		task := &api.QueueTask{
			Recipe: "care-comments",
			Rule:   "comments", Apply: "post-replies",
			Type:       api.TypePRRule,
			URL:        "https://github.com/test-owner/test-repo/pull/456",
			Number:     456,
			CommitSHA:  "0123456789abcdef",
			EnqueuedAt: time.Unix(1760000000, 0),
		}
		args := r.BuildArgs(task, "coder-bot")
		want := []string{"recipe", "care-comments", "--url", task.URL, "--run-name", "comments-456-0123456-1760000000", "--apply", "--action", "post-replies"}
		if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
			t.Errorf("args = %v, want them to start %v", args, want)
		}
	})

	t.Run("pr-iterate task runs care-rebase", func(t *testing.T) {
		task := &api.QueueTask{
			Recipe: "care-rebase",
			Rule:   "conflict", Apply: "post-replies",
			Type:       api.TypePRRule,
			URL:        "https://github.com/test-owner/test-repo/pull/456",
			Number:     456,
			CommitSHA:  "0123456789abcdef",
			EnqueuedAt: time.Unix(1760000000, 0),
		}
		args := r.BuildArgs(task, "coder-bot")
		want := []string{"recipe", "care-rebase", "--url", task.URL, "--run-name", "conflict-456-0123456-1760000000", "--apply", "--action", "post-replies"}
		if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
			t.Errorf("args = %v, want them to start %v", args, want)
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
		Type:   api.TypeIssueRule,
		Recipe: "fix",
		Rule:   "fix",
		Number: 10,
		URL:    "https://github.com/test-owner/test-repo/issues/10",
	}
	if err := r.Run(context.Background(), "task-10.yaml", task, "coder-bot"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if gotName != "/usr/bin/factory" {
		t.Errorf("expected the factory executable to be invoked, got %q", gotName)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "recipe" || gotArgs[1] != "fix" {
		t.Errorf("expected 'recipe fix' args, got %v", gotArgs)
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

	task := &api.QueueTask{Type: api.TypeIssueRule, Number: 11}
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

	task := &api.QueueTask{Type: api.TypeIssueRule, Number: 13}
	if err := r.Run(context.Background(), "task-13.yaml", task, ""); err == nil {
		t.Error("expected an error when the factory executable cannot be resolved")
	}
}

// Recovery requeues every task (Recover), which is only safe because
// each one's command names its run, by what it runs: an agent definition
// (agent create, a chore's run name) - even when its rule stamped a recipe
// too, as a workflow step's does - else a rule's recipe.
func TestEveryTaskNamesItsRun(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	at := time.Unix(1760000000, 0)
	for name, tc := range map[string]struct {
		task    *api.QueueTask
		command string
		want    func(*api.QueueTask) string
	}{
		"a rule task":     {&api.QueueTask{Type: api.TypePRRule, URL: "https://github.com/o/r/pull/1", Number: 1, Rule: "ci", Recipe: "care-ci", EnqueuedAt: at}, "recipe", api.RuleRunName},
		"a workflow step": {&api.QueueTask{Type: api.TypeAgentChore, URL: "https://github.com/o/r/issues/1", Number: 1, AgentFile: ".agents/workflows/x.md", Rule: "workflow", Recipe: "workflow", EnqueuedAt: at}, "agent", api.ChoreRunName},
		"a chore":         {&api.QueueTask{Type: api.TypeAgentChore, URL: "https://github.com/o/r", AgentFile: ".agents/x.md", EnqueuedAt: at}, "agent", api.ChoreRunName},
		"a chore recipe":  {&api.QueueTask{Type: api.TypeAgentChore, URL: "https://github.com/o/r", AgentFile: ".agents/retro.md", Recipe: "retro", EnqueuedAt: at}, "recipe", api.ChoreRunName},
	} {
		args := r.BuildArgs(tc.task, "coder-bot")
		if len(args) == 0 || args[0] != tc.command {
			t.Errorf("%s: BuildArgs %v, want factory %s", name, args, tc.command)
			continue
		}
		if want := tc.want(tc.task); !slices.Contains(args, "--run-name") || args[slices.Index(args, "--run-name")+1] != want {
			t.Errorf("%s: BuildArgs %v, want --run-name %s", name, args, want)
		}
	}
	// Neither an agent definition nor a recipe: nothing to run.
	if args := r.BuildArgs(&api.QueueTask{Type: api.TypePRRule, Number: 1}, "coder-bot"); args != nil {
		t.Errorf("a task that runs nothing builds %v", args)
	}
}

func TestCLIRunner_BuildArgs_ChoreRecipe(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	at := time.Unix(1760000000, 0)
	task := &api.QueueTask{
		Type:       api.TypeAgentChore,
		URL:        "https://github.com/GoogleCloudPlatform/k8s-config-connector",
		AgentFile:  ".agents/retro.md",
		AgentName:  "weekly-retro",
		Recipe:     "retro",
		Inputs:     map[string]string{"sample_size": "20"},
		EnqueuedAt: at,
	}
	args := r.BuildArgs(task, "test-user")
	wantPrefix := []string{"recipe", "retro", "--url", task.URL, "--run-name", api.RunNameOf(task), "--input", "sample_size=20"}
	if !slices.Equal(args[:len(wantPrefix)], wantPrefix) {
		t.Errorf("args = %v, want prefix %v", args, wantPrefix)
	}
}
