package recipe

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

func TestBuiltinTriageParses(t *testing.T) {
	data, r, err := Builtin("triage")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || r.Name != "triage" || r.Context == "" {
		t.Fatalf("recipe = %+v", r)
	}
	last := r.Start.Steps[len(r.Start.Steps)-1]
	if last.Capture != "triage-output.yaml" || fmt.Sprint(r.Outputs) != "[triage-output.yaml]" {
		t.Errorf("captures %q, outputs %v; want triage-output.yaml as both (what the CLI reads)", last.Capture, r.Outputs)
	}
}

func TestParseRejects(t *testing.T) {
	for name, y := range map[string]string{
		"no name":          "start: {steps: [{run: x}]}",
		"no steps":         "name: a",
		"two kinds":        "name: a\nstart: {steps: [{run: x, ask: y}]}",
		"no kind":          "name: a\nstart: {steps: [{id: x}]}",
		"unknown uses":     "name: a\nstart: {steps: [{uses: curl-the-token}]}",
		"with on run":      "name: a\nstart: {steps: [{run: x, with: {a: b}}]}",
		"capture on run":   "name: a\nstart: {steps: [{run: x, capture: out.txt}]}",
		"capture path":     "name: a\nstart: {steps: [{ask: x, capture: ../out.txt}]}",
		"capture dotfile":  "name: a\nstart: {steps: [{ask: x, capture: .bashrc}]}",
		"duplicate id":     "name: a\nstart: {steps: [{id: a, run: x}, {id: a, run: y}]}",
		"bad id":           "name: a\nstart: {steps: [{id: A-1, run: x}]}",
		"unknown field":    "name: a\nstart: {steps: [{run: x, shell: zsh}]}",
		"continue on uses": "name: a\nstart: {steps: [{uses: setup-git, continue-on-error: true}]}",
		"bad input name":   "name: a\ninputs: {Focus: {}}\nstart: {steps: [{run: x}]}",
		"output path":      "name: a\noutputs: [../x]\nstart: {steps: [{run: x}]}",
		"output dotfile":   "name: a\noutputs: [.env]\nstart: {steps: [{run: x}]}",
		"required default": "name: a\ninputs: {focus: {required: true, default: x}}\nstart: {steps: [{run: x}]}",
		"bad task type":    "name: a\ntask-type: Plan_1\nstart: {steps: [{run: x}]}",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

func TestResolveInputs(t *testing.T) {
	r, err := Parse([]byte(`
name: a
inputs:
  focus: {default: all}
  target: {required: true}
start: {steps: [{run: x}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	std := map[string]string{"issue_number": "7"}

	got, err := r.ResolveInputs(std, map[string]string{"target": "t", "issue_number": "8"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"issue_number": "8", "focus": "all", "target": "t"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("inputs = %v, want %v", got, want)
	}
	if _, err := r.ResolveInputs(std, nil); err == nil || !strings.Contains(err.Error(), "target") {
		t.Errorf("missing required input: err = %v", err)
	}
	if _, err := r.ResolveInputs(std, map[string]string{"target": "t", "fcous": "x"}); err == nil || !strings.Contains(err.Error(), "fcous") {
		t.Errorf("misspelt input: err = %v", err)
	}
}

// TestBuiltinTriageRendersFromIssueInputs: `recipe triage`
// gives it only what an issue URL sets, and every ask must still render.
func TestBuiltinTriageRendersFromIssueInputs(t *testing.T) {
	_, r, err := Builtin("triage")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := r.ResolveInputs(map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u",
		"issue_url": "u", "issue_number": "7", "issue_title": "t", "issue_body": "b",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := templateData{Inputs: inputs, Steps: map[string]*StepResult{}}
	for i, s := range r.Start.Steps {
		if s.Ask == "" {
			continue
		}
		if _, err := render(s.Label(i), s.Ask, data, t.TempDir()); err != nil {
			t.Errorf("step %s: %v", s.Label(i), err)
		}
	}
	if got := r.OutputFiles(); fmt.Sprint(got) != "[triage-output.yaml]" {
		t.Errorf("outputs = %v", got)
	}
}

func TestOutputs(t *testing.T) {
	r, err := Parse([]byte("name: a\noutputs: [diff.txt, reply.md]\nstart: {steps: [{ask: x, capture: reply.md}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.OutputFiles(); fmt.Sprint(got) != "[diff.txt reply.md]" {
		t.Errorf("outputs = %v", got)
	}
}

// fakeExec records the steps it ran. A run step's exit code is the number
// after "exit " in its script, so a test can fail one.
type fakeExec struct {
	ran []string
}

func (f *fakeExec) Uses(_ context.Context, name string, with map[string]string, log io.Writer) error {
	f.ran = append(f.ran, "uses:"+name)
	fmt.Fprintf(log, "ran %s\n", name)
	if name == "setup-repo" && with["fail"] == "yes" {
		return fmt.Errorf("boom")
	}
	return nil
}

func (f *fakeExec) Run(_ context.Context, script string, _ map[string]string, log io.Writer) (int, error) {
	f.ran = append(f.ran, "run:"+script)
	code := 0
	if n, err := fmt.Sscanf(strings.TrimPrefix(script, "exit "), "%d", &code); n == 0 || err != nil {
		code = 0
	}
	return code, nil
}

type fakeSession struct {
	prompts []string
	closed  bool
}

func (s *fakeSession) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return fmt.Sprintf("reply %d", len(s.prompts)), nil
}

func (s *fakeSession) Close() error { s.closed = true; return nil }

func newTestRunner(t *testing.T) (*Runner, *fakeExec, *fakeSession, *int) {
	t.Helper()
	ex, sess, starts := &fakeExec{}, &fakeSession{}, 0
	return &Runner{
		Exec: ex,
		StartSession: func(context.Context) (Session, error) {
			starts++
			return sess, nil
		},
		TaskDir: t.TempDir(),
		Inputs:  map[string]string{"issue": "#7"},
		Log:     io.Discard,
	}, ex, sess, &starts
}

func TestRunnerRunsStepsAroundOneSession(t *testing.T) {
	r, ex, sess, starts := newTestRunner(t)
	rec, err := Parse([]byte(`
name: demo
context: RULES
start:
  steps:
    - uses: setup-git
    - id: tests
      run: exit 3
      continue-on-error: true
    - ask: "fix {{ .Inputs.issue }}"
    - ask: "tests exited {{ .Steps.tests.ExitCode }}; {{ file \"note.txt\" }}"
      capture: out.txt
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.TaskDir, "note.txt"), []byte("see the log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ex.ran, ","); got != "uses:setup-git,run:exit 3" {
		t.Errorf("ran %s", got)
	}
	if *starts != 1 || !sess.closed {
		t.Errorf("session started %d times, closed %v; want once, closed", *starts, sess.closed)
	}
	if len(sess.prompts) != 2 {
		t.Fatalf("prompts = %q", sess.prompts)
	}
	if sess.prompts[0] != "RULES\n\n---\n\nfix #7" {
		t.Errorf("first prompt = %q, want the context ahead of it", sess.prompts[0])
	}
	if sess.prompts[1] != "tests exited 3; see the log" {
		t.Errorf("second prompt = %q, want no context and the step's result", sess.prompts[1])
	}
	if b, _ := os.ReadFile(filepath.Join(r.TaskDir, "out.txt")); string(b) != "reply 2" {
		t.Errorf("out.txt = %q", b)
	}
}

func TestRunnerStopsAtTheFirstFailure(t *testing.T) {
	for name, y := range map[string]string{
		"uses":     "name: a\nstart: {steps: [{uses: setup-repo, with: {fail: \"yes\"}}, {ask: never}]}",
		"run":      "name: a\nstart: {steps: [{run: exit 1}, {ask: never}]}",
		"template": "name: a\nstart: {steps: [{ask: \"{{ .Inputs.missing }}\"}, {run: never}]}",
	} {
		r, ex, sess, starts := newTestRunner(t)
		rec, err := Parse([]byte(y))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := r.Run(context.Background(), rec); err == nil {
			t.Errorf("%s: ran to the end, want a failure", name)
		}
		if len(sess.prompts) != 0 || *starts != 0 {
			t.Errorf("%s: the agent was asked %q (%d starts), want no engine at all", name, sess.prompts, *starts)
		}
		for _, s := range ex.ran {
			if s == "run:never" {
				t.Errorf("%s: a step after the failure ran", name)
			}
		}
	}
}

func TestSandboxRunStripsTokensAndPassesInputsAsEnv(t *testing.T) {
	repo := t.TempDir()
	e := &SandboxExecutor{TaskDir: t.TempDir(), RepoDir: repo, Env: []string{
		"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=secret", "GH_TOKEN=secret", "KEEP=1",
	}}
	var out strings.Builder
	// An input that would be a command if it were pasted into the script.
	code, err := e.Run(context.Background(),
		`echo "tok=${GITHUB_TOKEN:-none}/${GH_TOKEN:-none} keep=$KEEP title=$INPUT_ISSUE_TITLE pwd=$(pwd)"; exit 4`,
		map[string]string{"issue-title": "$(touch pwned)"}, &out)
	if err != nil || code != 4 {
		t.Fatalf("code %d, err %v", code, err)
	}
	want := "tok=none/none keep=1 title=$(touch pwned) pwd=" + repo
	if resolved, _ := filepath.EvalSymlinks(repo); resolved != repo {
		want = strings.Replace(want, repo, resolved, 1)
	}
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(repo, "pwned")); err == nil {
		t.Error("an input ran as a command")
	}
}

func TestSandboxUsesCallsTheNamedFunctionWithTheToken(t *testing.T) {
	root := t.TempDir()
	taskDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(taskDir, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &SandboxExecutor{
		StepScript: []byte(`setupGit() { echo "setupGit token=$GITHUB_TOKEN branch=$WITH_BRANCH_NAME"; }
"$RECIPE_STEP_FUNCTION"`),
		TaskDir: taskDir,
		RepoDir: filepath.Join(root, "repo"),
		Env:     []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=tok"},
	}
	var out strings.Builder
	if err := e.Uses(context.Background(), "setup-git", map[string]string{"branch-name": "main"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "setupGit token=tok branch=main" {
		t.Errorf("got %q", got)
	}
}

// fakeACPAgent answers each prompt with a tool call followed by text, and
// numbers the turns, so a test can see that two asks share one session and
// that the reply is what came after the tool call. It reports its argv and
// whether a GitHub token reached it.
const fakeACPAgent = `
import json, os, sys
turn = 0
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n"); sys.stdout.flush()
def update(u):
    send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s", "update": u}})
for line in sys.stdin:
    msg = json.loads(line)
    method, mid = msg.get("method"), msg.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "authMethods": []}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s"}})
    elif method == "session/prompt":
        turn += 1
        text = msg["params"]["prompt"][0]["text"]
        update({"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "let me look. "}})
        update({"sessionUpdate": "tool_call", "toolCallId": "t", "title": "ls", "status": "completed"})
        update({"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "turn %d: %s " % (turn, text)}})
        update({"sessionUpdate": "agent_message_chunk", "content": {"type": "text",
                "text": "argv=%s gh=%s" % (" ".join(sys.argv[1:]), os.environ.get("GITHUB_TOKEN", "none"))}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn" if text != "refuse" else "refusal"}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": method}})
`

func TestACPSessionAsksTurnByTurn(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(fakeACPAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	acpd.Engines["recipe-fake"] = acpd.Engine{Command: "python3", Args: []string{script}, APIKeyEnv: "FAKE_KEY", ModelFlag: "--model"}
	t.Cleanup(func() { delete(acpd.Engines, "recipe-fake") })
	t.Setenv("GITHUB_TOKEN", "must-not-reach-the-engine")

	taskDir := t.TempDir()
	s, err := StartACPSession(context.Background(), "recipe-fake", "m-1", "k", t.TempDir(), taskDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i, want := range []string{
		"turn 1: hello argv=--model m-1 gh=none",
		"turn 2: again argv=--model m-1 gh=none",
	} {
		got, err := s.Ask(context.Background(), []string{"hello", "again"}[i])
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("ask %d = %q\nwant      %q", i+1, got, want)
		}
	}
	if _, err := s.Ask(context.Background(), "refuse"); err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Errorf("a refused turn returned %v, want its stop reason", err)
	}
	if _, err := os.Stat(filepath.Join(taskDir, "session", "stream.ndjson")); err != nil {
		t.Errorf("no transcript: %v", err)
	}
}

// An input from a kind is what a run: action on that kind's result
// fills: fix takes a Plan as its plan, and a run of a recipe that takes
// no input from the result's kind is refused.
func TestInputFrom(t *testing.T) {
	_, fix, err := Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	if in, ok := fix.InputFrom("Plan"); !ok || in != "plan" {
		t.Errorf("fix's input from a Plan = %q %v, want plan", in, ok)
	}
	if _, ok := fix.InputFrom("Triage"); ok {
		t.Error("fix takes a Triage")
	}
	for name, y := range map[string]string{
		"unknown kind":      "inputs: {p: {from: Nope}}\n",
		"required":          "inputs: {p: {from: Plan, required: true}}\n",
		"a revise input":    "inputs: {p: {from: Plan, revise: true}}\n",
		"typed":             "inputs: {p: {from: Plan, type: instructions}}\n",
		"two from one kind": "inputs: {p: {from: Plan}, q: {from: Plan}}\n",
		"run takes nothing": "task-output: {kind: Summary, from: s.md, actions: [{verb: run, run: fix}]}\n",
	} {
		if _, err := Parse([]byte("name: a\non: [issue]\nstart: {steps: [{ask: x}]}\n" + y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte("name: a\non: [issue]\nstart: {steps: [{ask: x}]}\ninputs: {p: {from: Plan}}\ntask-output: {kind: Plan, from: s.md, actions: [{verb: run, run: a}]}\n")); err != nil {
		t.Errorf("a recipe that follows itself: %v", err)
	}
}

func TestForSandboxDropsFactoryFields(t *testing.T) {
	data, rec, err := Builtin("triage")
	if err != nil {
		t.Fatal(err)
	}
	if rec.TaskOutput == nil || rec.TaskOutput.Kind != "Triage" || rec.TaskOutput.From != "triage-output.yaml" {
		t.Fatalf("triage task-output = %+v", rec.TaskOutput)
	}
	if rec.Inputs["instructions"].Type != InstructionsType {
		t.Fatalf("triage instructions input = %+v", rec.Inputs["instructions"])
	}
	out, err := ForSandbox(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\ntask-output:") {
		t.Errorf("task-output still in the sandbox's recipe:\n%s", out)
	}
	// What a runner without task-output reads: everything else, strictly.
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.TaskOutput != nil || len(got.Start.Steps) != len(rec.Start.Steps) || got.Context != rec.Context {
		t.Errorf("sandbox recipe differs beyond task-output: %+v", got)
	}
	if in := got.Inputs["instructions"]; in.Type != "" || in.Description != rec.Inputs["instructions"].Description {
		t.Errorf("sandbox recipe's instructions input = %+v, want it without its type", in)
	}

	// The fix's revise inputs lose their mark.
	data, rec, err = Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Inputs["instruction"].Revise || rec.Inputs["plan"].From != "Plan" {
		t.Fatalf("fix inputs = %+v", rec.Inputs)
	}
	if out, err = ForSandbox(data); err != nil {
		t.Fatal(err)
	}
	if got, err = Parse(out); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Inputs["instruction"].Revise || got.Inputs["plan"].From != "" || len(got.Revise) != len(rec.Revise) {
		t.Errorf("sandbox fix recipe = %+v", got)
	}

	// care's revises share the start's anchored step.
	if data, rec, err = Builtin("care"); err != nil {
		t.Fatal(err)
	}
	if out, err = ForSandbox(data); err != nil {
		t.Fatal(err)
	}
	if got, err = Parse(out); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Revise[1].Steps[0].Run == "" || got.Revise[1].Steps[0].Run != rec.Revise[0].Steps[0].Run {
		t.Errorf("sandbox care recipe lost its anchored step:\n%s", out)
	}
}

func TestInputTypeValidated(t *testing.T) {
	r := &Recipe{Name: "x", Start: Part{Steps: []Step{{Run: "true"}}}, Inputs: map[string]Input{"a": {Type: "number"}}}
	if err := r.Validate(); err == nil {
		t.Error("an unknown input type validated")
	}
	r.Inputs["a"] = Input{Type: InstructionsType}
	if err := r.Validate(); err != nil {
		t.Errorf("instructions type: %v", err)
	}
}

func TestTaskOutputValidated(t *testing.T) {
	for _, to := range []string{"{kind: Poem, from: x.yaml}", "{kind: Triage, from: ../x}",
		"{kind: Triage, from: x.yaml, preview: assessment}", "{kind: Triage, from: x.yaml, preview: spec.}"} {
		if _, err := Parse([]byte("name: x\nstart: {steps: [{run: 'true'}]}\ntask-output: " + to + "\n")); err == nil {
			t.Errorf("task-output %s accepted", to)
		}
	}
	if _, err := Parse([]byte("name: x\nstart: {steps: [{run: 'true'}]}\ntask-output: {kind: Triage, from: x.yaml, preview: spec.assessment}\n")); err != nil {
		t.Errorf("preview spec.assessment: %v", err)
	}
}

// Each built-in recipe whose draft is not markdown names the spec field
// a glance at it shows.
func TestBuiltinPreviews(t *testing.T) {
	for name, want := range map[string]string{"triage": "spec.assessment", "review": "spec.body", "care": "spec.report", "fix": "spec.body"} {
		_, r, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		if r.TaskOutput == nil || r.TaskOutput.Preview != want {
			t.Errorf("%s preview = %+v, want %s", name, r.TaskOutput, want)
		}
	}
}

// TestBuiltinPlanRenders: `recipe plan` from an issue URL alone, and as a
// revision, with an earlier plan in the task directory and feedback.
func TestBuiltinPlanRenders(t *testing.T) {
	data, r, err := Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskType != "plan" || r.TaskOutput == nil || r.TaskOutput.Kind != "Plan" || r.TaskOutput.From != "plan-output.md" {
		t.Fatalf("plan task-type %q, task-output %+v", r.TaskType, r.TaskOutput)
	}
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u",
		"issue_url": "u", "issue_number": "7", "issue_title": "t", "issue_body": "b",
	}
	askAll := func(overrides map[string]string, prior string) string {
		t.Helper()
		inputs, err := r.ResolveInputs(std, overrides)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "prior-plan.md"), []byte(prior), 0o644); err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for i, s := range r.Start.Steps {
			if s.Ask == "" {
				continue
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, dir)
			if err != nil {
				t.Fatalf("step %s: %v", s.Label(i), err)
			}
			all.WriteString(out)
		}
		return all.String()
	}
	fresh := askAll(nil, "")
	if strings.Contains(fresh, "<previous_plan>") || strings.Contains(fresh, "<feedback>") {
		t.Errorf("a fresh plan mentions a previous plan or feedback:\n%s", fresh)
	}
	revise := askAll(map[string]string{"feedback": "merge steps 2 and 3"}, "## Summary\nold plan\n")
	if !strings.Contains(revise, "<previous_plan>\n## Summary\nold plan") || !strings.Contains(revise, "<feedback>\nmerge steps 2 and 3") {
		t.Errorf("a revision lacks the previous plan or the feedback:\n%s", revise)
	}

	out, err := ForSandbox(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "task-type") {
		t.Errorf("task-type still in the sandbox's recipe:\n%s", out)
	}
}

// TestBuiltinReviewRenders: `recipe review` from a PR URL alone holds the
// token to the clone, and revises into its session.
func TestBuiltinReviewRenders(t *testing.T) {
	_, r, err := Builtin("review")
	if err != nil {
		t.Fatal(err)
	}
	if r.Credentials != CredentialsClone || r.TaskType != "" || r.TaskOutput == nil || r.TaskOutput.Kind != "Review" || r.TaskOutput.From != "review.yaml" {
		t.Fatalf("review credentials %q, task-type %q, task-output %+v", r.Credentials, r.TaskType, r.TaskOutput)
	}
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u",
		"pr_url": "u", "pr_number": "7", "pr_title": "t", "pr_body": "b", "pr_head": "h", "pr_base": "main",
	}
	inputs, err := r.ResolveInputs(std, map[string]string{"instructions": "be brief"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckRender(inputs); err != nil {
		t.Fatal(err)
	}
	if got := r.OutputDecl().Actions; got[len(got)-1].Verb != "revise" || got[len(got)-1].Revise != "review" {
		t.Errorf("actions = %v, want a revise review last", got)
	}
}

func TestTaskOutputActionsValidated(t *testing.T) {
	for _, acts := range []string{"[{verb: merge}]", "[{verb: label}]", "[{verb: run, run: deploy}]"} {
		if _, err := Parse([]byte("name: x\nstart: {steps: [{run: 'true'}]}\ntask-output: {kind: Plan, from: p.md, actions: " + acts + "}\n")); err == nil {
			t.Errorf("task-output actions %s accepted", acts)
		}
	}
}

// The built-in recipes declare what their results offer.
func TestBuiltinActions(t *testing.T) {
	for name, want := range map[string]string{"triage": "edit label comment reject", "plan": "edit comment run reject", "review": "edit post-review reject", "fix": "edit open-pr reject", "care": "edit post-replies reject"} {
		_, r, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		var verbs []string
		for _, a := range r.TaskOutput.Actions {
			verbs = append(verbs, a.Verb)
		}
		if got := strings.Join(verbs, " "); got != want {
			t.Errorf("%s actions = %s, want %s", name, got, want)
		}
	}
}

const reviseRecipe = `
name: demo
context: RULES
outputs: [plan.md]
start:
  steps:
    - ask: investigate
    - id: write
      ask: write it
      capture: plan.md
    - id: save
      run: &save cp plan.md /somewhere
revise:
  - id: plan
    label: Update plan
    steps:
      - ask: "rewrite it for {{ .Inputs.issue }}"
        capture: plan.md
      - run: *save
`

func TestARevisePartRunsItsOwnStepsWithoutTheContext(t *testing.T) {
	rec, err := Parse([]byte(reviseRecipe))
	if err != nil {
		t.Fatal(err)
	}
	r, ex, sess, _ := newTestRunner(t)
	r.Revise = "plan"
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sess.prompts, "|"); got != "rewrite it for #7" {
		t.Errorf("prompts = %q, want only the revise's ask, without the context the session already has", got)
	}
	if got := strings.Join(ex.ran, ","); got != "run:cp plan.md /somewhere" {
		t.Errorf("ran %s, want the anchored save step", got)
	}
	if b, _ := os.ReadFile(filepath.Join(r.TaskDir, "plan.md")); string(b) != "reply 1" {
		t.Errorf("plan.md = %q", b)
	}

	r, _, _, _ = newTestRunner(t)
	r.Revise = "nope"
	if err := r.Run(context.Background(), rec); err == nil || !strings.Contains(err.Error(), `no revise "nope"`) {
		t.Errorf("an unknown revise: %v", err)
	}
}

func TestRevisesAreValidated(t *testing.T) {
	start := "name: a\noutputs: [p.md]\nstart: {steps: [{ask: x, capture: p.md}]}\n"
	for name, y := range map[string]string{
		"top-level steps":  "name: a\nsteps: [{run: x}]",
		"bad id":           start + "revise: [{id: Plan, label: L, steps: [{ask: y, capture: p.md}]}]",
		"duplicate id":     start + "revise: [{id: p, label: L, steps: [{ask: y, capture: p.md}]}, {id: p, label: M, steps: [{ask: z, capture: p.md}]}]",
		"no label":         start + "revise: [{id: p, steps: [{ask: y, capture: p.md}]}]",
		"no steps":         start + "revise: [{id: p, label: L}]",
		"bad step":         start + "revise: [{id: p, label: L, steps: [{run: y, capture: p.md}]}]",
		"captures nothing": start + "revise: [{id: p, label: L, steps: [{ask: y}]}]",
		"captures another": start + "revise: [{id: p, label: L, steps: [{ask: y, capture: other.md}]}]",
		"declared revise action": start + "task-output: {kind: Plan, from: p.md, actions: [{verb: revise, revise: p}]}\n" +
			"revise: [{id: p, label: L, steps: [{ask: y, capture: p.md}]}]",
		"misses the task output": "name: a\noutputs: [p.md, q.md]\nstart: {steps: [{ask: x, capture: p.md}, {ask: x2, capture: q.md}]}\n" +
			"task-output: {kind: Plan, from: p.md}\nrevise: [{id: p, label: L, steps: [{ask: y, capture: q.md}]}]",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// A start may ask nothing: the first revise opens the session.
	if _, err := Parse([]byte("name: a\noutputs: [p.md]\nstart: {steps: [{run: x}]}\nrevise: [{id: p, label: L, steps: [{ask: y, capture: p.md}]}]")); err != nil {
		t.Errorf("a start that asks nothing: %v", err)
	}
	// Step ids are each part's own.
	if _, err := Parse([]byte(start + "revise: [{id: redo-labels, label: L, steps: [{id: x, ask: y, capture: p.md}, {id: x2, run: z}]}]")); err != nil {
		t.Errorf("a valid revise: %v", err)
	}
}

func TestForSandboxKeepsAnchoredSteps(t *testing.T) {
	data, err := ForSandbox([]byte(reviseRecipe + "task-output: {kind: Plan, from: plan.md}\n"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Parse(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	steps, _ := rec.Steps("plan")
	if rec.TaskOutput != nil || len(steps) != 2 || steps[1].Run != "cp plan.md /somewhere" {
		t.Errorf("revise steps = %+v\n%s", steps, data)
	}
}

// A task of a recipe with revises offers each, after the actions declared
// or, with none declared, the kind's.
func TestOutputDeclOffersEachRevise(t *testing.T) {
	base := reviseRecipe + "  - id: shorter\n    label: Shorter\n    steps: [{ask: shorter, capture: plan.md}]\n"
	rec, err := Parse([]byte(base + "task-output: {kind: Plan, from: plan.md, actions: [{verb: comment}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := rec.OutputDecl().Actions
	want := []taskoutput.Action{{Verb: "comment"}, {Verb: "revise", Revise: "plan", Label: "Update plan"}, {Verb: "revise", Revise: "shorter", Label: "Shorter"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actions = %+v\nwant      %+v", got, want)
	}
	if len(rec.TaskOutput.Actions) != 1 {
		t.Errorf("the recipe's declaration changed: %+v", rec.TaskOutput.Actions)
	}

	rec, err = Parse([]byte(base + "task-output: {kind: Plan, from: plan.md}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got = rec.OutputDecl().Actions
	if n := len(taskoutput.DefaultActions("Plan")); len(got) != n+2 || got[0].Verb != "edit" || got[n].Revise != "plan" {
		t.Errorf("actions = %+v, want Plan's defaults and the revises", got)
	}
	if err := taskoutput.ValidateActions("Plan", got); err != nil {
		t.Error(err)
	}
}

// The built-in plan's Update plan saves the plan as its start does, in
// the recipe as a sandbox gets it.
func TestPlanUseAsPlan(t *testing.T) {
	data, rec, err := Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	if a := rec.OutputDecl().Actions; a[len(a)-1] != (taskoutput.Action{Verb: "revise", Revise: "plan", Label: "Update plan"}) {
		t.Errorf("last action = %+v", a[len(a)-1])
	}
	data, err = ForSandbox(data)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = Parse(data); err != nil {
		t.Fatal(err)
	}
	steps, err := rec.Steps("plan")
	if err != nil {
		t.Fatal(err)
	}
	save := rec.Start.Steps[len(rec.Start.Steps)-1].Run
	if len(steps) != 2 || steps[0].Capture != "plan-output.md" || steps[1].Run != save || !strings.Contains(save, "plan-issue-") {
		t.Errorf("revise plan = %+v", steps)
	}
}

func TestCredentialsCloneValidated(t *testing.T) {
	const ok = "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: configure-engine}, {ask: x}]}"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for name, y := range map[string]string{
		"unknown":           "name: a\ncredentials: some\nstart: {steps: [{uses: clone}]}",
		"no clone":          "name: a\ncredentials: clone\nstart: {steps: [{ask: x}]}",
		"two clones":        "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: clone}, {ask: x}]}",
		"clone after ask":   "name: a\ncredentials: clone\nstart: {steps: [{ask: x}, {uses: clone}]}",
		"setup-git":         "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: setup-git}, {ask: x}]}",
		"checkout":          "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: checkout-default-branch}, {ask: x}]}",
		"setup-fork":        "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: setup-fork}, {ask: x}]}",
		"push":              "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {ask: x}, {uses: push}]}",
		"clone in a revise": "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {ask: x, capture: o.md}]}\nrevise: [{id: r, label: R, steps: [{uses: clone}, {ask: y, capture: o.md}]}]",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// Under credentials: clone no step gets the token from the environment,
// and the clone step alone gets the task's secrets, once.
func TestSandboxUsesGivesTheSecretsToTheCloneAlone(t *testing.T) {
	taskDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(taskDir, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &SandboxExecutor{
		StepScript: []byte(`echo "$RECIPE_STEP_FUNCTION token=${GITHUB_TOKEN:-none}/${GH_TOKEN:-none}"`),
		TaskDir:    taskDir,
		RepoDir:    filepath.Join(t.TempDir(), "repo"),
		Env:        []string{"PATH=" + os.Getenv("PATH"), "GH_TOKEN=leaked"},
		// Credentials and Secrets as runRecipeExec sets them.
		Credentials: CredentialsClone,
		Secrets:     []string{"GITHUB_TOKEN=tok"},
	}
	var out strings.Builder
	for _, step := range []string{"configure-engine", "clone", "clone"} {
		if err := e.Uses(context.Background(), step, nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	want := "configureGemini token=none/none\ncloneRepo token=tok/none\ncloneRepo token=none/none\n"
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
}

func TestBuiltinResearch(t *testing.T) {
	data, rec, err := Builtin("research")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Credentials != CredentialsClone || rec.TaskType != "research" || rec.StartOutputs() {
		t.Fatalf("research = credentials %q, task-type %q, start outputs %v", rec.Credentials, rec.TaskType, rec.StartOutputs())
	}
	if d := rec.OutputDecl(); d == nil || d.Kind != "Notes" || d.Actions[len(d.Actions)-1].Revise != "notes" {
		t.Fatalf("output decl = %+v", d)
	}
	// What a repository target sets, and the member's question.
	repo := map[string]string{"repo_owner": "o", "repo_name": "r", "url": "https://github.com/o/r", "repo_url": "https://github.com/o/r"}
	inputs, err := rec.ResolveInputs(repo, map[string]string{"topic": "How are deletes reconciled?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckRender(inputs); err != nil {
		t.Fatalf("CheckRender: %v", err)
	}
	if _, err := rec.ResolveInputs(repo, nil); err == nil {
		t.Error("resolved without a topic")
	}
	// The runner enforces credentials, so it reaches the sandbox.
	out, err := ForSandbox(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Parse(out); err != nil || got.Credentials != CredentialsClone {
		t.Errorf("sandbox recipe = %+v, %v; want credentials kept", got, err)
	}
	// An issue's recipe on a repository fails before anything runs.
	_, plan, err := Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := plan.ResolveInputs(repo, nil); err != nil {
		t.Fatal(err)
	} else if err := plan.CheckRender(inputs); err == nil || !strings.Contains(err.Error(), "issue_url") {
		t.Errorf("plan on a repository: CheckRender = %v, want issue_url missing", err)
	}
}

// TestBuiltinFixRenders: `recipe fix` from an issue URL alone, and with
// the approved plan; it ends with the push, and what it writes for GitHub
// follows --disclose.
func TestBuiltinFixRenders(t *testing.T) {
	_, r, err := Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskType != "fix" || r.Credentials != "" || r.TaskOutput == nil || r.TaskOutput.Kind != "Change" || r.TaskOutput.From != "change.yaml" || len(r.Revise) != 1 {
		t.Fatalf("fix task-type %q, credentials %q, task-output %+v, %d revises", r.TaskType, r.Credentials, r.TaskOutput, len(r.Revise))
	}
	var uses []string
	for _, s := range r.Start.Steps {
		if s.Uses != "" {
			uses = append(uses, s.Uses)
		}
	}
	if got := strings.Join(uses, " "); got != "setup-git setup-fork configure-engine checkout-default-branch push" {
		t.Errorf("uses = %s", got)
	}
	if last := r.Start.Steps[len(r.Start.Steps)-1]; last.Uses != "push" {
		t.Errorf("the last step is %s, not the push", last.Label(len(r.Start.Steps)-1))
	}
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u", "disclose": "true",
		"issue_url": "u", "issue_number": "7", "issue_title": "t", "issue_body": "b",
	}
	askAll := func(overrides map[string]string) string {
		t.Helper()
		inputs, err := r.ResolveInputs(std, overrides)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.CheckRender(inputs); err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for i, s := range r.Start.Steps {
			if s.Ask == "" {
				continue
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, t.TempDir())
			if err != nil {
				t.Fatalf("step %s: %v", s.Label(i), err)
			}
			all.WriteString(out)
		}
		return all.String()
	}
	plain := askAll(nil)
	if strings.Contains(plain, "<approved_plan>") || !strings.Contains(plain, "Fixes #7") || !strings.Contains(plain, "generated by an AI") {
		t.Errorf("a fix without a plan:\n%s", plain)
	}
	withPlan := askAll(map[string]string{"plan": "## Steps\n1. do it\n", "instructions": "keep it small"})
	if !strings.Contains(withPlan, "<approved_plan>\n## Steps\n1. do it") || !strings.Contains(withPlan, "keep it small") {
		t.Errorf("a fix with a plan lacks it or the instructions:\n%s", withPlan)
	}
	undisclosed := askAll(map[string]string{"disclose": "false"})
	if strings.Contains(undisclosed, "generated by an AI") || !strings.Contains(undisclosed, "Co-authored-by") {
		t.Errorf("an undisclosed fix:\n%s", undisclosed)
	}
	if !strings.Contains(r.Context, "Do NOT run `git push`") {
		t.Errorf("the context does not forbid pushing:\n%s", r.Context)
	}
}

// TestBuiltinFixRevises: the fix's one revise, iterate, starts from the
// push the fix last made, writes a Change, and ends with the push; its asks
// render with the start's inputs and say what they must.
func TestBuiltinFixRevises(t *testing.T) {
	_, r, err := Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, rv := range r.Revise {
		ids = append(ids, rv.ID)
		first, last := rv.Steps[0], rv.Steps[len(rv.Steps)-1]
		if !strings.Contains(first.Run, "INPUT_PUSHED_HEAD") || !strings.Contains(first.Run, "/lease") {
			t.Errorf("%s does not start from the push: %q", rv.ID, first.Run)
		}
		if last.Uses != "push" {
			t.Errorf("%s ends with %s, not the push", rv.ID, last.Label(len(rv.Steps)-1))
		}
		if c := rv.Steps[len(rv.Steps)-2]; c.Ask == "" || c.Capture != "change.yaml" {
			t.Errorf("%s does not capture change.yaml before the push", rv.ID)
		}
	}
	if got := strings.Join(ids, " "); got != "iterate" {
		t.Errorf("revises = %s", got)
	}
	acts := r.OutputDecl().Actions
	var verbs []string
	for _, a := range acts {
		verbs = append(verbs, strings.TrimSpace(a.Verb+" "+a.Revise))
	}
	if got := strings.Join(verbs, ","); got != "edit,open-pr,reject,revise iterate" {
		t.Errorf("actions = %s", got)
	}

	inputs, err := r.ResolveInputs(map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u", "disclose": "true",
		"issue_url": "u", "issue_number": "7", "issue_title": "t", "issue_body": "b",
	}, map[string]string{"instruction": "rename foo"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, v := range map[string]string{"pr": "12", "lease": "h1", "base": "b2"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	asks := map[string]string{}
	for _, rv := range r.Revise {
		var all strings.Builder
		for i, s := range rv.Steps {
			if s.Ask == "" {
				continue
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, dir)
			if err != nil {
				t.Fatalf("%s step %s: %v", rv.ID, s.Label(i), err)
			}
			all.WriteString(out)
		}
		asks[rv.ID] = all.String()
	}
	for id, want := range map[string][]string{
		"iterate": {"rename foo", "change: {}"},
	} {
		for _, w := range want {
			if !strings.Contains(asks[id], w) {
				t.Errorf("%s's asks lack %q:\n%s", id, w, asks[id])
			}
		}
	}
}

// The step a revise of fix, and care's start and revises, begin with points
// the push at the PR's branch, leased against the head last pushed, and
// checks that branch out; care's also remembers the PR.
func TestPushedStepPointsThePushAtTheBranch(t *testing.T) {
	_, fix, err := Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	_, care, err := Builtin("care")
	if err != nil {
		t.Fatal(err)
	}
	carePushed := care.Start.Steps[4].Run
	for _, rv := range care.Revise {
		if rv.Steps[0].Run != carePushed {
			t.Errorf("care's %s does not start from the push", rv.ID)
		}
	}
	for name, tc := range map[string]struct {
		script string
		want   map[string]string
	}{
		"fix iterate": {fix.Revise[0].Steps[0].Run, map[string]string{"branch": "issue-7-1\n", "base": "b0\n", "lease": "h1"}},
		"care":        {carePushed, map[string]string{"branch": "issue-7-1\n", "base": "b0\n", "lease": "h1", "pr": "12"}},
	} {
		repo, taskDir := t.TempDir(), t.TempDir()
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@e", "-c", "init.defaultBranch=main"}, args...)...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q")
		git("commit", "-q", "--allow-empty", "-m", "base")
		git("branch", "issue-7-1")
		run := func(env ...string) error {
			cmd := exec.Command("bash", "-eo", "pipefail", "-c", tc.script)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), append(env, "TASK_DIR="+taskDir)...)
			_, err := cmd.CombinedOutput()
			return err
		}
		if err := run("INPUT_PUSHED_BRANCH=", "INPUT_PUSHED_HEAD=", "INPUT_PR_URL="); err == nil {
			t.Errorf("%s: ran with nothing pushed", name)
		}
		if err := run("INPUT_PUSHED_BRANCH=issue-7-1", "INPUT_PUSHED_BASE=b0", "INPUT_PUSHED_HEAD=h1", "INPUT_PR_URL=https://github.com/o/r/pull/12"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for file, want := range tc.want {
			if got, _ := os.ReadFile(filepath.Join(taskDir, file)); string(got) != want {
				t.Errorf("%s: %s = %q, want %q", name, file, got, want)
			}
		}
		out, _ := exec.Command("git", "-C", repo, "symbolic-ref", "--short", "HEAD").Output()
		if strings.TrimSpace(string(out)) != "issue-7-1" {
			t.Errorf("%s: checked out %q", name, out)
		}
	}
}

// A uses step gets the task directory, where push finds its branch.
func TestSandboxUsesGetsTheTaskDir(t *testing.T) {
	taskDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(taskDir, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &SandboxExecutor{
		StepScript: []byte(`echo "$RECIPE_STEP_FUNCTION $TASK_DIR"`),
		TaskDir:    taskDir,
		RepoDir:    filepath.Join(t.TempDir(), "repo"),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
	}
	var out strings.Builder
	if err := e.Uses(context.Background(), "push", nil, &out); err != nil {
		t.Fatal(err)
	}
	if want := "pushToFork " + taskDir + "\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// on: and a revise's inputs are checked when parsed, and kept from a
// sandbox's runner, which may predate them.
func TestOnAndReviseInputs(t *testing.T) {
	withOn := "label: Draft it\non: [issue, my-pr]\ninputs:\n  instruction:\n    revise: true\n" + strings.Replace(reviseRecipe, "    label: Update plan\n", "    label: Update plan\n    inputs: [instruction]\n", 1)
	rec, err := Parse([]byte(withOn))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.On, []string{OnIssue, OnMyPR}) || !slices.Equal(rec.Revise[0].Inputs, []string{"instruction"}) || rec.DisplayLabel() != "Draft it" {
		t.Errorf("parsed = on %v, revise inputs %v, label %q", rec.On, rec.Revise[0].Inputs, rec.DisplayLabel())
	}
	if (&Recipe{Name: "fix"}).DisplayLabel() != "Fix" {
		t.Error("an unlabelled recipe is not shown under its name")
	}
	out, err := ForSandbox([]byte(withOn))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.On != nil || got.Label != "" || got.Revise[0].Inputs != nil || len(got.Revise[0].Steps) != 2 {
		t.Errorf("sandbox recipe kept factory's fields:\n%s", out)
	}

	for name, bad := range map[string]string{
		"unknown target":          "on: [commit]\n" + reviseRecipe,
		"undeclared revise input": strings.Replace(reviseRecipe, "    label: Update plan\n", "    label: Update plan\n    inputs: [instruction]\n", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// A recipe runs on its on: targets, a PR of yours on [pr] as well, and
// everywhere with none; setup: and start.on are gone, and refused.
func TestRunsOn(t *testing.T) {
	plain, err := Parse([]byte(reviseRecipe))
	if err != nil {
		t.Fatal(err)
	}
	if !plain.RunsOn(OnRepo) || !plain.RunsOn(OnMyPR) {
		t.Error("a recipe with no on: does not run everywhere")
	}
	for on, want := range map[string]map[string]bool{
		"[issue]": {OnIssue: true, OnPR: false, OnMyPR: false},
		"[pr]":    {OnIssue: false, OnPR: true, OnMyPR: true},
		"[my-pr]": {OnIssue: false, OnPR: false, OnMyPR: true},
	} {
		rec, err := Parse([]byte("on: " + on + "\n" + reviseRecipe))
		if err != nil {
			t.Fatal(err)
		}
		for target, w := range want {
			if rec.RunsOn(target) != w {
				t.Errorf("on %s: runs on %s = %v", on, target, !w)
			}
		}
	}
	for name, bad := range map[string]string{
		"setup":    strings.Replace(reviseRecipe, "start:\n  steps:", "setup:\n  steps:\n    - run: prepare\nstart:\n  steps:", 1),
		"start.on": strings.Replace(reviseRecipe, "start:\n  steps:", "start:\n  on: [issue]\n  steps:", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// A revise into a fresh session sends the context as a start would.
func TestAReviseIntoAFreshSessionSendsTheContext(t *testing.T) {
	rec, err := Parse([]byte(reviseRecipe))
	if err != nil {
		t.Fatal(err)
	}
	r, _, sess, _ := newTestRunner(t)
	r.Revise = "plan"
	r.StartSession = func(context.Context) (Session, error) { return freshSession{sess}, nil }
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if len(sess.prompts) != 1 || !strings.HasPrefix(sess.prompts[0], "RULES\n") {
		t.Errorf("prompts = %q, want the context first", sess.prompts)
	}
}

type freshSession struct{ *fakeSession }

func (freshSession) Fresh() bool { return true }

// The built-in care renders on a PR's inputs, whatever its focus; its
// revises render on the start's, and each ends with the push.
func TestBuiltinCareRenders(t *testing.T) {
	_, rec, err := Builtin("care")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.RunsOn(OnMyPR) || rec.RunsOn(OnPR) || rec.RunsOn(OnIssue) || rec.TaskOutput == nil || rec.TaskOutput.Kind != "Change" {
		t.Fatalf("care on %v, task-output %+v", rec.On, rec.TaskOutput)
	}
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "https://github.com/o/r/pull/9", "pr_url": "https://github.com/o/r/pull/9",
		"pr_number": "9", "pr_title": "t", "pr_body": "b", "pr_head": "feature", "pr_base": "main", "disclose": "true",
	}
	dir := t.TempDir()
	for name, v := range map[string]string{"pr": "9", "lease": "h1", "base": "b2", "upstream": "u3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	asks := func(steps []Step, inputs map[string]string) string {
		t.Helper()
		var all strings.Builder
		for i, s := range steps {
			if s.Ask == "" {
				continue
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, dir)
			if err != nil {
				t.Fatalf("step %s: %v", s.Label(i), err)
			}
			all.WriteString(out)
		}
		return all.String()
	}
	for focus, want := range map[string][]string{
		"":         {"git rebase u3", "gh pr checks 9", "pulls/9/comments", "inReplyTo", "written by an AI"},
		"rebase":   {"git rebase u3"},
		"ci":       {"gh pr checks 9"},
		"comments": {"pulls/9/comments"},
	} {
		inputs, err := rec.ResolveInputs(std, map[string]string{"focus": focus})
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.CheckRender(inputs); err != nil {
			t.Fatalf("focus %q: %v", focus, err)
		}
		got := asks(rec.Start.Steps, inputs)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("focus %q lacks %q:\n%s", focus, w, got)
			}
		}
		for other, job := range map[string]string{"rebase": "git rebase u3", "ci": "gh pr checks 9", "comments": "pulls/9/comments"} {
			if focus != "" && focus != other && strings.Contains(got, job) {
				t.Errorf("focus %q asks for %s too", focus, other)
			}
		}
	}
	if last := rec.Start.Steps[len(rec.Start.Steps)-1]; last.Uses != "push" {
		t.Errorf("the start ends with %s, not the push", last.Label(len(rec.Start.Steps)-1))
	}

	inputs, err := rec.ResolveInputs(std, map[string]string{"instruction": "rename foo"})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][]string{
		"address-comments": {"pulls/9/comments", "since you last pushed\n   (h1)", "inReplyTo"},
		"fix-ci":           {"gh pr checks 9", "--log-failed", "report:"},
		"rebase":           {"git rebase b2"},
		"iterate":          {"rename foo"},
	} {
		i := slices.IndexFunc(rec.Revise, func(rv Revise) bool { return rv.ID == id })
		if i < 0 {
			t.Errorf("care has no %s", id)
			continue
		}
		steps := rec.Revise[i].Steps
		if steps[len(steps)-1].Uses != "push" || steps[len(steps)-2].Capture != "change.yaml" {
			t.Errorf("%s does not capture change.yaml and push", id)
		}
		got := asks(steps, inputs)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", id, w, got)
			}
		}
	}
}

// TestBuiltinFanoutRenders: `recipe fanout` renders from an issue alone,
// and shows the agent the spec's template syntax as written.
func TestBuiltinFanoutRenders(t *testing.T) {
	_, r, err := Builtin("fanout")
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskOutput == nil || r.TaskOutput.Kind != "FanOut" || len(r.Revise) != 0 {
		t.Fatalf("fanout task-output %+v, revises %d", r.TaskOutput, len(r.Revise))
	}
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "u",
		"issue_url": "u", "issue_number": "7", "issue_title": "t", "issue_body": "b",
	}
	inputs, err := r.ResolveInputs(std, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckRender(inputs); err != nil {
		t.Fatal(err)
	}
	last := r.Start.Steps[len(r.Start.Steps)-1]
	ask, err := render("spec", last.Ask, templateData{Inputs: inputs}, "")
	if err != nil || !strings.Contains(ask, "write {{.item.name}} wherever") {
		t.Errorf("the spec ask (%v):\n%s", err, ask)
	}
}
