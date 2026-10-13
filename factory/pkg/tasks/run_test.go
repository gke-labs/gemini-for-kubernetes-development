package tasks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The run steps (lib.sh's runs section) are executed here as the
// recipes run them: runPrepare and runFinish each a bash process of its
// own, against a clone whose origin is a bare "fork" and whose upstream
// is a bare "repository", with the agent's turn in between played by a
// shell snippet.

// The step script is assembled by concatenating lib.sh, so a syntax error
// surfaces only in the sandbox unless something checks it here.
func TestRunScriptParses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	b, err := GetRecipeStepScript()
	if err != nil {
		t.Fatalf("GetRecipeStepScript: %v", err)
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(string(b))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the step script does not parse: %v\n%s", err, out)
	}
}

// runWorld is a sandbox's clone and the two remotes it talks to.
type runWorld struct {
	t                   *testing.T
	root, ws, fork, src string
	lib                 string
}

var gitIdentity = []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// git runs git in dir; a bare repository (the fork, the repository) by
// --git-dir, which works where safe.bareRepository is explicit.
func (w *runWorld) git(dir string, args ...string) string {
	w.t.Helper()
	at := []string{"-C", dir}
	if strings.HasSuffix(dir, ".git") {
		at = []string{"--git-dir", dir}
	}
	cmd := exec.Command("git", append(at, args...)...)
	cmd.Env = append(os.Environ(), gitIdentity...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (w *runWorld) write(dir, rel, content string, mode os.FileMode) {
	w.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		w.t.Fatal(err)
	}
}

// newRunWorld makes the repository (main, with a runbook under
// .agents/runbooks/gke, and pull request 7), an empty fork, and the
// sandbox's clone on main.
func newRunWorld(t *testing.T) *runWorld {
	t.Helper()
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w := &runWorld{t: t, root: root, ws: filepath.Join(root, "ws"), fork: filepath.Join(root, "fork.git"), src: filepath.Join(root, "src.git"), lib: string(lib)}
	seed := filepath.Join(root, "seed")
	w.git(root, "init", "-q", "-b", "main", seed)
	w.write(seed, "README.md", "code\n", 0644)
	w.write(seed, ".agents/runbooks/gke/runbook.md", "# GKE\n", 0644)
	w.write(seed, ".agents/runbooks/gke/deploy.sh", "#!/bin/sh\n", 0755)
	w.write(seed, ".agents/runbooks/gke/receipt-20260101-0000.md", "VERIFIED elsewhere\n", 0644)
	w.git(seed, "add", "-A")
	w.git(seed, "commit", "-qm", "main")
	w.git(seed, "checkout", "-qb", "pr7")
	w.write(seed, "README.md", "code, changed by the pull request\n", 0644)
	w.git(seed, "commit", "-qam", "pr")
	w.git(root, "clone", "-q", "--bare", seed, w.src)
	w.git(w.src, "update-ref", "refs/pull/7/head", "pr7")
	w.git(root, "init", "-q", "--bare", w.fork)
	w.git(root, "clone", "-q", "-b", "main", w.src, w.ws)
	w.git(w.ws, "remote", "rename", "origin", "upstream")
	w.git(w.ws, "remote", "add", "origin", w.fork)
	return w
}

// step runs one of the run functions in a process of its own, with the
// recipe's with: phase and inputs, and returns its exit code and output.
func (w *runWorld) step(fn, phase, run string, inputs map[string]string, extra string) (int, string) {
	w.t.Helper()
	task := filepath.Join(w.root, "task-"+run)
	if err := os.MkdirAll(task, 0755); err != nil {
		w.t.Fatal(err)
	}
	env := append(os.Environ(), gitIdentity...)
	env = append(env, "REPO_NAME=ws", "RUN_REPO="+w.ws, "TASK_DIR="+task, "GITHUB_USER_TOKEN=x", "WITH_PHASE="+phase, "INPUT_RUN="+run)
	for k, v := range inputs {
		env = append(env, "INPUT_"+strings.ToUpper(k)+"="+v)
	}
	cmd := exec.Command("bash", "-c", w.lib+"\n"+extra+"\n"+fn)
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// phase runs runPrepare, then agent in the run's worktree, then
// runFinish, and fails the test if either step fails.
func (w *runWorld) phase(phase, run string, inputs map[string]string, agent string) runResult {
	w.t.Helper()
	if code, out := w.step("runPrepare", phase, run, inputs, ""); code != 0 {
		w.t.Fatalf("runPrepare %s %s: exit %d\n%s", phase, run, code, out)
	}
	if agent != "" {
		cmd := exec.Command("bash", "-c", agent)
		cmd.Dir = w.ws
		if out, err := cmd.CombinedOutput(); err != nil {
			w.t.Fatalf("agent: %v\n%s", err, out)
		}
	}
	if code, out := w.step("runFinish", phase, run, inputs, ""); code != 0 {
		w.t.Fatalf("runFinish %s %s: exit %d\n%s", phase, run, code, out)
	}
	return w.result(run)
}

func (w *runWorld) result(run string) runResult {
	w.t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.root, "task-"+run, "result.json"))
	if err != nil {
		w.t.Fatal(err)
	}
	var res runResult
	if err := json.Unmarshal(raw, &res); err != nil {
		w.t.Fatalf("result.json: %v\n%s", err, raw)
	}
	return res
}

