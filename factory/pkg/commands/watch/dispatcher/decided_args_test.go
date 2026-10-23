package dispatcher

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A task the engine decided runs its rule's recipe, apply and inputs - for
// every prebaked rule that runs a recipe, the command the dispatcher's
// recipe table ran before the rules named it - and a task no rule decided
// runs nothing.
func TestDecidedTaskRunsItsRulesRecipe(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	env := rules.Env{Trigger: "overseer", Prefixes: conventions.Prefixes("overseer"), Pool: []string{"ada-coder-bot"}, Now: now}
	yes, no := true, false
	pr := func(mod func(*rules.Item)) rules.Item {
		it := rules.Item{Kind: rules.OnPR, Number: 7, Author: "ada-coder-bot", Labels: []string{"overseer", "overseer/review"},
			Head: "aaaaaaaa", Mergeable: &yes, Checks: "green", UpdatedAt: now, LastTrustedActivity: now}
		mod(&it)
		return it
	}
	// The phase each handler sets its task to.
	handlerPhase := map[string]api.TaskPhase{"comments": api.PhaseComments, "conflict": api.PhaseRebase, "ci": api.PhaseInvestigate, "review": api.PhaseComments, "fix": api.PhaseInvestigate}
	for _, tc := range []struct {
		rule string
		typ  api.TaskType
		url  string
		it   rules.Item
		run  []string
	}{
		{"comments", api.TypePRRule, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.NewTrustedFeedback = true }),
			[]string{"care-comments", "comments", "--apply", "--action", "post-replies"}},
		{"conflict", api.TypePRRule, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.Mergeable = &no }),
			[]string{"care-rebase", "conflict", "--apply", "--action", "post-replies"}},
		{"ci", api.TypePRRule, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.Checks = "failing" }),
			[]string{"care-ci", "ci", "--apply", "--action", "post-replies"}},
		{"review", api.TypePRRule, "https://github.com/o/r/pull/7", pr(func(*rules.Item) {}),
			[]string{"review", "review", "--apply", "--action", "submit-review"}},
		{"fix", api.TypeIssueRule, "https://github.com/o/r/issues/12", rules.Item{Kind: rules.OnIssue, Number: 12, Labels: []string{"overseer"}, UpdatedAt: now},
			[]string{"fix", "fix", "--apply", "--action", "open-pr", "--pr-labels", "--input", "draft=false"}},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			var d rules.Decision
			for _, e := range set.Decide(tc.it, env) {
				if e.Rule == tc.rule {
					d = e
				}
			}
			if d.Outcome != rules.OutcomeRun {
				t.Fatalf("the rules do not run %s on the item: %+v", tc.rule, set.Decide(tc.it, env))
			}
			plain := &api.QueueTask{Type: tc.typ, Number: tc.it.Number, URL: tc.url, CommitSHA: "aaaaaaaa", EnqueuedAt: now}
			decided := *plain
			act.Task(&decided, d, set.Named(tc.rule))
			if decided.Recipe == "" || decided.Rule != tc.rule {
				t.Fatalf("the decision was not put on the task: %+v", decided)
			}
			r := testCLIRunner(t.TempDir())
			if args := r.BuildArgs(plain, "ada-coder-bot"); args != nil {
				t.Errorf("a task no rule decided runs %q", args)
			}
			runName := api.RuleRunName(&decided)
			want := append([]string{"recipe", tc.run[0], "--url", tc.url, "--run-name", runName}, tc.run[2:]...)
			if got := r.BuildArgs(&decided, "ada-coder-bot"); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
				t.Errorf("command = %q, want it to start %q", got, want)
			}
			if !strings.HasPrefix(runName, tc.run[1]+"-") {
				t.Errorf("run name %s, want it to start %s-", runName, tc.run[1])
			}
			if decided.Phase != handlerPhase[tc.rule] {
				t.Errorf("phase = %d, want the handler's %d", decided.Phase, handlerPhase[tc.rule])
			}
		})
	}
}
