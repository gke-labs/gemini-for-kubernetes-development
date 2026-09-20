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
	cmd := chatCommand("plan", chatHomeByTask["plan"], "gemini", "sk-test", "")
	for _, want := range []string{
		"tmux new-session -A -s chat-plan",
		"export HOME=/workspaces/.home",
		"export GEMINI_CLI_TRUST_WORKSPACE=true",
		"cp -Rnp /root/.gemini/tmp/.",
		"trustedFolders.json",
		"gemini --skip-trust --include-directories /workspaces --resume latest",
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

// The orientation closes the resumed agent's informational gap: the task
// script, not the agent, wrote the plan file, so the agent must be told
// where the plan lives and that editing it is the tieback to the board.
// Injected via `-i` only on session creation (tmux -A skips it on attach).
func TestChatOrientation(t *testing.T) {
	o := chatOrientation("plan", "fix-substrate-1746")
	// The orientation must read as information, not a task: a resumed
	// agent given a bare pointer to the plan started implementing it.
	for _, want := range []string{
		"issue #1746",
		"/workspaces/plan-issue-1746.md",
		"informational only",
		"Do not implement the plan",
		"wait for the user",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("plan orientation missing %q in: %s", want, o)
		}
	}
	if got := chatOrientation("fix", "fix-substrate-1746"); got != "" {
		t.Errorf("fix chat has no orientation yet, got %q", got)
	}
	if got := chatOrientation("plan", "no-issue-suffix"); got != "" {
		t.Errorf("unparseable sandbox name must skip orientation, got %q", got)
	}
	cmd := chatCommand("plan", chatHomeByTask["plan"], "gemini", "k", chatOrientation("plan", "fix-substrate-1746"))
	if !strings.Contains(cmd, "--resume latest -i ") {
		t.Errorf("orientation not wired into the resume command:\n%s", cmd)
	}
}

// Sessions are engine-private: a claude board's chat must resume with
// claude (--continue), carry ANTHROPIC_API_KEY, and skip the gemini-only
// trust seeding and /root rescue shims.
func TestChatCommandClaude(t *testing.T) {
	cmd := chatCommand("plan", chatHomeByTask["plan"], "claude", "sk-ant", chatOrientation("plan", "fix-substrate-1746"))
	for _, want := range []string{
		"tmux new-session -A -s chat-plan",
		"export HOME=/workspaces/.home",
		"claude --continue",
		`ANTHROPIC_API_KEY='\''sk-ant'\''`,
		// The onboarding seed: without it every fresh sandbox greets the
		// user with Claude Code's first-run wizard instead of the chat.
		".claude.json",
		"hasTrustDialogAccepted",
		"customApiKeyResponses",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("claude chatCommand missing %q in:\n%s", want, cmd)
		}
	}
	for _, reject := range []string{"gemini", "trustedFolders", "GEMINI_API_KEY"} {
		if strings.Contains(cmd, reject) {
			t.Errorf("claude chatCommand must not contain %q:\n%s", reject, cmd)
		}
	}
}
