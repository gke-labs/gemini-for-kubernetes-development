package tasks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunEngineContract executes lib.sh's runEngine against stub engine
// binaries — the closest thing to a live smoke that runs in CI. It pins
// the per-engine contract: invocation → <engine>-output.json, response
// extraction (.response vs .result) → the task output file, usage mapped
// into llm-usage.json, and model fallback on failure.
func TestRunEngineContract(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}

	writeStub := func(dir, name, script string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+script), 0755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		engine, wantText, wantJSON, wantModel string
	}{
		{"gemini", "GEMINI RESPONSE", "gemini-output.json", "gemini-test"},
		{"claude", "CLAUDE RESPONSE", "claude-output.json", "claude-test"},
		// agy names no model in its JSON; usage is booked under the one
		// runEngine asked for.
		{"antigravity", "AGY RESPONSE", "antigravity-output.json", "goodmodel"},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			taskDir := filepath.Join(home, "task")
			for _, d := range []string{bin, taskDir} {
				if err := os.MkdirAll(d, 0755); err != nil {
					t.Fatal(err)
				}
			}
			// Both stubs record their argv (the invocation-line contract)
			// and fail on the first model ("badmodel") to prove fallback.
			writeStub(bin, "gemini", `cat > /dev/null
echo "$*" >> "$(dirname "$0")/gemini.args"
case "$*" in *badmodel*) exit 1 ;; esac
echo '{"response":"GEMINI RESPONSE","stats":{"models":{"gemini-test":{"api":{"totalRequests":1},"tokens":{"input":10,"output":5,"total":15}}}}}'`)
			writeStub(bin, "claude", `cat > /dev/null
echo "sandbox=${IS_SANDBOX:-} $*" >> "$(dirname "$0")/claude.args"
case "$*" in *badmodel*) exit 1 ;; esac
echo '{"type":"result","result":"CLAUDE RESPONSE","usage":{"input_tokens":10,"output_tokens":5},"modelUsage":{"claude-test":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":2,"costUSD":0.01}},"num_turns":3,"duration_ms":1000,"session_id":"s"}'`)
			// agy takes the prompt as an argument and must not be fed
			// stdin; it only uses GEMINI_API_KEY when settings.json names
			// the gemini provider. badmodel exits 0 with a WAITING status
			// — a soft-denied permission — which must still fall back.
			writeStub(bin, "agy", `if [ -t 0 ] || [ -n "$(cat)" ]; then echo "stdin was fed" >&2; exit 7; fi
grep -q '"modelProvider": "gemini"' "$HOME/.gemini/antigravity-cli/settings.json" || exit 8
echo "update=${AGY_CLI_DISABLE_AUTO_UPDATE:-} $*" >> "$(dirname "$0")/antigravity.args"
case "$*" in *badmodel*) echo '{"status":"WAITING","response":""}'; exit 0 ;; esac
echo '{"conversation_id":"c","status":"SUCCESS","response":"AGY RESPONSE","duration_seconds":2.5,"num_turns":2,"usage":{"input_tokens":10,"output_tokens":5,"thinking_tokens":3,"cache_read_tokens":4,"total_tokens":15}}'`)

			libPath := filepath.Join(home, "lib.sh")
			if err := os.WriteFile(libPath, lib, 0644); err != nil {
				t.Fatal(err)
			}
			if tc.engine == "claude" {
				// A transcript in the shape observed on a live claude run
				// (fix-substrate-1763): tool_use pairs with tool_result by
				// id, 2.5s apart → the telemetry miner's input.
				proj := filepath.Join(home, ".claude", "projects", "-workspaces-repo-under-test")
				if err := os.MkdirAll(proj, 0755); err != nil {
					t.Fatal(err)
				}
				transcript := `{"type":"assistant","timestamp":"2026-09-20T06:53:01.500Z","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...","description":"run tests"}}]}}
{"type":"user","timestamp":"2026-09-20T06:53:04.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1"}]}}
{"type":"assistant","timestamp":"2026-09-20T06:53:05.000Z","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/workspaces/x.go"}}]}}
{"type":"user","timestamp":"2026-09-20T06:53:05.400Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2"}]}}
`
				if err := os.WriteFile(filepath.Join(proj, "session.jsonl"), []byte(transcript), 0644); err != nil {
					t.Fatal(err)
				}
			}
			promptPath := filepath.Join(taskDir, "agent-prompt.txt")
			if err := os.WriteFile(promptPath, []byte("prompt"), 0644); err != nil {
				t.Fatal(err)
			}

			harness := `set -e
source "$LIB"
cd() { :; } # the repo checkout does not exist in the test sandbox
runEngine plan-output.txt
GEMINI_CONTINUE_SESSION=true runEngine plan-output.txt`
			cmd := exec.Command(bash, "-c", harness)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+":"+os.Getenv("PATH"),
				"HOME="+home,
				"LIB="+libPath,
				"PROMPT_FILE="+promptPath,
				"MODELS=badmodel goodmodel",
				"REPO_NAME=repo-under-test",
				"ENGINE="+tc.engine,
				"GEMINI_API_KEY=test-key",
				"ANTHROPIC_API_KEY=test-key",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("runEngine failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "Retrying with next model") {
				t.Errorf("model fallback did not trigger:\n%s", out)
			}

			text, err := os.ReadFile(filepath.Join(taskDir, "plan-output.txt"))
			if err != nil || strings.TrimSpace(string(text)) != tc.wantText {
				t.Errorf("extracted output = %q, %v; want %q", strings.TrimSpace(string(text)), err, tc.wantText)
			}
			if _, err := os.Stat(filepath.Join(taskDir, tc.wantJSON)); err != nil {
				t.Errorf("engine json missing: %v", err)
			}
			// The invocation line IS the compatibility contract: gemini must
			// be called exactly as it was before the engine seam existed.
			args, err := os.ReadFile(filepath.Join(bin, tc.engine+".args"))
			if err != nil {
				t.Fatalf("engine argv not recorded: %v", err)
			}
			wantArgs := map[string][]string{
				"gemini": {
					"--yolo --model goodmodel --output-format json",
					"--yolo --model goodmodel --output-format json --resume latest",
				},
				// IS_SANDBOX=1 is required: Claude Code refuses
				// --dangerously-skip-permissions as root without it.
				"claude": {
					"sandbox=1 -p --dangerously-skip-permissions --model goodmodel --output-format json",
					"sandbox=1 -p --dangerously-skip-permissions --model goodmodel --output-format json --continue",
				},
				// --print-timeout: agy's 5m default would kill every
				// real task.
				"antigravity": {
					"update=true -p prompt --dangerously-skip-permissions --output-format json --print-timeout 24h --model goodmodel",
					"update=true -p prompt --dangerously-skip-permissions --output-format json --print-timeout 24h --model goodmodel --continue",
				},
			}[tc.engine]
			var succeeded []string
			for _, l := range strings.Split(strings.TrimSpace(string(args)), "\n") {
				if !strings.Contains(l, "badmodel") {
					succeeded = append(succeeded, l)
				}
			}
			if len(succeeded) != 2 || succeeded[0] != wantArgs[0] || succeeded[1] != wantArgs[1] {
				t.Errorf("%s argv = %q, want %q", tc.engine, succeeded, wantArgs)
			}

			usage, err := os.ReadFile(filepath.Join(taskDir, "llm-usage.json"))
			if err != nil {
				t.Fatalf("llm-usage.json missing: %v", err)
			}
			var parsed struct {
				Models map[string]struct {
					Tokens map[string]int `json:"tokens"`
				} `json:"models"`
			}
			if err := json.Unmarshal(usage, &parsed); err != nil {
				t.Fatalf("llm-usage.json unparseable: %v\n%s", err, usage)
			}
			// The harness runs runEngine twice (plain + resume), and usage
			// accumulates across runs of a task — 2 × (10 in / 5 out).
			m, ok := parsed.Models[tc.wantModel]
			if !ok || m.Tokens["input"] != 20 || m.Tokens["output"] != 10 {
				t.Errorf("usage for %s = %+v (present=%v), want accumulated input=20 output=10\n%s", tc.wantModel, m, ok, usage)
			}

			if tc.engine == "claude" {
				tel, err := os.ReadFile(filepath.Join(taskDir, "tool-telemetry.json"))
				if err != nil {
					t.Fatalf("tool-telemetry.json missing for claude: %v", err)
				}
				var tm struct {
					TotalToolCalls int `json:"total_tool_calls"`
					Tools          map[string]struct {
						Count    int     `json:"count"`
						TotalSec float64 `json:"total_sec"`
					} `json:"tools"`
					ShellCalls []struct {
						Cmd string `json:"cmd"`
					} `json:"shell_calls"`
				}
				if err := json.Unmarshal(tel, &tm); err != nil {
					t.Fatalf("tool-telemetry.json unparseable: %v\n%s", err, tel)
				}
				if tm.TotalToolCalls != 2 || tm.Tools["Bash"].Count != 1 || tm.Tools["Bash"].TotalSec != 2.5 || tm.Tools["Read"].Count != 1 {
					t.Errorf("telemetry = %+v, want 2 calls (Bash 2.5s, Read)\n%s", tm, tel)
				}
				if len(tm.ShellCalls) != 1 || tm.ShellCalls[0].Cmd != "go test ./..." {
					t.Errorf("shell_calls = %+v, want the Bash command", tm.ShellCalls)
				}
			}
		})
	}
}

