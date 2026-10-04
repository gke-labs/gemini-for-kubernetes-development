package factorycli

import (
	"strings"
	"testing"
)

// The markdown underlines a heading with '='s, as the ISSUE PLAN closer
// is made of.
const planTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Plan
target:
  url: https://github.com/o/repo/issues/5
source:
  sandbox: fix-repo-5
  task: recipe-plan-20261003-120000-0001
  recipe: plan
spec:
  markdown: |-
    ## Summary
    Fix the crash.

    Notes
    ================================================
    Keep it small.
`

const planMarkdown = "## Summary\nFix the crash.\n\nNotes\n================================================\nKeep it small."

func startPlan(t *testing.T, bin string, opts PlanOptions) (Result, *fakeProber) {
	t.Helper()
	p := &fakeProber{probe: TaskProbe{State: ProbeNone}}
	r := &Runner{Binary: bin, Prober: p, running: map[string]struct{}{}, results: map[string]Result{}}
	opts.Namespace, opts.SandboxName, opts.IssueURL = "alice", "fix-repo-5", "https://github.com/o/repo/issues/5"
	r.StartPlan("alice/plan-repo-5", opts)
	return waitResult(t, r, "alice/plan-repo-5"), p
}

// The plan runs as a recipe and its result is read back by run name, as
// the markdown drafts are kept in.
func TestStartPlanRunsTheRecipe(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "Running recipe plan..." ;;
sandbox) echo "Waiting for sandbox pod fix-repo-5 to become ready..." >&2; cat <<'DOC'
`+planTaskOutput+`DOC
;;
*) exit 9 ;;`)
	res, p := startPlan(t, bin, PlanOptions{RunName: "plan/b/5/1", Feedback: "merge steps", Engine: "claude"})
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if got := ExtractPlan(res.Output); got != planMarkdown {
		t.Errorf("draft = %q, want %q", got, planMarkdown)
	}
	args := readArgs(t, argsLog)
	if len(args) != 2 {
		t.Fatalf("commands run = %q, want recipe then sandbox task output", args)
	}
	for _, want := range []string{"--abort-on-cancel=false", "--feedback merge steps", "--engine claude"} {
		if !strings.HasPrefix(args[0], "recipe plan --run-name plan/b/5/1 ") || !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if !strings.HasPrefix(args[1], "sandbox task output fix-repo-5 --namespace alice --run-name plan/b/5/1") {
		t.Errorf("output args = %q", args[1])
	}
	if p.taskType != "" {
		t.Errorf("probed %q: a plan is resumed by its run name", p.taskType)
	}
}

// A recipe that fails is a failed plan; there is no result to read.
func TestStartPlanRecipeFailure(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "sandbox fix-repo-5 is busy"; exit 1 ;;
*) exit 9 ;;`)
	if res, _ := startPlan(t, bin, PlanOptions{RunName: "plan/b/5/1"}); res.Err == nil {
		t.Fatalf("want the recipe's failure, got %q", res.Output)
	}
	if args := readArgs(t, argsLog); len(args) != 1 {
		t.Errorf("commands run = %q, want only the recipe", args)
	}
}

func TestExtractPlanOtherKind(t *testing.T) {
	if got := ExtractPlan(planBanner + "\n" + triageTaskOutput + "\n" + bannerCloser + "\n"); got != "" {
		t.Errorf("a Triage document read as a plan: %q", got)
	}
}