// A plan makes the run's branch from the empty runs/root, with only the
// run's files at its root, and pushes both to the fork; what the agent
// changed in the checkout is listed, not committed. The deploy after it
// starts from the fork's branch and adds to it.
func TestRunBranchPlanThenDeploy(t *testing.T) {
	w := newRunWorld(t)
	res := w.phase("plan", "r1", nil, `printf 'PLANNED — nothing executed\n' > .factory-run/receipt-20261013-0900.md
echo '# the procedure' > .factory-run/runbook.md
echo edited >> README.md`)
	if res.ExitCode != 0 || str(res.Verdict) != "PLANNED" || res.Branch != "runs/r1" || str(res.Receipt) != "receipt-20261013-0900.md" {
		t.Errorf("plan result = %+v", res)
	}
	if strings.Join(res.Discarded, ",") != "README.md" {
		t.Errorf("discarded = %v, want the checkout's README.md", res.Discarded)
	}
	if got := w.git(w.fork, "rev-parse", "runs/r1"); got != str(res.Commit) {
		t.Errorf("fork runs/r1 = %s, want the commit %s", got, str(res.Commit))
	}
	if files := w.git(w.fork, "ls-tree", "--name-only", "runs/r1"); files != "receipt-20261013-0900.md\nrunbook.md" {
		t.Errorf("runs/r1 holds %q, want the run's files alone", files)
	}
	rootSHA := w.git(w.fork, "rev-parse", "runs/root")
	if w.git(w.fork, "rev-list", "--max-parents=0", "runs/r1") != rootSHA || w.git(w.fork, "ls-tree", "runs/root") != "" {
		t.Error("runs/r1 does not start from the empty runs/root")
	}
	if w.git(w.ws, "diff", "--name-only") != "README.md" {
		t.Error("the checkout's change was not left where it was")
	}

	res = w.phase("deploy", "r1", nil, `test -f .factory-run/runbook.md
printf '**VERIFIED** — up\n' > .factory-run/receipt-20261013-1000.md`)
	if str(res.Verdict) != "VERIFIED" || str(res.Receipt) != "receipt-20261013-1000.md" {
		t.Errorf("deploy result = %+v", res)
	}
	if n := w.git(w.fork, "rev-list", "--count", "runs/r1"); n != "3" {
		t.Errorf("runs/r1 has %s commits, want root, plan, deploy", n)
	}
}