// TestSetGitHubURLRewrite pins the fix for sandboxes shared by several
// identities: HOME is on the PVC, a review task runs as the reviewer bot,
// then an iterate task runs as the PR's coder bot. Before the fix both
// credential-bearing insteadOf rewrites piled up in ~/.gitconfig and git
// used the first (the reviewer's), so every push to the coder's fork was
// a 403 (seen on GoogleCloudPlatform/k8s-config-connector#13301).
func TestSetGitHubURLRewrite(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}

	// run sources lib.sh into a fresh HOME with an unrelated rewrite that
	// must survive, runs body, and returns the output and ~/.gitconfig.
	run := func(t *testing.T, body string) (string, string) {
		t.Helper()
		home := t.TempDir()
		libPath := filepath.Join(home, "lib.sh")
		if err := os.WriteFile(libPath, lib, 0644); err != nil {
			t.Fatal(err)
		}
		harness := `set -e
source "$LIB"
git config --global url."https://mirror.example/".insteadOf "https://example.org/"
` + body + `
echo "RESOLVED=$(git ls-remote --get-url https://github.com/owner/repo.git)"`
		cmd := exec.Command(bash, "-c", harness)
		cmd.Env = append(os.Environ(),
			// The trace assertions match bash's default prefix; bash
			// inherits an exported PS4 when not running as root.
			"PS4=+ ",
			"HOME="+home,
			"LIB="+libPath,
			"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
			"GIT_CONFIG_NOSYSTEM=1",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("harness failed: %v\n%s", err, out)
		}
		cfg, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(cfg), "https://mirror.example/") {
			t.Errorf("unrelated rewrite was removed:\n%s", cfg)
		}
		return string(out), string(cfg)
	}

	t.Run("reviewer then coder", func(t *testing.T) {
		// The tasks run under set -x, which must print neither token and
		// must be back on after the call; with tracing off it stays off.
		out, cfg := run(t, `export GITHUB_USER_TOKEN=REVIEWER_TOKEN
set -x
setGitHubURLRewrite reviewbot-robot
set +x
export GITHUB_USER_TOKEN=CODER_TOKEN
setGitHubURLRewrite lovelace-coder-bot
echo UNTRACED_AFTER
set -x
setGitHubURLRewrite lovelace-coder-bot
echo TRACED_AFTER
set +x`)

		if want := "RESOLVED=https://lovelace-coder-bot:CODER_TOKEN@github.com/owner/repo.git"; !strings.Contains(out, want) {
			t.Errorf("github.com resolved to the wrong identity; want %q in:\n%s", want, out)
		}
		for _, l := range strings.Split(out, "\n") {
			// The RESOLVED line is the harness's own output, not a trace.
			if strings.HasPrefix(l, "+") && (strings.Contains(l, "REVIEWER_TOKEN") || strings.Contains(l, "CODER_TOKEN")) {
				t.Errorf("token leaked into the xtrace: %q", l)
			}
		}
		if !strings.Contains(out, "+ echo TRACED_AFTER") {
			t.Errorf("xtrace was not restored after the call:\n%s", out)
		}
		if strings.Contains(out, "+ echo UNTRACED_AFTER") {
			t.Errorf("xtrace was turned on by a call made with tracing off:\n%s", out)
		}
		if strings.Contains(cfg, "REVIEWER_TOKEN") {
			t.Errorf("previous identity's token left on disk:\n%s", cfg)
		}
		if n := strings.Count(cfg, "insteadOf = https://github.com/"); n != 1 {
			t.Errorf("want exactly 1 github.com rewrite, got %d:\n%s", n, cfg)
		}
	})

	t.Run("empty token", func(t *testing.T) {
		// A passwordless rewrite would override gh's helper; the stale
		// one must still go, leaving github.com to the helper.
		out, cfg := run(t, `export GITHUB_USER_TOKEN=REVIEWER_TOKEN
setGitHubURLRewrite reviewbot-robot
export GITHUB_USER_TOKEN=
setGitHubURLRewrite lovelace-coder-bot`)

		if want := "RESOLVED=https://github.com/owner/repo.git"; !strings.Contains(out, want) {
			t.Errorf("github.com still rewritten with no token; want %q in:\n%s", want, out)
		}
		if strings.Contains(cfg, "github.com/") {
			t.Errorf("want no github.com rewrite, got:\n%s", cfg)
		}
	})
}

