package tasks

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Every script rendered as lib.sh + task body. run.sh was written
// without being added here, which is how it shipped for two days
// missing the token bridge below — the contract this file exists to
// enforce was never applied to it. A new task script belongs in this
// list on the commit that adds it.
var taskScripts = []string{
	"address_feedback.sh", "adopt.sh", "fix_issue.sh",
	"investigate_failures.sh", "iterate.sh", "plan_issue.sh", "review.sh",
	"run.sh", "run_agent.sh",
}

// Functions that live only in lib.sh — every rendered script must define
// them exactly once (a duplicate means a script kept a stale copy).
var libOnly = []string{"setupGit", "setGitHubURLRewrite", "configureGemini", "record_gemini_usage", "record_claude_usage", "record_engine_usage", "runEngine"}

// The engine seam: every task that drives the model does it through
// runEngine with its task-specific knobs; no script carries a private
// engine loop anymore (run_agent's runAgent is the documented exception
// until the agent flow's engine follow-up).
var engineCalls = map[string]string{
	"plan_issue.sh":           "runEngine plan-output.txt",
	"review.sh":               "runEngine review-output.txt",
	"fix_issue.sh":            "\nrunEngine\n",
	"iterate.sh":              "SKIP_EMPTY_PROMPT=true runEngine",
	"address_feedback.sh":     "\nrunEngine\n",
	"investigate_failures.sh": "\nrunEngine\n",
}

// Scripts whose setupGitRepos deliberately shadows lib.sh's default
// (bash: last definition wins, and lib.sh is prepended).
var shadowsSetupGitRepos = map[string]bool{
	"fix_issue.sh": true, "iterate.sh": true, "run_agent.sh": true,
}

func defCount(script, fn string) int {
	return len(regexp.MustCompile(`(?m)^function `+fn+` \{`).FindAllString(script, -1))
}

// The consolidation contract: rendered scripts are lib.sh + task body.
// Shared functions come from lib exactly once; per-task overrides shadow
// by redefinition; the models placeholder is resolved.
func TestRenderedScripts(t *testing.T) {
	bash, bashErr := exec.LookPath("bash")
	for _, name := range taskScripts {
		data, err := getScriptWithDefaults(name)
		if err != nil {
			t.Fatalf("%s: render failed: %v", name, err)
		}
		script := string(data)
		if strings.Contains(script, "__DEFAULT_MODELS__") {
			t.Errorf("%s: models placeholder not replaced", name)
		}
		for _, fn := range libOnly {
			if n := defCount(script, fn); n != 1 {
				t.Errorf("%s: %s defined %d times, want 1", name, fn, n)
			}
		}
		want := 1
		if shadowsSetupGitRepos[name] {
			want = 2
		}
		if n := defCount(script, "setupGitRepos"); n != want {
			t.Errorf("%s: setupGitRepos defined %d times, want %d", name, n, want)
		}
		if n := defCount(script, "runGemini"); n != 0 {
			t.Errorf("%s: stale runGemini definition survives (%d)", name, n)
		}
		if call, ok := engineCalls[name]; ok && !strings.Contains(script, call) {
			t.Errorf("%s: expected engine call %q in rendered script", name, strings.TrimSpace(call))
		}
		if !strings.HasPrefix(script, "#!/bin/bash") {
			t.Errorf("%s: rendered script must start with the lib shebang", name)
		}
		// The two names for one secret: callers pass GITHUB_TOKEN,
		// setupGit reads GITHUB_USER_TOKEN, and lib.sh bridges
		// neither. A script that skips the bridge still runs — it
		// writes an empty oauth_token into gh's hosts.yml and a
		// passwordless credential into git, and then dies somewhere
		// unrelated, in gh's words, about dbus.
		if strings.Contains(script, "\nsetupGit\n") && !strings.Contains(script, "GITHUB_USER_TOKEN:-${GITHUB_TOKEN}") {
			t.Errorf("%s: calls setupGit without bridging GITHUB_TOKEN to GITHUB_USER_TOKEN", name)
		}
		// execution.log is the script's xtrace; the token resolution must
		// run with it off.
		if i := strings.Index(script, "export GITHUB_USER_TOKEN="); i >= 0 {
			before := script[:i]
			if strings.LastIndex(before, "\nset -x") > strings.LastIndex(before, "\n{ set +x; } 2>/dev/null") {
				t.Errorf("%s: resolves GITHUB_USER_TOKEN under set -x", name)
			}
		}
		if bashErr == nil {
			cmd := exec.Command(bash, "-n", "/dev/stdin")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s: bash -n failed: %v\n%s", name, err, out)
			}
		}
	}
	if bashErr != nil {
		t.Logf("bash not found; syntax check skipped")
	}
}
