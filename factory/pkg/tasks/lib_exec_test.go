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