// runs/root is the same commit wherever it is made, so two sandboxes
// making it at once never conflict on the fork.
func TestRunsRootIsTheSameEverywhere(t *testing.T) {
	a, b := newRunWorld(t), newRunWorld(t)
	sha := func(w *runWorld) string {
		code, out := w.step("runsRoot", "plan", "r1", nil, "")
		if code != 0 {
			t.Fatalf("runsRoot: %s", out)
		}
		return strings.TrimSpace(out)
	}
	if x, y := sha(a), sha(b); x == "" || x != y {
		t.Errorf("runsRoot = %q and %q, want one commit", x, y)
	}
}

// A plan from a runbook copies it in, from the repository or from
// another run's branch — not its receipts, and not a target.env — and
// never over a run that has one, or from itself.
func TestRunBranchPlanFromARunbook(t *testing.T) {
	w := newRunWorld(t)
	head := w.git(w.src, "rev-parse", "refs/pull/7/head")
	w.phase("plan", "gke-a", map[string]string{"runbook": "gke"}, `test -x .factory-run/deploy.sh
test ! -e .factory-run/receipt-20260101-0000.md
printf 'TARGET_PR=7\nTARGET_SHA=`+head+`\n' > .factory-run/target.env
echo PLANNED > .factory-run/receipt-20261013-0900.md`)
	if msg := w.git(w.fork, "log", "-1", "--format=%s", "runs/gke-a"); !strings.Contains(msg, "plan from runbook .agents/runbooks/gke at") {
		t.Errorf("plan commit = %q", msg)
	}
	w.phase("plan", "gke-b", map[string]string{"runbook": "gke-a"}, `test -f .factory-run/runbook.md
test ! -e .factory-run/target.env
test ! -e .factory-run/receipt-20261013-0900.md`)
	if msg := w.git(w.fork, "log", "-1", "--format=%s", "runs/gke-b"); !strings.Contains(msg, "plan from runbook run gke-a (runs/gke-a)") {
		t.Errorf("plan commit = %q", msg)
	}
	for _, c := range []struct{ run, runbook, want string }{
		{"gke-a", "gke", "already has a runbook.md"},
		{"gke-c", "gke-c", "names this run itself"},
		{"gke-d", "missing", "no runbook named 'missing'"},
	} {
		if code, out := w.step("runPrepare", "plan", c.run, map[string]string{"runbook": c.runbook}, ""); code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("plan %s from %s: exit %d, want %q\n%s", c.run, c.runbook, code, c.want, out)
		}
	}
}

// A pull request run pins the pull request's head in target.env and
// checks the code out at it, detached; deploy reads the pin back and
// checks out the same commit, however far the pull request moves.
func TestRunBranchTarget(t *testing.T) {
	w := newRunWorld(t)
	head := w.git(w.src, "rev-parse", "refs/pull/7/head")
	res := w.phase("plan", "pr7", map[string]string{"target_pr": "7"}, `grep -q 'changed by the pull request' README.md
echo PLANNED > .factory-run/receipt-20261013-0900.md`)
	if res.Target == nil || res.Target.PR != 7 || res.Target.SHA != head {
		t.Errorf("target = %+v, want 7 at %s", res.Target, head)
	}
	if pin := w.git(w.fork, "show", "runs/pr7:target.env"); pin != "TARGET_PR=7\nTARGET_SHA="+head {
		t.Errorf("target.env = %q", pin)
	}
	w.git(w.ws, "checkout", "-q", "main")
	w.phase("deploy", "pr7", nil, `test "$(git rev-parse HEAD)" = `+head+`
echo VERIFIED > .factory-run/receipt-20261013-1000.md`)

	w.write(w.ws, ".factory-run/target.env", "TARGET_PR=7\nTARGET_SHA=nope\n", 0644)
	w.git(w.ws+"/.factory-run", "commit", "-qam", "bad pin")
	w.git(w.ws, "push", "-q", "origin", "runs/pr7")
	if code, out := w.step("runPrepare", "teardown", "pr7", nil, ""); code == 0 || !strings.Contains(out, "does not pin a pull request and a commit") {
		t.Errorf("a bad pin: exit %d\n%s", code, out)
	}
}

