package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

func TestWatchRulesFlow(t *testing.T) {
	out := runWatchRules(t, "--flow")
	for _, sub := range []string{
		"PR rules, walked top to bottom",
		"gates  stop              the PR has the stop label: no rule decides, its queued tasks dropped",
		"closed            the PR is closed",
		"merge-queue       the PR is in the merge queue: no rule decides\n",
		" 30 ● ci\n",
		"wait 2h after a success (a new head ends it; not when assignee: pool)",
		"after 3 runs since head, human, pause → the stop label",
		"run care-ci → post-replies",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("--flow lacks %q:\n%s", sub, out)
		}
	}
	if out := runWatchRules(t, "--flow", "issue"); !strings.Contains(out, "once per change to the item") || strings.Contains(out, "merge queue") {
		t.Errorf("--flow issue:\n%s", out)
	}
	cmd := newWatchRulesCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--flow", "comment"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "want pr or issue") {
		t.Errorf("--flow comment: %v", err)
	}
}

// explain --record walks the overseer's last record of the item.
func TestWatchRulesExplainRecord(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	m := true
	env := rules.Env{Trigger: "overseer", Prefixes: []string{"overseer"}, Pool: []string{"ada-coder-bot"}, Now: at}
	line := func(rec rules.Record) string {
		b, _ := json.Marshal(rec)
		return string(b)
	}
	item := rules.Item{Kind: rules.OnPR, Number: 7, Labels: []string{"overseer"}, Author: "ada-coder-bot", Head: "abc1234def", Mergeable: &m, Checks: "failing"}
	older := item
	older.Checks = "green"
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	data := strings.Join([]string{
		line(rules.Record{At: at.Add(time.Hour), Scanner: "prs", Env: env, Item: item}),
		line(rules.Record{At: at, Scanner: "prs", Env: env, Item: older}),
		line(rules.Record{At: at.Add(2 * time.Hour), Scanner: "issues", Env: env, Item: rules.Item{Kind: rules.OnIssue, Number: 7}}),
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runWatchRules(t, "explain", "https://github.com/o/r/pull/7", "--record", path)
	for _, sub := range []string{
		"PR o/r#7  (facts the overseer's record of 2026-10-19T13:00:00Z)",
		"checks failing",
		" 30 ● ci",
		"✓  run care-ci → post-replies",
		"skipped: ci took the item",
		"✗  mergeable: false (has mergeable true)",
		"decides ci run",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("explain lacks %q:\n%s", sub, out)
		}
	}
	var got struct {
		Record      rules.Record      `json:"record"`
		Explanation rules.Explanation `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(runWatchRules(t, "explain", "https://github.com/o/r/pull/7", "--record", path, "-o", "json")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Record.Item.Checks != "failing" || len(got.Explanation.Decisions()) != 1 {
		t.Errorf("json = %+v", got)
	}

	cmd := newWatchRulesCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"explain", "https://github.com/o/r/pull/8", "--record", path})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no record of pr #8") {
		t.Errorf("an item without a record: %v", err)
	}
}