// runLib sources lib.sh in bash with HOME in a temp dir and a git
// repository standing in for /workspaces/$REPO_NAME (every cd lands there),
// runs body, and returns the combined output and the repository's path.
func runLib(t *testing.T, env []string, setup, body string) (string, string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	libPath := filepath.Join(home, "lib.sh")
	if err := os.WriteFile(libPath, lib, 0644); err != nil {
		t.Fatal(err)
	}
	harness := `set -e
source "$LIB"
git init -q "$REPO_DIR"
cd() { builtin cd "$REPO_DIR"; }
sleep() { :; }
` + setup + `
` + body
	cmd := exec.Command(bash, "-c", harness)
	cmd.Env = append(os.Environ(),
		"PS4=+ ",
		"HOME="+home,
		"LIB="+libPath,
		"REPO_DIR="+repo,
		"REPO_NAME=repo",
		"REPO_OWNER=upstream-org",
		"PATH="+filepath.Join(home, "bin")+":"+os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	cmd.Env = append(cmd.Env, env...)
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), repo, err
}

// engineGitSetup is what setupGit leaves behind, with a gh stub that answers
// `gh auth git-credential get` the way gh does from hosts.yml.
const engineGitSetup = `
mkdir -p "$HOME/bin" && cat > "$HOME/bin/gh" <<'STUB'
#!/bin/bash
[ "$1 $2 $3" = "auth git-credential get" ] && printf 'username=coder-bot\npassword=HOSTS_YML_TOKEN\n'
STUB
chmod +x "$HOME/bin/gh"
git config --global user.name "coder bot"
git config --global core.hooksPath /dev/null
git config --global credential.https://github.com.helper ""
git config --global --add credential.https://github.com.helper "!$HOME/bin/gh auth git-credential"
export GITHUB_USER_TOKEN=CODER_TOKEN
setGitHubURLRewrite coder-bot
unset GITHUB_USER_TOKEN
builtin cd "$REPO_DIR"
echo one > f && git add f && git -c user.email=x@x commit -qm one && echo two > f`