// Local-only, nothing is pushed; the branch lives in the sandbox and the
// next phase starts from it.
func TestRunBranchLocalOnly(t *testing.T) {
	w := newRunWorld(t)
	guard := `git -C "$RUN_REPO" remote set-url --push origin "$LOCAL_ONLY_PUSH_URL"`
	if code, out := w.step("true", "plan", "lo", nil, guard); code != 0 {
		t.Fatal(out)
	}
	res := w.phase("plan", "lo", nil, `echo PLANNED > .factory-run/receipt-20261013-0900.md`)
	if !res.LocalOnly || res.Commit == nil {
		t.Errorf("result = %+v, want local-only with a commit", res)
	}
	if refs := w.git(w.fork, "for-each-ref"); refs != "" {
		t.Errorf("the fork got %s", refs)
	}
	w.phase("deploy", "lo", nil, `test -f .factory-run/receipt-20261013-0900.md
echo VERIFIED > .factory-run/receipt-20261013-1000.md`)
	if n := w.git(w.ws, "rev-list", "--count", "runs/lo"); n != "3" {
		t.Errorf("runs/lo has %s commits", n)
	}
}

// A phase whose push failed keeps its commit in the sandbox, and the next
// phase pushes it; a branch the sandbox and the fork disagree on stops
// the phase rather than lose one of them.
func TestRunBranchUnpushedAndDiverged(t *testing.T) {
	w := newRunWorld(t)
	w.phase("plan", "r1", nil, `echo PLANNED > .factory-run/receipt-20261013-0900.md`)
	w.git(w.ws, "remote", "set-url", "--push", "origin", filepath.Join(w.root, "nowhere.git"))
	if code, _ := w.step("runPrepare", "deploy", "r1", nil, ""); code != 0 {
		t.Fatal("runPrepare failed")
	}
	w.write(w.ws, ".factory-run/receipt-20261013-1000.md", "VERIFIED\n", 0644)
	if code, out := w.step("runFinish", "deploy", "r1", nil, ""); code == 0 || !strings.Contains(out, "kept in this sandbox") {
		t.Fatalf("a failed push: exit %d\n%s", code, out)
	}
	w.git(w.ws, "remote", "set-url", "--push", "origin", w.fork)
	w.phase("teardown", "r1", nil, `echo TORN-DOWN > .factory-run/receipt-20261013-1100.md`)
	if files := w.git(w.fork, "ls-tree", "--name-only", "runs/r1"); !strings.Contains(files, "receipt-20261013-1000.md") {
		t.Errorf("the unpushed deploy never reached the fork: %s", files)
	}

	other := filepath.Join(w.root, "other")
	w.git(w.root, "clone", "-q", "-b", "runs/r1", w.fork, other)
	w.write(other, "note.md", "pushed from elsewhere\n", 0644)
	w.git(other, "add", "-A")
	w.git(other, "commit", "-qm", "elsewhere")
	w.git(other, "push", "-q", "origin", "runs/r1")
	w.git(w.ws+"/.factory-run", "commit", "-q", "--allow-empty", "-m", "here")
	if code, out := w.step("runPrepare", "plan", "r1", nil, ""); code == 0 || !strings.Contains(out, "have diverged") {
		t.Errorf("a diverged branch: exit %d\n%s", code, out)
	}
}

// A run that dies in the ask leaves runPrepare's result: the phase never
// finished.
func TestRunPrepareResultSaysUnfinished(t *testing.T) {
	w := newRunWorld(t)
	if code, out := w.step("runPrepare", "plan", "r1", nil, ""); code != 0 {
		t.Fatal(out)
	}
	if res := w.result("r1"); res.ExitCode != -1 || res.Verdict != nil || res.Commit != nil {
		t.Errorf("result = %+v, want exitCode -1 and nothing else", res)
	}
	for _, bad := range []string{"", "root", "Bad_Name", "a/b"} {
		if code, out := w.step("runPrepare", "plan", bad, nil, ""); code == 0 || !strings.Contains(out, "is not a run name") {
			t.Errorf("run %q: exit %d\n%s", bad, code, out)
		}
	}
}

