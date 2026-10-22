package dispatcher

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// A rule task runs its rule's recipe, under a run named after the rule: a
// built-in by its command, a repository's recipe by recipe run --recipe.
func TestRuleTaskArgs(t *testing.T) {
	r := testCLIRunner(t.TempDir())
	at := time.Unix(1700000000, 0)
	for recipe, want := range map[string][]string{
		"summarize":      {"recipe", "summarize", "--url"},
		"deploy-preview": {"recipe", "run", "--recipe", "deploy-preview", "--url"},
	} {
		task := &api.QueueTask{Type: api.TypePRRule, Number: 7, URL: "https://github.com/o/r/pull/7", Rule: "preview", Recipe: recipe, Apply: "comment", CommitSHA: "abcdef123", EnqueuedAt: at}
		got := r.BuildArgs(task, "ada-coder-bot")
		if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
			t.Errorf("%s: command %q, want it to start %q", recipe, got, want)
		}
		if !slices.Contains(got, "preview-7-abcdef1-1700000000") || !slices.Contains(got, "--action") {
			t.Errorf("%s: command %q lacks the rule's run name or its apply", recipe, got)
		}
	}
	if got := api.RuleTaskFile(false, 12, "triage-2"); got != "task-issue-12-rule-triage-2.yaml" {
		t.Errorf("issue rule task file = %s", got)
	}
	if !api.IsPRTask(api.TypePRRule) || api.IsPRTask(api.TypeIssueRule) {
		t.Error("IsPRTask is wrong for rule tasks")
	}
	if !strings.HasPrefix(api.RuleTaskFile(true, 7, "x"), "task-pr-7-") {
		t.Error("a PR rule task does not count as the PR's (task-pr-<n>-)")
	}
}
