package factorycli

import (
	"os"
	"strings"
	"testing"
	"time"
)

const triageTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Triage
target:
  url: https://github.com/o/repo/issues/5
source:
  sandbox: fix-repo-5
  task: recipe-triage-20261003-120000-0001
spec:
  labels:
    - bug
  assessment: A crash on start.
`

// The markdown underlines a heading with '='s, as the banner's closer is
// made of.
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

const changeTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Change
target:
  url: https://github.com/o/repo/issues/5
  commit: 0123abcd
spec:
  fork: alice/repo
  branch: issue-5-1
  title: "fix the nil case"
`

// fakeFactory is a factory binary that logs its arguments to a file and
// runs script, a shell case body over "$1".
func fakeFactory(t *testing.T, script string) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	bin, argsLog = dir+"/factory", dir+"/args"
	body := "#!/bin/sh\necho \"$@\" >> " + argsLog + "\ncase \"$1\" in\n" + script + "\nesac\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func readArgs(t *testing.T, argsLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// writes is a factory whose recipe writes doc, read back with progress on
// stderr, and whose apply proves it was handed doc's kind.
func writes(doc, kind string) string {
	return `recipe|revise) echo "Running recipe..." ;;
sandbox) echo "Waiting for sandbox pod fix-repo-5 to become ready..." >&2; cat <<'DOC'
` + doc + `DOC
;;
apply) grep -q "kind: ` + kind + `" "$3" && echo "Applied it" ;;
*) exit 9 ;;`
}

func newRunner(bin string, p TaskProber) *Runner {
	return &Runner{Binary: bin, Prober: p, running: map[string]struct{}{}, results: map[string]Result{}}
}

