package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME ON ORDER RUN POLICY SOURCE ENABLED" {
		t.Fatalf("header = %v", got)
	}
	var names []string
	for _, l := range lines[1:] {
		names = append(names, strings.Fields(l)[0])
	}
	// Issues, then PRs, each in the order the engine walks them, then the rest.
	want := "workflow-missing workflow plan-asking plan-answered plan implement implement-graph implement-unplanned implement-not-ready fix issue-asking issue-answered inherit-labels inactive comments conflict ci review ready ready-unreview"
	if strings.Join(names, " ") != want {
		t.Errorf("rows = %v, want %s", names, want)
	}
	for _, sub := range []string{
		"10 exclusive", "50 exclusive",
		"care-comments → post-replies", "fix → open-pr",
		"dedupe=update cooldown=10m/20m give-up=3/human+pause phase=3",
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
	if len(fromYAML) != 20 || len(fromJSON) != 20 {
		t.Fatalf("yaml %d rules, json %d", len(fromYAML), len(fromJSON))
	}
	if fromYAML[9]["name"] != "fix" || fromJSON[9]["name"] != "fix" {
		t.Errorf("tenth rule = %v / %v", fromYAML[9]["name"], fromJSON[9]["name"])
	}
	if fromJSON[9]["order"] != float64(20) {
		t.Errorf("fix order = %v", fromJSON[9]["order"])
	}
	policy := fromJSON[9]["policy"].(map[string]any)
	if cd := policy["cooldown"].(map[string]any); cd["success"] != "10m" {
		t.Errorf("cooldown = %v", cd)
	}
	// A named duration and a list of resets read back as written.
	out := runWatchRules(t, "-o", "yaml")
	for _, sub := range []string{`success: '{{workflow-cooldown}}'`, "- pause"} {
		if !strings.Contains(out, sub) {
			t.Errorf("yaml lacks %q", sub)
		}
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

// --dir lays a checkout's .agents/watch/ over factory's: the repository
// turns review off and inactive on with a name and enabled each, and the
// table says whose each rule and enabled is.
func TestWatchRulesDirLaysTheRepositorysRulesOver(t *testing.T) {
	checkout := t.TempDir()
	dir := filepath.Join(checkout, ".agents", "watch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"review.yaml":   "name: review\nenabled: false\n",
		"inactive.yaml": "name: inactive\nenabled: true\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows := map[string][]string{}
	for _, l := range strings.Split(strings.TrimSpace(runWatchRules(t, "--dir", checkout)), "\n")[1:] {
		f := strings.Fields(l)
		rows[f[0]] = f
	}
	tail := func(name string) string {
		f := rows[name]
		if len(f) > 3 && strings.HasPrefix(f[len(f)-1], "(") {
			return strings.Join(f[len(f)-3:], " ")
		}
		return strings.Join(f[len(f)-2:], " ")
	}
	for name, want := range map[string]string{
		"review":   "factory false (repo)",
		"inactive": "factory true (repo)",
		"fix":      "factory true",
	} {
		if got := tail(name); got != want {
			t.Errorf("%s: SOURCE ENABLED = %q, want %q", name, got, want)
		}
	}

	// A checkout with no rules of its own lists factory's.
	if out := runWatchRules(t, "--dir", t.TempDir()); strings.Contains(out, "(repo)") {
		t.Errorf("a checkout without .agents/watch changed a rule:\n%s", out)
	}
}

// replay runs the rules on recorded decisions and lists where they differ.
func TestWatchRulesReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	env := `"env":{"Trigger":"overseer","Prefixes":["overseer"],"Pool":["bot"]}`
	item := `"item":{"Kind":"issue","Number":7,"Labels":["overseer"],"UpdatedAt":"2026-10-17T10:00:00Z"}`
	lines := `{"at":"2026-10-17T12:00:00Z","scanner":"issues",` + env + `,` + item + `,"decided":["fix run"]}
{"at":"2026-10-17T12:00:00Z","scanner":"issues",` + env + `,` + item + `}
{"at":"2026-10-17T12:00:00Z","scanner":"issues",` + env + `,` + item + `,"gate":"its sandbox is busy"}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runWatchRules(t, "replay", path)
	for _, sub := range []string{"2 decisions compared, 1 agree, 1 differ; 1 held back", "fix", "issue#7", "fix run"} {
		if !strings.Contains(out, sub) {
			t.Errorf("replay lacks %q:\n%s", sub, out)
		}
	}
	var res struct{ Compared, Gated int }
	if err := json.Unmarshal([]byte(runWatchRules(t, "replay", path, "-o", "json")), &res); err != nil || res.Compared != 2 || res.Gated != 1 {
		t.Errorf("json = %+v, %v", res, err)
	}
}
