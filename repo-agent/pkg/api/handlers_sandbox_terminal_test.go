package api

import (
	"strings"
	"testing"
)

// The chat command is the contract for resuming a task's agent
// conversation: HOME picks which task family's sessions gemini sees
// (plan/triage/review under /root, fix/agent under the workspace home),
// the tmux session is per-task-type so it coexists with the "board"
// shell, and the API key must survive shell quoting.
func TestChatCommand(t *testing.T) {
	cmd := chatCommand("plan", chatHomeByTask["plan"], "sk-test")
	for _, want := range []string{
		"tmux new-session -A -s chat-plan",
		"export HOME=/root",
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
	if !strings.Contains(chatCommand("fix", chatHomeByTask["fix"], "k"), "HOME=/workspaces/.home") {
		t.Errorf("fix chat should use the workspace home")
	}
}
