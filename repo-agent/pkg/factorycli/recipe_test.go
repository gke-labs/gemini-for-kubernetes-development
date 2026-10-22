package factorycli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
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
	return &Runner{Binary: bin, Prober: p, running: map[string]context.CancelFunc{}, results: map[string]Result{}}
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
		Sandbox: SandboxSize{EphemeralStorage: "20Gi", CPURequest: "2", MemoryLimit: "16Gi", StorageClass: "premium-rwo"},
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
		"--input a=b --input feedback=merge steps", "--instruction skip vendor/ ", "--engine claude", "--image img:1", "--workspace-disk-size 20Gi",
		"--workspace-storage-class premium-rwo --ephemeral-storage 20Gi --cpu-request 2 --memory-limit 16Gi", "--disclose=true"} {
		if !strings.HasPrefix(args[0], "recipe plan --run-name plan/b/5/1 ") || !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Count(args[0], "--instruction") != 1 || strings.Contains(args[0], "--detached") || strings.Contains(args[0], "--sandbox-key") {
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
	for _, want := range []string{"recipe research --run-name research/s1 ", "--sandbox-key s1", "--detached", "--input topic=how are deletes handled?", "--timeout 20m0s"} {
		if !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Contains(args[0], "--secret") || strings.Contains(args[0], "--engine") {
		t.Errorf("recipe args = %q: no secret, and no engine unless asked", args[0])
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

// A run for a new conversation says so; any other continues its session's.
// A size field left empty passes no flag, so factory's default (or its
// config file's) applies.
func TestRecipeArgsSandboxSizeOnlySetFields(t *testing.T) {
	args := strings.Join(recipeArgs(RecipeOptions{Recipe: "fix", Sandbox: SandboxSize{EphemeralStorage: "10Gi"}}, time.Minute), " ")
	if !strings.Contains(args, "--ephemeral-storage 10Gi") {
		t.Errorf("args = %q, want --ephemeral-storage 10Gi", args)
	}
	for _, flag := range []string{"--cpu-request", "--cpu-limit", "--memory-request", "--memory-limit", "--workspace-storage-class"} {
		if strings.Contains(args, flag) {
			t.Errorf("args = %q, has %s though it was not set", args, flag)
		}
	}
}

func TestRecipeArgsNewSession(t *testing.T) {
	opts := RecipeOptions{Recipe: "care-ci", URL: "https://github.com/o/repo/pull/9", Namespace: "alice", RunName: "r/1"}
	if args := strings.Join(recipeArgs(opts, time.Minute), " "); strings.Contains(args, "--new-session") {
		t.Errorf("args = %q, want no --new-session", args)
	}
	opts.NewSession = true
	if args := strings.Join(recipeArgs(opts, time.Minute), " "); !strings.Contains(args, " --new-session") {
		t.Errorf("args = %q, want --new-session", args)
	}
}

// The catalog carries what each recipe applies when a run ends and how
// long a run may take, as factory recipe list publishes them.
func TestRecipesCarryOnFinishAndTimeout(t *testing.T) {
	bin, _ := fakeFactory(t, `recipe) cat <<'JSON'
[{"name":"summarize","label":"Summarize"},
 {"name":"fix","label":"Fix","kind":"Change","onFinish":"open-pr","timeout":"3h"}]
JSON
;;`)
	recipes, _, err := newRunner(bin, &fakeProber{}).Recipes(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]boardv1alpha1.BoardRecipe{}
	for _, rec := range recipes {
		byName[rec.Name] = rec
	}
	if fix := byName["fix"]; fix.OnFinish != "open-pr" || fix.Timeout != "3h" {
		t.Errorf("fix = %+v, want onFinish open-pr and timeout 3h", fix)
	}
	if s := byName["summarize"]; s.OnFinish != "" || s.Timeout != "" {
		t.Errorf("summarize = %+v, want neither", s)
	}
}

// With a repository, the catalog is factory's list for it: the built-ins
// and the repository's recipes, a file that is not one left out and
// named; a repository recipe runs as recipe run --recipe, with no
// --instruction (recipe run has none).
func TestRecipesOfARepository(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) cat <<'JSON'
[{"name":"fix","label":"Fix","source":"builtin"},
 {"name":"explain-pr","label":"Explain","on":["pr"],"source":".agents/recipes/explain-pr.yaml"},
 {"name":"broken","source":".agents/recipes/broken.yaml","error":"recipe is named x, not broken as its file is"}]
JSON
;;`)
	recipes, skipped, err := newRunner(bin, &fakeProber{}).Recipes(context.Background(), "https://github.com/o/r", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if args := readArgs(t, argsLog); !strings.Contains(args[0], "recipe list -o json --url https://github.com/o/r") {
		t.Errorf("args = %q", args)
	}
	if len(recipes) != 2 || recipes[0].Name != "fix" || !recipes[0].IsBuiltin() || recipes[1].Name != "explain-pr" || recipes[1].IsBuiltin() {
		t.Errorf("recipes = %+v", recipes)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], ".agents/recipes/broken.yaml: recipe is named x") {
		t.Errorf("skipped = %v", skipped)
	}

	opts := RecipeOptions{Recipe: "explain-pr", RepoRecipe: true, URL: "https://github.com/o/r/pull/9", Namespace: "alice", RunName: "r/1", Instructions: []string{"be brief"}}
	args := strings.Join(recipeArgs(opts, time.Minute), " ")
	if !strings.HasPrefix(args, "recipe run --recipe explain-pr --run-name r/1") || strings.Contains(args, "--instruction") {
		t.Errorf("repository recipe args = %q", args)
	}
	opts.Recipe, opts.RepoRecipe = "review", false
	if args := strings.Join(recipeArgs(opts, time.Minute), " "); !strings.HasPrefix(args, "recipe review --run-name") || !strings.Contains(args, "--instruction be brief") {
		t.Errorf("built-in args = %q", args)
	}
}