// Runs push their branch to origin, so origin must be the member's fork
// before anything is pushed: runs-remote does that, before the checkout
// (the recipes' order: recipe.TestBuiltinRunRecipes), and run-prepare,
// a process of its own, reads from the checkout whether it made the run
// local-only rather than setting the fork up again.
func TestRunPrepareFindsTheRemoteRunsRemoteMade(t *testing.T) {
	b, err := GetRecipeStepScript()
	if err != nil {
		t.Fatalf("GetRecipeStepScript: %v", err)
	}
	prepare := funcBody(t, string(b), "runPrepare")
	if strings.Contains(prepare, "setupRunsRemote") || strings.Contains(prepare, "checkoutDefaultBranch") {
		t.Errorf("runPrepare does runs-remote's or checkout-default-branch's work again:\n%s", prepare)
	}
	if !strings.Contains(prepare, `= "${LOCAL_ONLY_PUSH_URL}" ]; then`) || !strings.Contains(prepare, "RUNS_LOCAL_ONLY=1") {
		t.Errorf("runPrepare does not read local-only from the push URL:\n%s", prepare)
	}
}

// run-finish runs in a process of its own after the ask: what
// run-prepare found reaches it through $TASK_DIR, newlines and quotes
// intact.
func TestRunStateCrossesTheAsk(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	b, err := GetRecipeStepScript()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	from := strings.Index(s, "RUN_STATE_VARS=")
	fns := s[from : from+strings.Index(s[from:], "# runPrepare is")]
	task := t.TempDir()
	script := "TASK_DIR=" + task + "\n" + fns + `
RUNS_LOCAL_ONLY=1; RUNBOOK_ORIGIN='run "gke" (runs/gke)'; TARGET_PR=42; TARGET_SHA=abc
RESULT_RECEIPTS_BEFORE=$'receipt-1.md\nreceipt-2.md'
saveRunState
unset RUNS_LOCAL_ONLY RUNBOOK_ORIGIN TARGET_PR TARGET_SHA RESULT_RECEIPTS_BEFORE
loadRunState
printf '%s|%s|%s|%s|%s' "$RUNS_LOCAL_ONLY" "$RUNBOOK_ORIGIN" "$TARGET_PR" "$TARGET_SHA" "$RESULT_RECEIPTS_BEFORE"
`
	out, err := exec.Command(bash, "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "1|run \"gke\" (runs/gke)|42|abc|receipt-1.md\nreceipt-2.md"; string(out) != want {
		t.Errorf("state after the ask = %q, want %q", out, want)
	}
}

// funcBody is the body of the shell function name in script.
func funcBody(t *testing.T, script, name string) string {
	t.Helper()
	at := strings.Index(script, "function "+name+" {")
	if at < 0 {
		t.Fatalf("no function %s", name)
	}
	return script[at : at+strings.Index(script[at:], "\n}\n")+3]
}

func str(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// runResult is result.json as a reader sees it.
type runResult struct {
	Version   int        `json:"version"`
	Run       string     `json:"run"`
	Mode      string     `json:"mode"`
	ExitCode  int        `json:"exitCode"`
	Verdict   *string    `json:"verdict"`
	Receipt   *string    `json:"receipt"`
	Branch    string     `json:"branch"`
	LocalOnly bool       `json:"localOnly"`
	Commit    *string    `json:"commit"`
	Target    *runTarget `json:"target"`
	Discarded []string   `json:"discarded"`
}

type runTarget struct {
	PR  int    `json:"pr"`
	SHA string `json:"sha"`
}
