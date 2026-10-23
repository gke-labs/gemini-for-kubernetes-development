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