// engineGitReport prints what the engine's git sees.
const engineGitReport = `
echo "RESOLVED=$(git ls-remote --get-url https://github.com/owner/repo.git)"
echo "NAME=$(git config user.name)"
echo "CREDENTIAL=$(printf 'protocol=https\nhost=github.com\n\n' | GIT_TERMINAL_PROMPT=0 git credential fill 2>&1 | grep password)"
echo "LISTED_TOKENS=$(git config --list | grep -c CODER_TOKEN)"
echo "ENV_TOKENS=$(env | grep -c CODER_TOKEN)"`

// TestEngineGitConfig pins gemini-cli 0.62+'s engine git: every shell
// command runs with GIT_CONFIG_GLOBAL=/dev/null and gemini's overrides
// appended. Without the include the identity and credentials were gone and
// pushes failed with "could not read Username" (k8s-config-connector#13619);
// with the whole global config included, `git config --list` printed the
// token from the github.com rewrite into the trace
// (fix-k8s-config-connector-13652); and gemini's diff.external="" broke
// every plain `git diff`.
func TestEngineGitConfig(t *testing.T) {
	out, _, err := runLib(t, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=foo.inherited", "GIT_CONFIG_VALUE_0=kept"}, engineGitSetup, `
engineGitConfig
# What gemini-cli does to every shell command it runs.
n=$GIT_CONFIG_COUNT
export GIT_CONFIG_GLOBAL=/dev/null
export "GIT_CONFIG_KEY_${n}=credential.helper" "GIT_CONFIG_VALUE_${n}="
export "GIT_CONFIG_KEY_$((n+1))=core.hooksPath" "GIT_CONFIG_VALUE_$((n+1))="
export "GIT_CONFIG_KEY_$((n+2))=diff.external" "GIT_CONFIG_VALUE_$((n+2))="
export GIT_CONFIG_COUNT=$((n+3))
`+engineGitReport+`
echo "INHERITED=$(git config foo.inherited)"
echo "HOOKS=[$(git config core.hooksPath)]"
echo "DIFF=$(git diff 2>&1 | grep -e '^+two' -e 'external diff')"`)
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"RESOLVED=https://github.com/owner/repo.git",
		"NAME=coder bot",
		"CREDENTIAL=password=HOSTS_YML_TOKEN",
		"LISTED_TOKENS=0",
		"ENV_TOKENS=0",
		"INHERITED=kept",
		// gemini's other overrides come after the include and still win.
		"HOOKS=[]",
		"DIFF=+two",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

// TestEngineGitGlobal pins the same for claude and agy, which leave git's
// config alone: the global config they read must not carry the token.
func TestEngineGitGlobal(t *testing.T) {
	out, _, err := runLib(t, nil, engineGitSetup, `
echo "SCRIPT_RESOLVED=$(git ls-remote --get-url https://github.com/owner/repo.git)"
engineGitGlobal
`+engineGitReport)
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		// The script's own git keeps the rewrite.
		"SCRIPT_RESOLVED=https://coder-bot:CODER_TOKEN@github.com/owner/repo.git",
		"RESOLVED=https://github.com/owner/repo.git",
		"NAME=coder bot",
		"CREDENTIAL=password=HOSTS_YML_TOKEN",
		"LISTED_TOKENS=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

// TestEnsureForkRemote pins the fix for k8s-config-connector#13635: the
// fork call answered 429, the error was ignored, origin stayed the upstream
// repository, and the agent pushed its branch there.
func TestEnsureForkRemote(t *testing.T) {
	// The gh stub fails as often as $HOME/fails says, then does what
	// `gh repo fork --remote` does: upstream ← origin, origin ← the fork.
	ghStub := `mkdir -p "$HOME/bin" && cat > "$HOME/bin/gh" <<'STUB'
#!/bin/bash
echo call >> "$HOME/gh.calls"
f=$(cat "$HOME/fails" 2>/dev/null || echo 0)
if [ "$f" -gt 0 ]; then echo $((f-1)) > "$HOME/fails"; echo "HTTP 429" >&2; exit 1; fi
git remote rename origin upstream
git remote add origin https://github.com/Coder-Bot/repo.git
STUB
chmod +x "$HOME/bin/gh"
builtin cd "$REPO_DIR" && git remote add origin https://github.com/upstream-org/repo.git`
	report := `
echo "ORIGIN=$(git remote get-url origin)"
echo "CALLS=$(wc -l < "$HOME/gh.calls" 2>/dev/null | tr -d ' ')"`

	t.Run("retries past a 429", func(t *testing.T) {
		out, _, err := runLib(t, []string{"GITHUB_BOT_LOGIN=coder-bot"}, ghStub+`
echo 2 > "$HOME/fails"`, "ensureForkRemote"+report)
		if err != nil {
			t.Fatalf("ensureForkRemote failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "ORIGIN=https://github.com/Coder-Bot/repo.git") || !strings.Contains(out, "CALLS=3") {
			t.Errorf("want origin to be the fork after 3 calls:\n%s", out)
		}
	})

	t.Run("stops when the fork never comes", func(t *testing.T) {
		out, _, err := runLib(t, []string{"GITHUB_BOT_LOGIN=coder-bot"}, ghStub+`
echo 99 > "$HOME/fails"`, "ensureForkRemote\necho REACHED")
		if err == nil || strings.Contains(out, "REACHED") {
			t.Fatalf("ensureForkRemote let the task go on with origin = upstream:\n%s", out)
		}
		if !strings.Contains(out, "stopping rather than pushing to the upstream repository") {
			t.Errorf("missing the reason:\n%s", out)
		}
	})

	t.Run("no token in the trace", func(t *testing.T) {
		// git remote get-url applies the token-bearing github.com rewrite,
		// and fix_issue.sh runs under set -x.
		out, _, err := runLib(t, []string{"GITHUB_BOT_LOGIN=coder-bot"}, ghStub+`
git remote set-url origin https://github.com/coder-bot/repo.git
GITHUB_USER_TOKEN=CODER_TOKEN setGitHubURLRewrite coder-bot`, "set -x\nensureForkRemote\nset +x")
		if err != nil {
			t.Fatalf("ensureForkRemote failed: %v\n%s", err, out)
		}
		if strings.Contains(out, "CODER_TOKEN") {
			t.Errorf("token leaked into the xtrace:\n%s", out)
		}
	})

	t.Run("origin already the fork", func(t *testing.T) {
		out, _, err := runLib(t, []string{"GITHUB_BOT_LOGIN=coder-bot"}, ghStub+`
git remote set-url origin https://github.com/coder-bot/repo.git`, "ensureForkRemote"+report)
		if err != nil {
			t.Fatalf("ensureForkRemote failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "CALLS=\n") && !strings.Contains(out, "CALLS=0") {
			t.Errorf("gh was called although origin was already the fork:\n%s", out)
		}
	})
}

// TestCheckoutDefaultBranch: after the fork, origin's default branch is
// the member's last sync, so the checkout must come from upstream; with no
// upstream (a local-only run) origin is the repository itself.
func TestCheckoutDefaultBranch(t *testing.T) {
	// upstream has a commit the stale fork lacks. gh only answers
	// `gh repo view` with the default branch.
	setup := `mkdir -p "$HOME/bin" && printf '#!/bin/bash\necho main\n' > "$HOME/bin/gh" && chmod +x "$HOME/bin/gh"
c() { git -c user.email=x@x -c user.name=x commit -q --allow-empty -m "$1"; }
git init -q -b main "$HOME/up" && (builtin cd "$HOME/up" && c old)
git clone -q "$HOME/up" "$HOME/fork"
(builtin cd "$HOME/up" && c new)
rm -rf "$REPO_DIR" && git clone -q "$HOME/fork" "$REPO_DIR" && builtin cd "$REPO_DIR"
`
	report := `
echo "HEAD=$(git log -1 --format=%s)"`

	t.Run("upstream when forked", func(t *testing.T) {
		out, _, err := runLib(t, nil, setup+`git remote add upstream "$HOME/up"`, "checkoutDefaultBranch"+report)
		if err != nil {
			t.Fatalf("checkoutDefaultBranch failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "HEAD=new") {
			t.Errorf("want upstream's latest commit, not the fork's:\n%s", out)
		}
	})

	t.Run("origin when local-only", func(t *testing.T) {
		out, _, err := runLib(t, nil, setup+`git remote set-url origin "$HOME/up"`, "checkoutDefaultBranch"+report)
		if err != nil {
			t.Fatalf("checkoutDefaultBranch failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "HEAD=new") {
			t.Errorf("want origin's latest commit:\n%s", out)
		}
	})
}

// TestResetRepoGitConfig pins the repository-level settings left behind on a
// long-lived workspace that broke later tasks: diff.external=false made
// every `git diff` fail (fix-k8s-config-connector-13580), and a repository's
// own hooksPath or credential rewrite would beat setupGit's global ones.
func TestResetRepoGitConfig(t *testing.T) {
	out, repo, err := runLib(t, nil, `builtin cd "$REPO_DIR"
git config diff.external false
git config core.pager less
git config core.hooksPath .husky
git config credential.helper store
git config url."https://someone:OLD_TOKEN@github.com/".insteadOf https://github.com/
git config url."https://mirror.example/".insteadOf https://example.org/
git config foo.unrelated kept`, `set -x
resetRepoGitConfig
set +x`)
	if err != nil {
		t.Fatalf("resetRepoGitConfig failed: %v\n%s", err, out)
	}
	cfg, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"external", "pager", ".husky", "store", "OLD_TOKEN"} {
		if strings.Contains(string(cfg), gone) {
			t.Errorf("%q still in .git/config:\n%s", gone, cfg)
		}
	}
	for _, kept := range []string{"hooksPath = /dev/null", "https://mirror.example/", "unrelated = kept"} {
		if !strings.Contains(string(cfg), kept) {
			t.Errorf("want %q in .git/config:\n%s", kept, cfg)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "+") && strings.Contains(l, "OLD_TOKEN") {
			t.Errorf("token leaked into the xtrace: %q", l)
		}
	}
}

// TestEngineEnvHasNoGitHubTokens pins that no engine inherits a GitHub
// token: agents print their environment into the recorded trace, and one
// did (k8s-config-connector#13619). The API key the engine needs stays.
func TestEngineEnvHasNoGitHubTokens(t *testing.T) {
	tokenVars := []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_USER_TOKEN", "GITHUB_BOT_TOKEN", "GITHUB_BOT_MANUAL_PAT", "GITHUB_BOT_OAUTH_PAT", "MANUAL_PAT", "OAUTH_PAT"}
	env := []string{"MODELS=m", "GEMINI_API_KEY=API_KEY_VALUE", "ANTHROPIC_API_KEY=API_KEY_VALUE"}
	for _, v := range tokenVars {
		env = append(env, v+"=SECRET_"+v)
	}
	// Each stub dumps its environment, then answers like a successful run.
	stubs := `mkdir -p "$HOME/bin" "$HOME/task"
printf 'prompt' > "$HOME/task/agent-prompt.txt"
export PROMPT_FILE="$HOME/task/agent-prompt.txt"
for e in gemini claude agy; do
cat > "$HOME/bin/$e" <<'STUB'
#!/bin/bash
env > "$HOME/$(basename "$0").env"
echo '{"response":"ok","result":"ok","status":"SUCCESS"}'
STUB
chmod +x "$HOME/bin/$e"
done
git config --global user.name "coder bot"`
	out, _, err := runLib(t, env, stubs, `
for engine in gemini claude antigravity; do ENGINE=$engine runEngine; done
for e in gemini claude agy; do
  echo "$e TOKENS=$(grep -c SECRET_ "$HOME/$e.env")"
  echo "$e KEY=$(grep -c API_KEY_VALUE "$HOME/$e.env")"
  echo "$e ENGINE_GITCONFIG=$(grep -c 'engine.gitconfig' "$HOME/$e.env")"
done
echo "SCRIPT_KEPT=${GITHUB_USER_TOKEN}"`)
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	for _, e := range []string{"gemini", "claude", "agy"} {
		if !strings.Contains(out, e+" TOKENS=0") {
			t.Errorf("%s inherited a GitHub token:\n%s", e, out)
		}
		if strings.Contains(out, e+" KEY=0") {
			t.Errorf("%s lost its API key:\n%s", e, out)
		}
		// The global config without the token: GIT_CONFIG_GLOBAL for
		// claude and agy, an include for gemini.
		if strings.Contains(out, e+" ENGINE_GITCONFIG=0") {
			t.Errorf("%s did not get the engine's git config:\n%s", e, out)
		}
	}
	if !strings.Contains(out, "SCRIPT_KEPT=SECRET_GITHUB_USER_TOKEN") {
		t.Errorf("the script's own token was dropped too:\n%s", out)
	}
}
