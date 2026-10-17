package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func runWatchRules(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newWatchRulesCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("watch rules %v: %v", args, err)
	}
	return out.String()
}

func TestWatchRulesTable(t *testing.T) {
	out := runWatchRules(t)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME ON GROUP RUN AS POLICY ENABLED" {
		t.Fatalf("header = %v", got)
	}
	var names []string
	for _, l := range lines[1:] {
		names = append(names, strings.Fields(l)[0])
	}
	// Issues, then PRs (ungrouped, then pr-care in order), then the rest.
	want := "adopt-issue fix stop workflow adopt-pr inactive inherit-labels comments conflict ci review ready chore nudge"
	if strings.Join(names, " ") != want {
		t.Errorf("rows = %v, want %s", names, want)
	}
	for _, sub := range []string{
		"pr-care/10", "pr-care/last",
		"care-comments → post-replies", "fix → open-pr",
		"dedupe=item cooldown=10m/20m phase=3",
		"give-up=3/head",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("table lacks %q:\n%s", sub, out)
		}
	}
}

func TestWatchRulesYAMLAndJSON(t *testing.T) {
	var fromYAML []map[string]any
	if err := yaml.Unmarshal([]byte(runWatchRules(t, "-o", "yaml")), &fromYAML); err != nil {
		t.Fatal(err)
	}
	var fromJSON []map[string]any
	if err := json.Unmarshal([]byte(runWatchRules(t, "-o", "json", "--url", "https://github.com/o/r")), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if len(fromYAML) != 14 || len(fromJSON) != 14 {
		t.Fatalf("yaml %d rules, json %d", len(fromYAML), len(fromJSON))
	}
	if fromYAML[1]["name"] != "fix" || fromJSON[1]["name"] != "fix" {
		t.Errorf("second rule = %v / %v", fromYAML[1]["name"], fromJSON[1]["name"])
	}
	policy := fromJSON[1]["policy"].(map[string]any)
	if cd := policy["cooldown"].(map[string]any); cd["success"] != "10m" {
		t.Errorf("cooldown = %v", cd)
	}
}

func TestWatchRulesUnknownOutput(t *testing.T) {
	cmd := newWatchRulesCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"-o", "xml"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "want yaml or json") {
		t.Errorf("err = %v", err)
	}
}