// A recipe runs by its name with its inputs, and its task output is read
// back by run name, whole, after the banner.
func TestStartRecipeRunsAndReadsBack(t *testing.T) {
	bin, argsLog := fakeFactory(t, writes(planTaskOutput, "Plan"))
	p := &fakeProber{probe: TaskProbe{State: ProbeRunning}}
	r := newRunner(bin, p)
	r.StartRecipe("alice/plan-repo-5", RecipeOptions{
		Recipe: "plan", Namespace: "alice", SandboxName: "fix-repo-5", URL: "https://github.com/o/repo/issues/5",
		RunName: "plan/b/5/1", Inputs: map[string]string{"feedback": "merge steps", "a": "b"},
		Instructions: []string{"skip vendor/", ""}, Engine: "claude", Image: "img:1", WorkspaceDiskSize: "20Gi", Disclose: true,
	})
	res := waitResult(t, r, "alice/plan-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if got := HarvestedOutput("Plan", res.Output); got != planTaskOutput || Draft("Plan", got) != planMarkdown {
		t.Errorf("task output = %q, want the plan whole", got)
	}
	args := readArgs(t, argsLog)
	if len(args) != 2 {
		t.Fatalf("commands run = %q, want recipe then sandbox task output", args)
	}
	for _, want := range []string{"--url https://github.com/o/repo/issues/5", "--namespace alice", "--timeout 30m0s", "--abort-on-cancel=false",
		"--input a=b --input feedback=merge steps", "--instruction skip vendor/ ", "--engine claude", "--image img:1", "--workspace-disk-size 20Gi", "--disclose=true"} {
		if !strings.HasPrefix(args[0], "recipe plan --run-name plan/b/5/1 ") || !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Count(args[0], "--instruction") != 1 || strings.Contains(args[0], "--detached") || strings.Contains(args[0], "--session") {
		t.Errorf("recipe args = %q: no empty instruction, not detached, no session", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output fix-repo-5 --namespace alice --run-name plan/b/5/1") {
		t.Errorf("output args = %q", args[1])
	}
	if p.taskType != "" {
		t.Errorf("probed %q: a recipe run is resumed by its run name", p.taskType)
	}
}

// With an action, the task output is applied in the member's namespace
// once read: a fix's open-pr finds the issue's sandbox there to alias to
// the PR it opens.
func TestStartRecipeAppliesItsAction(t *testing.T) {
	bin, argsLog := fakeFactory(t, writes(changeTaskOutput, "Change"))
	r := newRunner(bin, nil)
	r.StartRecipe("alice/fix-repo-5", RecipeOptions{
		Recipe: "fix", Namespace: "alice", URL: "https://github.com/o/repo/issues/5", RunName: "fix/b/5/1",
		Inputs: map[string]string{"with_plan": "true"}, Apply: "open-pr",
	})
	res := waitResult(t, r, "alice/fix-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if HarvestedOutput("Change", res.Output) != changeTaskOutput || !strings.Contains(res.Output, bannerCloser+"\nApplied it") {
		t.Errorf("output = %q, want the Change and what apply said", res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 {
		t.Fatalf("commands run = %q, want recipe, task output, apply", args)
	}
	if !strings.Contains(args[0], "--input with_plan=true") || !strings.Contains(args[0], "--disclose=false") {
		t.Errorf("recipe args = %q", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output https://github.com/o/repo/issues/5 ") {
		t.Errorf("output args = %q: without a sandbox, factory finds it by URL", args[1])
	}
	if !strings.HasPrefix(args[2], "apply -f ") || !strings.Contains(args[2], "--action open-pr") || !strings.Contains(args[2], "--namespace alice") {
		t.Errorf("apply args = %q", args[2])
	}
}

// An action that fails fails the run, with what apply said.
func TestStartRecipeApplyFailure(t *testing.T) {
	bin, _ := fakeFactory(t, strings.Replace(writes(changeTaskOutput, "Change"), `apply) grep -q "kind: Change" "$3" && echo "Applied it" ;;`,
		`apply) echo "Error: the fork has no branch issue-5-1"; exit 1 ;;`, 1))
	r := newRunner(bin, nil)
	r.StartRecipe("alice/fix-repo-5", RecipeOptions{Recipe: "fix", Namespace: "alice", URL: "u", RunName: "fix/b/5/1", Apply: "open-pr"})
	res := waitResult(t, r, "alice/fix-repo-5")
	if res.Err == nil || !strings.Contains(res.Output, "Error: the fork has no branch") || HarvestedOutput("Change", res.Output) == "" {
		t.Fatalf("want apply's failure after the Change, got %v\n%s", res.Err, res.Output)
	}
}

// A recipe that fails has no task output to read.
func TestStartRecipeFailure(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "sandbox fix-repo-5 is busy"; exit 1 ;;
*) exit 9 ;;`)
	r := newRunner(bin, nil)
	r.StartRecipe("alice/triage-repo-5", RecipeOptions{Recipe: "triage", Namespace: "alice", URL: "u", RunName: "r"})
	if res := waitResult(t, r, "alice/triage-repo-5"); res.Err == nil {
		t.Fatalf("want the recipe's failure, got %q", res.Output)
	}
	if args := readArgs(t, argsLog); len(args) != 1 {
		t.Errorf("commands run = %q, want only the recipe", args)
	}
}

// A detached run (research) returns once the sandbox has its task, in the
// sandbox its session names; there is nothing to read back yet. No
// --secret: mounting the member secret would put the engine key on the
// sandbox's disk.
func TestStartRecipeDetached(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "started" ;;
*) exit 9 ;;`)
	r := newRunner(bin, nil)
	r.StartRecipe("alice/rsch-s1", RecipeOptions{
		Recipe: "research", Namespace: "alice", URL: "https://github.com/o/repo", Session: "s1", RunName: ResearchRunName("s1"),
		Inputs: map[string]string{"topic": "how are deletes handled?"}, Detached: true, Timeout: 20 * time.Minute,
	})
	if res := waitResult(t, r, "alice/rsch-s1"); res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 1 {
		t.Fatalf("commands run = %q, want only the recipe", args)
	}
	for _, want := range []string{"recipe research --run-name research/s1 ", "--session s1", "--detached", "--input topic=how are deletes handled?", "--timeout 20m0s"} {
		if !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Contains(args[0], "--secret") || strings.Contains(args[0], "--engine") {
		t.Errorf("recipe args = %q: no secret, and no engine unless asked", args[0])
	}
}

// A revise runs in its run's sandbox and session with its inputs, is read
// back as a run is, and applies its action.
func TestStartRevise(t *testing.T) {
	bin, argsLog := fakeFactory(t, writes(changeTaskOutput, "Change"))
	r := newRunner(bin, nil)
	r.StartRevise("alice/revise-fix-repo-5", ReviseOptions{
		Namespace: "alice", SandboxName: "fix-repo-5", Revise: "iterate", Session: "recipe-fix-1",
		RunName: "revise/b/fix-repo-5/iterate/1", Apply: "post-replies",
		Inputs: map[string]string{"instruction": "rename it", "a": "b"},
	})
	res := waitResult(t, r, "alice/revise-fix-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if HarvestedOutput("Change", res.Output) != changeTaskOutput {
		t.Errorf("output = %q, want the Change", res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 || !strings.HasPrefix(args[0], "recipe revise fix-repo-5 iterate --run-name revise/b/fix-repo-5/iterate/1 ") {
		t.Fatalf("commands run = %q, want revise, task output, apply", args)
	}
	if !strings.Contains(args[0], "--task recipe-fix-1 --input a=b --input instruction=rename it") {
		t.Errorf("revise args = %q, want the session and the inputs, sorted", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output fix-repo-5 --namespace alice --run-name revise/b/fix-repo-5/iterate/1") {
		t.Errorf("output args = %q", args[1])
	}
	if !strings.Contains(args[2], "--action post-replies") || !strings.Contains(args[2], "--namespace alice") {
		t.Errorf("apply args = %q", args[2])
	}

	// Without an action, nothing is applied.
	bin, argsLog = fakeFactory(t, writes(planTaskOutput, "Plan"))
	r = newRunner(bin, nil)
	r.StartRevise("alice/revise-plan", ReviseOptions{Namespace: "alice", SandboxName: "fix-repo-5", Revise: "plan", RunName: "revise/b/5/plan/1"})
	if res := waitResult(t, r, "alice/revise-plan"); HarvestedOutput("Plan", res.Output) != planTaskOutput {
		t.Errorf("output = %q, want the plan", res.Output)
	}
	if args := readArgs(t, argsLog); len(args) != 2 {
		t.Errorf("commands run = %q, want revise then task output", args)
	}
}

// Any kind's task output is found after the banner, behind progress lines
// and without the runner's words after the closer; only one of the kind
// asked for, with a draft, is that kind's.
func TestHarvestedTaskOutput(t *testing.T) {
	for _, doc := range []string{changeTaskOutput, triageTaskOutput, planTaskOutput} {
		out := "Running recipe...\n" + taskOutputBanner + "\nWaiting for sandbox pod...\n" + doc + bannerCloser + "\nopened #9\n"
		if got := HarvestedTaskOutput(out); got != doc {
			t.Errorf("got %q, want %q", got, doc)
		}
	}
	for _, out := range []string{"", "posted\n", taskOutputBanner + "\njust words\n" + bannerCloser} {
		if got := HarvestedTaskOutput(out); got != "" {
			t.Errorf("%q: %q, want none", out, got)
		}
	}
	out := taskOutputBanner + "\n" + triageTaskOutput + bannerCloser + "\n"
	if HarvestedOutput("Plan", out) != "" || HarvestedOutput("Triage", out) != triageTaskOutput {
		t.Error("a Triage read as a plan, or not as a triage")
	}
	empty := strings.Replace(planTaskOutput, "spec:", "other:", 1)
	if HarvestedOutput("Plan", taskOutputBanner+"\n"+empty+bannerCloser) != "" {
		t.Error("a plan with no markdown has no draft")
	}
}

// A Triage task output, whole or behind lines that are not YAML, becomes
// the triage: block; any other draft is left as it is.
func TestNormalizeTriageDraft(t *testing.T) {
	block := "triage:\n  labels:\n    - bug\n  assessment: A crash on start."
	for name, tc := range map[string]struct{ in, want string }{
		"task output":     {triageTaskOutput, block},
		"behind progress": {"Waiting for sandbox pod fix-repo-5 to become ready...\nRunning inside Kubernetes cluster.\n" + triageTaskOutput, block},
		"triage block":    {block, block},
		"other kind":      {strings.Replace(triageTaskOutput, "kind: Triage", "kind: Plan", 1), strings.Replace(triageTaskOutput, "kind: Triage", "kind: Plan", 1)},
		"not yaml":        {"triage: [", "triage: ["},
	} {
		if got := NormalizeTriageDraft(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
