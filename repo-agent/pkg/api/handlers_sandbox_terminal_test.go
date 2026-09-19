package api

import (
	"strings"
	"testing"
)

// The chat command is the contract for resuming a task's agent
// conversation: HOME picks where gemini finds the task's sessions (all
// factory tasks run under the workspace home, so conversations survive
// pod restarts), the tmux session is per-task-type so it coexists with
// the "board" shell, and the API key must survive shell quoting.
func TestChatCommand(t *testing.T) {
	cmd := chatCommand("plan", chatHomeByTask["plan"], "sk-test")
	for _, want := range []string{
		"tmux new-session -A -s chat-plan",
		"export HOME=/workspaces/.home",
		"gemini --resume latest",
		`GEMINI_API_KEY='\''sk-test'\''`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("chatCommand missing %q in:\n%s", want, cmd)
		}
	}
	// A quote in the key must not escape its single-quoted context (the
	// key is quoted for tmux's inner shell, then the whole inner command
	// is quoted again for the outer sh -c).
	if got := shellSingleQuote("with-'quote"); got != `'with-'\''quote'` {
		t.Errorf("shellSingleQuote = %s", got)
	}
	// Every whitelisted task type resumes under the workspace home.
	for task, home := range chatHomeByTask {
		if home != "/workspaces/.home" {
			t.Errorf("chat home for %s = %s, want /workspaces/.home", task, home)
		}
	}
}
