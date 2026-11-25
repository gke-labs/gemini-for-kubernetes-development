package dispatcher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// A failed child's log says what went wrong: the last "Error:" line.
func TestFailureFromLog(t *testing.T) {
	for name, tc := range map[string]struct {
		log, reason string
	}{
		"apply": {
			log: "::step 8:ask (ask)\nRecipe care-comments completed.\n\nApplying the result of task recipe-care-comments-1...\n" +
				"Error: applying the Change for https://github.com/o/r/pull/12155: replying to comment 5554069132: it is neither a review comment nor a comment\n" +
				"Error: applying the Change for https://github.com/o/r/pull/12155: replying to comment 5554069132: it is neither a review comment nor a comment\n",
			reason: "applying the Change for https://github.com/o/r/pull/12155: replying to comment 5554069132: it is neither a review comment nor a comment",
		},
		"run": {
			log:    "::step 8:ask (ask)\nError: step 8:ask: agent: prompt failed: RPC error [500]\nError: running recipe: task failed with exit code 1\n",
			reason: "running recipe: task failed with exit code 1",
		},
		"no error line": {log: "panic: something\n"},
	} {
		path := filepath.Join(t.TempDir(), "task.log")
		if err := os.WriteFile(path, []byte(tc.log), 0o644); err != nil {
			t.Fatal(err)
		}
		if reason := failureFromLog(path); reason != tc.reason {
			t.Errorf("%s: failureFromLog = %q; want %q", name, reason, tc.reason)
		}
	}
	long := filepath.Join(t.TempDir(), "long.log")
	_ = os.WriteFile(long, []byte("Error: "+strings.Repeat("x", 1000)+"\n"), 0o644)
	if reason := failureFromLog(long); len(reason) > maxReason+len("…") {
		t.Errorf("reason is %d bytes, want at most %d", len(reason), maxReason)
	}
	if reason := failureFromLog(filepath.Join(t.TempDir(), "missing.log")); reason != "" {
		t.Errorf("missing log = %q; want nothing", reason)
	}
}

// A child that fails returns its exit status with the error its log
// printed.
func TestCLIRunner_Run_ReturnsTheLoggedError(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	r.resolveExecutable = func() (string, error) { return "/usr/bin/factory", nil }
	r.execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `echo "Applying the result of task t..."; echo "Error: applying the Change: replying to comment 1: nope"; exit 1`)
	}
	err := r.Run(context.Background(), "task-pr-1-rule-comments.yaml", &api.QueueTask{Type: api.TypePRRule, Number: 1, Recipe: "care-comments"}, "")
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run = %v, want a *RunError", err)
	}
	if runErr.Error() != "exit status 1: applying the Change: replying to comment 1: nope" {
		t.Errorf("RunError = %q", runErr.Error())
	}
}

// A run its engine could not serve - gemini's "RPC error [503]: This model
// is currently experiencing high demand", a 429, RESOURCE_EXHAUSTED - is
// told apart from the task's own failure, and its reason is the engine's
// line, not the "task failed" factory prints after it.
func TestFailureOfLogEngineUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		log         string
		reason      string
		unavailable bool
	}{
		"gemini at capacity (#14839)": {
			"::failed 8:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.\n" +
				"Error: step 8:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.\n" +
				"Error: running recipe: task failed with exit code 1\nError: running recipe: task failed with exit code 1\n",
			"step 8:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.", true},
		// factory now adds the failed step to the task's error, so the last
		// error line says it on its own.
		"the failed step in factory's own error": {"Error: running recipe: task failed with exit code 1: 3:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand.\n",
			"running recipe: task failed with exit code 1: 3:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand.", true},
		"rate limited": {"Error: step 3:ask: agent: prompt failed: RPC error [429]: RESOURCE_EXHAUSTED\nError: running recipe: task failed with exit code 1\n",
			"step 3:ask: agent: prompt failed: RPC error [429]: RESOURCE_EXHAUSTED", true},
		"the model's API out of reach (#14849)": {"Error: step 8:ask: agent: prompt failed: RPC error [500]: exception TypeError: fetch failed sending request\nError: running recipe: task failed with exit code 1\n",
			"step 8:ask: agent: prompt failed: RPC error [500]: exception TypeError: fetch failed sending request", true},
		"the task's own failure": {"Error: step 9:push: no commit on top of abc\nError: running recipe: task failed with exit code 1\n",
			"running recipe: task failed with exit code 1", false},
		"503 only in the agent's output, not an error line": {"the agent fetched a page: 503 Service Unavailable\nError: running recipe: task failed with exit code 1\n",
			"running recipe: task failed with exit code 1", false},
	} {
		path := filepath.Join(t.TempDir(), "task.log")
		if err := os.WriteFile(path, []byte(tc.log), 0o644); err != nil {
			t.Fatal(err)
		}
		reason, unavailable := failureOfLog(path)
		if reason != tc.reason || unavailable != tc.unavailable {
			t.Errorf("%s: failureOfLog = %q, %v; want %q, %v", name, reason, unavailable, tc.reason, tc.unavailable)
		}
	}
}
