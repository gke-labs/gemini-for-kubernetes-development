package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario suite beside the rules decides as it expects: a rule change
// that changes one of these decisions fails here.
func TestWatchRulesScenarios(t *testing.T) {
	out := runWatchRules(t, "decide", "-f", "../watch/rules/scenarios")
	if !strings.Contains(out, " scenarios, 0 failed") {
		t.Errorf("scenarios:\n%s", out)
	}
}

// A scenario that decides otherwise than it expects fails the command; one
// without a kind does not load; -o json gives each one's result.
func TestWatchRulesDecide(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wrong := write("wrong.yaml", "name: wrong\nenv: {pool: [bot], now: 2026-10-20T12:00:00Z}\nitem: {kind: pr, author: bot, labels: [overseer], checks: failing, mergeable: true}\nexpect: [review run]\n")
	cmd := newWatchRulesCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"decide", "-f", wrong})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "1 of 1 scenarios decided otherwise") {
		t.Errorf("a wrong expectation: %v", err)
	}
	if !strings.Contains(out.String(), "expect  review run  ✗ FAIL") || !strings.Contains(out.String(), "decides ci run") {
		t.Errorf("output:\n%s", out.String())
	}

	nokind := write("nokind.yaml", "item: {number: 3}\n")
	cmd = newWatchRulesCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"decide", "-f", nokind})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "needs a kind") {
		t.Errorf("no kind: %v", err)
	}

	right := write("right.yaml", "env: {pool: [bot], now: 2026-10-20T12:00:00Z}\nitem: {kind: pr, author: bot, labels: [overseer], checks: failing, mergeable: true}\nexpect: [ci run]\n")
	var results []struct {
		Decides []string `json:"decides"`
		Pass    *bool    `json:"pass"`
	}
	if err := json.Unmarshal([]byte(runWatchRules(t, "decide", "-f", right, "-o", "json")), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Pass == nil || !*results[0].Pass || strings.Join(results[0].Decides, ",") != "ci run" {
		t.Errorf("json = %+v", results)
	}
}
