package dispatcher

import (
	"slices"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A task the engine decided runs its rule's recipe, apply and inputs; for
// every prebaked rule that runs a recipe, that is today's command
// exactly, so nothing changes when the runner starts reading them.
func TestDecidedTaskRunsTodaysCommand(t *testing.T) {
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
	}{
		{"comments", api.TypePRComments, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.NewTrustedFeedback = true })},
		{"conflict", api.TypePRIterate, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.Mergeable = &no })},
		{"ci", api.TypePRInvestigate, "https://github.com/o/r/pull/7", pr(func(it *rules.Item) { it.Checks = "failing" })},
		{"review", api.TypePRReview, "https://github.com/o/r/pull/7", pr(func(*rules.Item) {})},
		{"fix", api.TypeIssueFix, "https://github.com/o/r/issues/12", rules.Item{Kind: rules.OnIssue, Number: 12, Labels: []string{"overseer"}, UpdatedAt: now}},
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
			want, got := r.BuildArgs(plain, "ada-coder-bot"), r.BuildArgs(&decided, "ada-coder-bot")
			if !slices.Equal(want, got) {
				t.Errorf("command differs:\ntoday   %q\ndecided %q", want, got)
			}
			if decided.Phase != handlerPhase[tc.rule] {
				t.Errorf("phase = %d, want the handler's %d", decided.Phase, handlerPhase[tc.rule])
			}
		})
	}
}
