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
			// Both stubs fail on the first model ("badmodel") to prove the
			// fallback loop, succeed on the second.
			writeStub(bin, "gemini", `cat > /dev/null
case "$*" in *badmodel*) exit 1 ;; esac
echo '{"response":"GEMINI RESPONSE","stats":{"models":{"gemini-test":{"api":{"totalRequests":1},"tokens":{"input":10,"output":5,"total":15}}}}}'`)
			writeStub(bin, "claude", `cat > /dev/null
case "$*" in *badmodel*) exit 1 ;; esac
echo '{"type":"result","result":"CLAUDE RESPONSE","usage":{"input_tokens":10,"output_tokens":5},"modelUsage":{"claude-test":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":2,"costUSD":0.01}},"num_turns":3,"duration_ms":1000,"session_id":"s"}'`)

			libPath := filepath.Join(home, "lib.sh")
			if err := os.WriteFile(libPath, lib, 0644); err != nil {
				t.Fatal(err)
			}
			promptPath := filepath.Join(taskDir, "agent-prompt.txt")
			if err := os.WriteFile(promptPath, []byte("prompt"), 0644); err != nil {
				t.Fatal(err)
			}

			harness := `set -e
source "$LIB"
cd() { :; } # the repo checkout does not exist in the test sandbox
runEngine plan-output.txt`
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
			m, ok := parsed.Models[tc.wantModel]
			if !ok || m.Tokens["input"] != 10 || m.Tokens["output"] != 5 {
				t.Errorf("usage for %s = %+v (present=%v), want input=10 output=5\n%s", tc.wantModel, m, ok, usage)
			}
		})
	}
}
