package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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
	// The agent writes the triage to the file the CLI reads.
	last := r.Start.Steps[len(r.Start.Steps)-1]
	if !strings.Contains(last.Ask, `{{ output "triage-output.yaml" }}`) || last.Capture != "" || fmt.Sprint(r.Outputs) != "[triage-output.yaml]" {
		t.Errorf("last ask %q captures %q, outputs %v; want it to name triage-output.yaml as its output", last.Ask, last.Capture, r.Outputs)
	}
}

func TestParseRejects(t *testing.T) {
	for name, y := range map[string]string{
		"no name":                       "start: {steps: [{run: x}]}",
		"no steps":                      "name: a",
		"two kinds":                     "name: a\nstart: {steps: [{run: x, ask: y}]}",
		"no kind":                       "name: a\nstart: {steps: [{id: x}]}",
		"unknown uses":                  "name: a\nstart: {steps: [{uses: curl-the-token}]}",
		"with on run":                   "name: a\nstart: {steps: [{run: x, with: {a: b}}]}",
		"capture on run":                "name: a\nstart: {steps: [{run: x, capture: out.txt}]}",
		"capture path":                  "name: a\nstart: {steps: [{ask: x, capture: ../out.txt}]}",
		"capture dotfile":               "name: a\nstart: {steps: [{ask: x, capture: .bashrc}]}",
		"duplicate id":                  "name: a\nstart: {steps: [{id: a, run: x}, {id: a, run: y}]}",
		"bad id":                        "name: a\nstart: {steps: [{id: A-1, run: x}]}",
		"unknown field":                 "name: a\nstart: {steps: [{run: x, shell: zsh}]}",
		"continue on uses":              "name: a\nstart: {steps: [{uses: setup-git, continue-on-error: true}]}",
		"bad input name":                "name: a\ninputs: {Focus: {}}\nstart: {steps: [{run: x}]}",
		"output path":                   "name: a\noutputs: [../x]\nstart: {steps: [{run: x}]}",
		"output dotfile":                "name: a\noutputs: [.env]\nstart: {steps: [{run: x}]}",
		"required default":              "name: a\ninputs: {focus: {required: true, default: x}}\nstart: {steps: [{run: x}]}",
		"bad task type":                 "name: a\ntask-type: Plan_1\nstart: {steps: [{run: x}]}",
		"applicable-when empty":         "name: a\napplicable-when: {}\nstart: {steps: [{run: x}]}",
		"applicable-when bad template":  "name: a\napplicable-when: {label: \"{{ .Trigger\"}\nstart: {steps: [{run: x}]}",
		"applicable-when unknown field": "name: a\napplicable-when: {label: \"{{ .Repo }}/x\"}\nstart: {steps: [{run: x}]}",
		"bad timeout":                   "name: a\ntimeout: soon\nstart: {steps: [{run: x}]}",
		"zero timeout":                  "name: a\ntimeout: 0s\nstart: {steps: [{run: x}]}",
		"on-finish not offered":         "name: a\ntask-output: {kind: Change, from: c.yaml, actions: [{verb: open-pr}], on-finish: post-replies}\nstart: {steps: [{run: x}]}",
		"on-finish edit":                "name: a\ntask-output: {kind: Plan, from: p.md, on-finish: edit}\nstart: {steps: [{run: x}]}",
		"on-finish reject":              "name: a\ntask-output: {kind: Plan, from: p.md, on-finish: reject}\nstart: {steps: [{run: x}]}",
		"on-finish unknown":             "name: a\ntask-output: {kind: Plan, from: p.md, on-finish: deploy}\nstart: {steps: [{run: x}]}",
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

func (f *fakeExec) Uses(_ context.Context, name string, with, _ map[string]string, log io.Writer) error {
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
	// onAsk, when set, is what the agent does in a turn: write a file.
	onAsk func(n int, prompt string)
}

func (s *fakeSession) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if s.onAsk != nil {
		s.onAsk(len(s.prompts), prompt)
	}
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

// output names a result file by its absolute path in the task directory,
// for the agent to write; a name that is not a plain file name fails.
func TestRunnerOutputIsTheTaskDirPath(t *testing.T) {
	r, _, sess, _ := newTestRunner(t)
	rec, err := Parse([]byte("name: demo\nstart: {steps: [{ask: 'write it to {{ output \"plan.md\" }}'}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if want := "write it to " + filepath.Join(r.TaskDir, "plan.md"); len(sess.prompts) != 1 || sess.prompts[0] != want {
		t.Errorf("prompts = %q, want %q", sess.prompts, want)
	}
	bad, err := Parse([]byte("name: demo\nstart: {steps: [{ask: '{{ output \"../x\" }}'}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.CheckRender(nil); err == nil {
		t.Error("output ../x rendered, want a plain file name required")
	}
}

// An ask that expects a file goes on once the agent wrote it; an agent
// that forgot is asked once more, and the step fails if it still has not,
// before a later step acts on the result.
func TestRunnerExpectsTheFileTheAgentWrites(t *testing.T) {
	rec, err := Parse([]byte(`
name: demo
start:
  steps:
    - ask: 'write it to {{ output "change.yaml" }}'
      expects: change.yaml
    - run: push
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		writeAt int // the turn the agent writes the file in; 0: never
		turns   int
		fails   bool
	}{
		{"written", 1, 1, false},
		{"written when reminded", 2, 2, false},
		{"never written", 0, 2, true},
	} {
		r, ex, sess, _ := newTestRunner(t)
		sess.onAsk = func(n int, _ string) {
			if n == tc.writeAt {
				_ = os.WriteFile(filepath.Join(r.TaskDir, "change.yaml"), []byte("change: {}\n"), 0o644)
			}
		}
		err := r.Run(context.Background(), rec)
		if (err != nil) != tc.fails {
			t.Errorf("%s: Run = %v, want failure %v", tc.name, err, tc.fails)
		}
		if len(sess.prompts) != tc.turns {
			t.Errorf("%s: %d turns, want %d", tc.name, len(sess.prompts), tc.turns)
		}
		if pushed := len(ex.ran) > 0; pushed == tc.fails {
			t.Errorf("%s: push ran %v, want %v", tc.name, pushed, !tc.fails)
		}
	}
	for _, bad := range []string{
		"name: a\nstart: {steps: [{run: x, expects: f}]}",
		"name: a\nstart: {steps: [{ask: x, expects: ../f}]}",
		"name: a\nstart: {steps: [{ask: x, expects: f, capture: f}]}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
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

// A step's if: is rendered like an ask; false skips the step (recorded as
// skipped, so a later if: can read it), true runs it, anything else
// fails the recipe, and a skipped ask starts no engine.
func TestRunnerIf(t *testing.T) {
	r, ex, sess, starts := newTestRunner(t)
	rec, err := Parse([]byte(`
name: demo
start:
  steps:
    - id: a
      if: '{{ eq .Inputs.issue "#8" }}'
      run: exit 0
    - if: '{{ .Steps.a.Skipped }}'
      run: echo skipped-a
    - if: '{{ eq .Inputs.issue "#8" }}'
      ask: never
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ex.ran, ","); got != "run:echo skipped-a" {
		t.Errorf("ran %s", got)
	}
	if len(sess.prompts) != 0 || *starts != 0 {
		t.Errorf("asked %q (%d starts), want no engine", sess.prompts, *starts)
	}

	r, ex, _, _ = newTestRunner(t)
	rec, err = Parse([]byte("name: a\nstart: {steps: [{if: \"{{ .Inputs.issue }}\", run: x}, {run: never}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "neither true nor false") {
		t.Errorf("non-boolean if: Run = %v", err)
	}
	if len(ex.ran) != 0 {
		t.Errorf("ran %v after a bad if:", ex.ran)
	}
	if err := rec.CheckRender(map[string]string{"issue": "#7"}); err == nil {
		t.Error("CheckRender passed a non-boolean if:")
	}
	if _, err := Parse([]byte("name: a\nstart: {steps: [{if: \"{{ .Inputs.issue \", run: x}]}")); err == nil {
		t.Error("Parse accepted an if: that is not a template")
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

func TestSandboxUsesCallsTheNamedFunctionWithTheTokenAndInputs(t *testing.T) {
	root := t.TempDir()
	taskDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(taskDir, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &SandboxExecutor{
		StepScript: []byte(`setupGit() { echo "setupGit token=$GITHUB_TOKEN branch=$WITH_BRANCH_NAME pr=$INPUT_PR_NUMBER"; }
"$RECIPE_STEP_FUNCTION"`),
		TaskDir: taskDir,
		RepoDir: filepath.Join(root, "repo"),
		Env:     []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=tok"},
	}
	var out strings.Builder
	// The inputs too, as a run step gets them: pr-comments reads
	// INPUT_TRUSTED_LOGINS, issue-comments INPUT_ISSUE_NUMBER.
	if err := e.Uses(context.Background(), "setup-git", map[string]string{"branch-name": "main"}, map[string]string{"pr_number": "7"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "setupGit token=tok branch=main pr=7" {
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
        # gemini's account of the turn's tokens: _meta.quota.
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn" if text != "refuse" else "refusal",
              "_meta": {"quota": {"token_count": {"input_tokens": 100 * turn, "output_tokens": 10 * turn},
                                  "model_usage": [{"model": "m-1", "token_count": {"input_tokens": 100 * turn, "output_tokens": 10 * turn}}]}}}})
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
	s, err := StartACPSession(context.Background(), "recipe-fake", "m-1", "k", t.TempDir(), taskDir, nil)
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
		// The turn's tokens, as the engine's reply put them.
		n := int64(i + 1)
		if u := s.TurnUsage(); len(u) != 1 || u[0] != (acpd.ModelUsage{Model: "m-1", Input: 100 * n, Output: 10 * n, Total: 110 * n}) {
			t.Errorf("ask %d used %+v", i+1, u)
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
		"revise, gone":      "inputs: {p: {from: Plan, revise: true}}\n",
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

// on-finish names an apply action the result offers, its kind's defaults
// when it declares none; timeout is a positive duration.
func TestOnFinishAndTimeout(t *testing.T) {
	rec, err := Parse([]byte("name: a\ntimeout: 3h\ntask-output: {kind: Plan, from: p.md, on-finish: comment}\nstart: {steps: [{run: x}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Timeout != "3h" || rec.TaskOutput.OnFinish != "comment" {
		t.Errorf("timeout %q, on-finish %q", rec.Timeout, rec.TaskOutput.OnFinish)
	}
	out, err := ForSandbox([]byte("name: a\ntimeout: 3h\nstart: {steps: [{run: x}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "timeout") {
		t.Errorf("timeout still in the sandbox's recipe:\n%s", out)
	}
}

// What a board's runner applies when a run ends, and how long it waits.
func TestBuiltinsDeclareHowTheyFinish(t *testing.T) {
	for name, want := range map[string][2]string{
		"fix":           {"open-pr", "3h"},
		"review":        {"post-review", "45m"},
		"care":          {"post-replies", ""},
		"care-ci":       {"post-replies", ""},
		"care-comments": {"post-replies", ""},
		"care-rebase":   {"post-replies", ""},
		"care-iterate":  {"post-replies", ""},
		"plan":          {"", ""},
		"triage":        {"", ""},
	} {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := [2]string{rec.TaskOutput.OnFinish, rec.Timeout}; got != want {
			t.Errorf("%s: on-finish, timeout = %q, want %q", name, got, want)
		}
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

	// The fix's plan input loses its source.
	data, rec, err = Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Inputs["plan"].From != "Plan" {
		t.Fatalf("fix inputs = %+v", rec.Inputs)
	}
	if out, err = ForSandbox(data); err != nil {
		t.Fatal(err)
	}
	if got, err = Parse(out); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Inputs["plan"].From != "" || len(got.Start.Steps) != len(rec.Start.Steps) {
		t.Errorf("sandbox fix recipe = %+v", got)
	}

	// The session tag is factory's.
	if data, _, err = Builtin("care-ci"); err != nil {
		t.Fatal(err)
	}
	if out, err = ForSandbox(data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "session:") {
		t.Errorf("sandbox care-ci recipe kept its session:\n%s", out)
	}

	// So is where a board offers it.
	if data, rec, err = Builtin("fanout"); err != nil {
		t.Fatal(err)
	}
	if rec.ApplicableWhen == nil || rec.ApplicableWhen.Label != "{{ .Trigger }}/fanout" {
		t.Fatalf("fanout applicable-when = %+v, want it on fan-out parents", rec.ApplicableWhen)
	}
	if out, err = ForSandbox(data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "applicable-when:") {
		t.Errorf("sandbox fanout recipe kept its applicable-when:\n%s", out)
	}
}

// An applicable-when label is spelled once per trigger, without repeats.
func TestApplicableWhenLabels(t *testing.T) {
	aw := &ApplicableWhen{Label: "{{ .Trigger }}/fanout"}
	for _, c := range []struct {
		triggers []string
		want     []string
	}{
		{[]string{"overseer", "kcc"}, []string{"overseer/fanout", "kcc/fanout"}},
		{[]string{"overseer", "Overseer"}, []string{"overseer/fanout"}},
	} {
		if got, err := aw.Labels(c.triggers...); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("Labels(%v) = %v, %v, want %v", c.triggers, got, err, c.want)
		}
	}
	if got, _ := (&ApplicableWhen{Label: "help wanted"}).Labels("overseer", "kcc"); !slices.Equal(got, []string{"help wanted"}) {
		t.Errorf("a plain label = %v, want it alone", got)
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
// token to the clone.
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
	var verbs []string
	for _, a := range r.OutputDecl().Actions {
		verbs = append(verbs, a.Verb)
	}
	if got := strings.Join(verbs, " "); got != "edit post-review reject" {
		t.Errorf("actions = %s, want edit post-review reject", got)
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

const demoRecipe = `
name: demo
context: RULES
outputs: [plan.md]
start:
  steps:
    - ask: investigate
    - id: write
      ask: "write it for {{ .Inputs.issue }} to {{ output \"plan.md\" }}"
    - id: save
      run: &save cp plan.md /somewhere
    - run: *save
`

// revise: parts are gone: a result is a file the agent writes, again
// whenever it is asked to later in the conversation.
func TestRemovedFieldsAreRejected(t *testing.T) {
	start := "name: a\noutputs: [p.md]\nstart: {steps: [{ask: x}]}\n"
	for name, y := range map[string]string{
		"top-level steps": "name: a\nsteps: [{run: x}]",
		"revise":          start + "revise: [{id: p, label: L, steps: [{ask: y}]}]",
		"revise action":   start + "task-output: {kind: Plan, from: p.md, actions: [{verb: revise, revise: p}]}\n",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestForSandboxKeepsAnchoredSteps(t *testing.T) {
	data, err := ForSandbox([]byte(demoRecipe + "task-output: {kind: Plan, from: plan.md}\n"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Parse(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	steps := rec.Start.Steps
	if rec.TaskOutput != nil || len(steps) != 4 || steps[3].Run != "cp plan.md /somewhere" {
		t.Errorf("steps = %+v\n%s", steps, data)
	}
}

// A task's task output is the recipe's declaration, a copy of it.
func TestOutputDeclIsTheDeclaration(t *testing.T) {
	rec, err := Parse([]byte(demoRecipe + "task-output: {kind: Plan, from: plan.md, actions: [{verb: comment}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := rec.OutputDecl()
	if want := []taskoutput.Action{{Verb: "comment"}}; got.Kind != "Plan" || got.From != "plan.md" || !reflect.DeepEqual(got.Actions, want) {
		t.Errorf("decl = %+v", got)
	}
	if got == rec.TaskOutput {
		t.Error("OutputDecl is the recipe's own declaration, not a copy")
	}
}

// The built-in plan's plan is the file its agent writes, offered to fix,
// and linked where the next plan run revises it, in the recipe as a
// sandbox gets it.
func TestPlanUseAsPlan(t *testing.T) {
	data, rec, err := Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&taskoutput.Document{Kind: "Plan", Actions: rec.OutputDecl().Actions}).Offer("run", "fix"); err != nil {
		t.Error(err)
	}
	data, err = ForSandbox(data)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = Parse(data); err != nil {
		t.Fatal(err)
	}
	link := rec.Start.Steps[len(rec.Start.Steps)-1].Run
	if !strings.Contains(link, "plan-issue-") || !strings.Contains(link, "ln -sfn") {
		t.Errorf("last step = %q, want the plan linked at plan-issue-<n>.md", link)
	}
}

func TestCredentialsCloneValidated(t *testing.T) {
	const ok = "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: configure-engine}, {ask: x}]}"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for name, y := range map[string]string{
		"unknown":         "name: a\ncredentials: some\nstart: {steps: [{uses: clone}]}",
		"no clone":        "name: a\ncredentials: clone\nstart: {steps: [{ask: x}]}",
		"two clones":      "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: clone}, {ask: x}]}",
		"clone after ask": "name: a\ncredentials: clone\nstart: {steps: [{ask: x}, {uses: clone}]}",
		"setup-git":       "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: setup-git}, {ask: x}]}",
		"checkout":        "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: checkout-default-branch}, {ask: x}]}",
		"setup-fork":      "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {uses: setup-fork}, {ask: x}]}",
		"push":            "name: a\ncredentials: clone\nstart: {steps: [{uses: clone}, {ask: x}, {uses: push}]}",
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
		if err := e.Uses(context.Background(), step, nil, nil, &out); err != nil {
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
	// The member works as themselves: their credentials, a fork to push to.
	if credentialsOf(rec) != CredentialsFull || rec.TaskType != "research" {
		t.Fatalf("research = credentials %q, task-type %q", rec.Credentials, rec.TaskType)
	}
	if got := []string{rec.Start.Steps[0].Uses, rec.Start.Steps[1].Uses}; got[0] != "setup-git" || got[1] != "setup-repo" {
		t.Errorf("research starts with %v, want setup-git, setup-repo", got)
	}
	if strings.Contains(rec.Context, "must not change anything") || strings.Contains(rec.Context, "no GitHub credentials") {
		t.Errorf("research's context still forbids changes:\n%s", rec.Context)
	}
	// The notes are there once someone asks for them, in the conversation.
	if d := rec.OutputDecl(); d == nil || d.Kind != "Notes" || !d.Optional || d.From != "notes.md" {
		t.Fatalf("output decl = %+v", d)
	}
	if ask := rec.Start.Steps[len(rec.Start.Steps)-1].Ask; !strings.Contains(ask, `{{ output "notes.md" }}`) || !strings.Contains(ask, "save notes") {
		t.Errorf("research's ask does not say where saved notes go:\n%s", ask)
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
	if _, err := ForSandbox(data); err != nil {
		t.Fatal(err)
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
	if r.TaskType != "fix" || r.Credentials != "" || r.TaskOutput == nil || r.TaskOutput.Kind != "Change" || r.TaskOutput.From != "change.yaml" {
		t.Fatalf("fix task-type %q, credentials %q, task-output %+v", r.TaskType, r.Credentials, r.TaskOutput)
	}
	if _, ok := r.Inputs["instruction"]; ok {
		t.Error("fix takes an instruction: changing a fix once its PR is open is care-iterate's")
	}
	var uses []string
	for _, s := range r.Start.Steps {
		if s.Uses != "" {
			uses = append(uses, s.Uses)
		}
	}
	if got := strings.Join(uses, " "); got != "setup-git setup-fork configure-engine checkout-default-branch issue-comments push" {
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
		// The issue-comments step's file: trusted authors' comments only.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "comments.md"), []byte("the trusted comments"), 0o644); err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		comments := false
		for i, s := range r.Start.Steps {
			comments = comments || s.Uses == "issue-comments"
			if s.Ask == "" {
				continue
			}
			if strings.Contains(s.Ask, `file "comments.md"`) && !comments {
				t.Errorf("step %s reads comments.md before the issue-comments step", s.Label(i))
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, dir)
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
	// The comments come from the step, never from gh, which shows
	// everyone's.
	if !regexp.MustCompile(`<issue_comments>\s+the trusted comments`).MatchString(plain) || strings.Contains(plain, "--comments") {
		t.Errorf("a fix's comments are not the issue-comments step's:\n%s", plain)
	}
	if in, err := r.ResolveInputs(std, nil); err != nil || in["draft"] != "true" {
		t.Errorf("draft defaults to %q (%v), want true", in["draft"], err)
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

// The step care's start begins with points the push at the PR's branch,
// leased against the head last pushed, checks that branch out, and
// remembers the PR.
func TestPushedStepPointsThePushAtTheBranch(t *testing.T) {
	_, care, err := Builtin("care")
	if err != nil {
		t.Fatal(err)
	}
	carePushed := care.Start.Steps[4].Run
	for name, tc := range map[string]struct {
		script string
		want   map[string]string
	}{
		"care": {carePushed, map[string]string{"branch": "issue-7-1\n", "base": "b0\n", "lease": "h1", "pr": "12"}},
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
	if err := e.Uses(context.Background(), "push", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if want := "pushToFork " + taskDir + "\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// on: and label are checked when parsed, and kept from a sandbox's
// runner, which may predate them.
func TestOnAndLabel(t *testing.T) {
	withOn := "label: Draft it\non: [issue, my-pr]\n" + demoRecipe
	rec, err := Parse([]byte(withOn))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.On, []string{OnIssue, OnMyPR}) || rec.DisplayLabel() != "Draft it" {
		t.Errorf("parsed = on %v, label %q", rec.On, rec.DisplayLabel())
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
	if got.On != nil || got.Label != "" || len(got.Start.Steps) != 4 {
		t.Errorf("sandbox recipe kept factory's fields:\n%s", out)
	}
	if _, err := Parse([]byte("on: [commit]\n" + demoRecipe)); err == nil {
		t.Error("an unknown target parsed")
	}
}

// A recipe runs on its on: targets, a PR of yours on [pr] as well, and
// everywhere with none; setup: and start.on are gone, and refused.
func TestRunsOn(t *testing.T) {
	plain, err := Parse([]byte(demoRecipe))
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
		rec, err := Parse([]byte("on: " + on + "\n" + demoRecipe))
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
		"setup":    strings.Replace(demoRecipe, "start:\n  steps:", "setup:\n  steps:\n    - run: prepare\nstart:\n  steps:", 1),
		"start.on": strings.Replace(demoRecipe, "start:\n  steps:", "start:\n  on: [issue]\n  steps:", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// A run into a fresh session sends the context, once, with its first ask.
func TestARunIntoAFreshSessionSendsTheContext(t *testing.T) {
	rec, err := Parse([]byte(demoRecipe))
	if err != nil {
		t.Fatal(err)
	}
	r, _, sess, _ := newTestRunner(t)
	r.StartSession = func(context.Context) (Session, error) { return freshSession{sess}, nil }
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	want := "write it for #7 to " + filepath.Join(r.TaskDir, "plan.md")
	if len(sess.prompts) != 2 || !strings.HasPrefix(sess.prompts[0], "RULES\n") || sess.prompts[1] != want {
		t.Errorf("prompts = %q, want the context first, once, and the output's path", sess.prompts)
	}
}

type freshSession struct{ *fakeSession }

func (freshSession) Fresh() bool { return true }

// Each of care's recipes asks its own job, then the replies, and pushes,
// leased against the PR's head.
// care-rebase checks, before it pushes, that the rebase happened: the
// default branch the ask named ($TASK_DIR/base) is under HEAD. Without it
// an agent that gave up pushed the PR unchanged as if rebased.
func TestBuiltinCareRebaseChecksTheRebase(t *testing.T) {
	_, rec, err := Builtin("care-rebase")
	if err != nil {
		t.Fatal(err)
	}
	steps := rec.Start.Steps
	n := len(steps)
	if n < 2 || steps[n-1].Uses != "push" || !strings.Contains(steps[n-2].Run, `git merge-base --is-ancestor "$(cat "$TASK_DIR/base")" HEAD`) {
		t.Fatalf("want the rebase checked right before push, got the last steps %+v", steps[max(0, n-2):])
	}
}

func TestBuiltinCareRenders(t *testing.T) {
	std := map[string]string{
		"repo_owner": "o", "repo_name": "r", "url": "https://github.com/o/r/pull/9", "pr_url": "https://github.com/o/r/pull/9",
		"pr_number": "9", "pr_title": "t", "pr_body": "b", "pr_head": "feature", "pr_base": "main", "disclose": "true",
	}
	dir := t.TempDir()
	for name, v := range map[string]string{"pr": "9", "lease": "h1", "base": "b2", "upstream": "u3", "ci-logs.md": "the excerpts", "comments.md": "the trusted feedback"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The failed jobs' logs come from the ci-logs step, not from
	// `gh run view --log-failed`, which waits for the whole run; the
	// comments from the pr-comments step, trusted authors' only, never
	// from gh, which shows everyone's.
	jobs := map[string]string{"rebase": "git rebase u3", "ci": "the excerpts", "comments": "the trusted feedback"}
	for name, tc := range map[string]struct {
		want, not []string
	}{
		"care":          {want: []string{jobs["rebase"], jobs["ci"], jobs["comments"]}, not: []string{"--log-failed", "comments --paginate", "9 --comments"}},
		"care-comments": {want: []string{jobs["comments"]}, not: []string{jobs["rebase"], jobs["ci"], "comments --paginate", "9 --comments"}},
		"care-ci":       {want: []string{jobs["ci"], "gh pr checks 9"}, not: []string{jobs["rebase"], jobs["comments"], "--log-failed"}},
		"care-rebase":   {want: []string{"git rebase b2"}, not: []string{jobs["ci"], jobs["comments"]}},
		"care-iterate":  {want: []string{"rename foo"}, not: []string{jobs["rebase"], jobs["ci"], jobs["comments"]}},
	} {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		if !rec.RunsOn(OnMyPR) || rec.RunsOn(OnPR) || rec.RunsOn(OnIssue) || rec.Session != "care" || rec.TaskOutput == nil || rec.TaskOutput.Kind != "Change" {
			t.Fatalf("%s on %v in session %q, task-output %+v", name, rec.On, rec.Session, rec.TaskOutput)
		}
		overrides := map[string]string{}
		if name == "care-iterate" {
			overrides["instruction"] = "rename foo"
		}
		inputs, err := rec.ResolveInputs(std, overrides)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.CheckRender(inputs); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got strings.Builder
		ciLogs, prComments := false, false
		for i, s := range rec.Start.Steps {
			ciLogs = ciLogs || s.Uses == "ci-logs"
			prComments = prComments || s.Uses == "pr-comments"
			if s.Ask == "" {
				continue
			}
			if strings.Contains(s.Ask, `file "ci-logs.md"`) && !ciLogs {
				t.Errorf("%s step %s reads ci-logs.md before the ci-logs step", name, s.Label(i))
			}
			if strings.Contains(s.Ask, `file "comments.md"`) && !prComments {
				t.Errorf("%s step %s reads comments.md before the pr-comments step", name, s.Label(i))
			}
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, dir)
			if err != nil {
				t.Fatalf("%s step %s: %v", name, s.Label(i), err)
			}
			got.WriteString(out)
		}
		for _, w := range append(tc.want, "at\nh1, the PR's head now", "inReplyTo", "written by an AI") {
			if !strings.Contains(got.String(), w) {
				t.Errorf("%s lacks %q:\n%s", name, w, got.String())
			}
		}
		for _, w := range tc.not {
			if strings.Contains(got.String(), w) {
				t.Errorf("%s asks for %q too", name, w)
			}
		}
		if last := rec.Start.Steps[len(rec.Start.Steps)-1]; last.Uses != "push" {
			t.Errorf("%s ends with %s, not the push", name, last.Label(len(rec.Start.Steps)-1))
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
	if r.TaskOutput == nil || r.TaskOutput.Kind != "FanOut" {
		t.Fatalf("fanout task-output %+v", r.TaskOutput)
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
	if err != nil || !strings.Contains(ask, "{{range $i, $it := .items}}{{if $i}}, {{end}}{{$it.name}}{{end}}") {
		t.Errorf("the spec ask (%v):\n%s", err, ask)
	}
}

// A session's recipes land in one sandbox and one of them names the
// group; a recipe in one is recorded under it.
func TestSessions(t *testing.T) {
	var builtins []*Recipe
	for _, name := range BuiltinNames() {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		builtins = append(builtins, rec)
	}
	if err := CheckSessions(builtins); err != nil {
		t.Errorf("built-in recipes: %v", err)
	}

	in := func(name, session string, on ...string) *Recipe {
		return &Recipe{Name: name, Session: session, On: on}
	}
	for _, tc := range []struct {
		why  string
		recs []*Recipe
	}{
		{"on differs", []*Recipe{in("s", "s", "my-pr"), in("s-a", "s", "pr")}},
		{"credentials differ", []*Recipe{in("s", "s", "pr"), {Name: "s-a", Session: "s", On: []string{"pr"}, Credentials: CredentialsClone}}},
		{"none named like the tag", []*Recipe{in("s-a", "s", "pr"), in("s-b", "s", "pr")}},
	} {
		if err := CheckSessions(tc.recs); err == nil {
			t.Errorf("%s: checked", tc.why)
		}
	}
	if err := CheckSessions([]*Recipe{in("s", "s", "pr", "issue"), in("s-a", "s", "issue", "pr"), {Name: "s-b", Session: "s", On: []string{"pr"}, Credentials: CredentialsFull}}); err == nil {
		t.Error("on [pr issue] and [pr] checked as one")
	}
	if err := CheckSessions([]*Recipe{in("s", "s", "pr", "issue"), in("s-a", "s", "issue", "pr")}); err != nil {
		t.Errorf("on in another order: %v", err)
	}

	start := Part{Steps: []Step{{Run: "true"}}}
	for _, r := range []*Recipe{
		{Name: "x", Session: "Bad Name", Start: start},
		{Name: "x", Session: "s", TaskType: "fix", Start: start},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v validated", r)
		}
	}
	if r := (&Recipe{Name: "x-a", Session: "x", Start: start}); r.Validate() != nil || r.SessionName() != "x" {
		t.Errorf("a recipe in session x: %v, %q", r.Validate(), r.SessionName())
	}
	if r := (&Recipe{Name: "x"}); r.SessionName() != "x" {
		t.Errorf("an untagged recipe's session = %q, want its name", r.SessionName())
	}
}

// usageSession is a fakeSession whose engine reports each turn's tokens.
type usageSession struct {
	fakeSession
	used []acpd.ModelUsage
}

func (s *usageSession) TurnUsage() []acpd.ModelUsage { return s.used }

// Each ask's tokens go to the task's token-usage.json, where usagereport
// harvests them: a request per turn, per model, added up.
func TestRunnerRecordsEachAsksUsage(t *testing.T) {
	r, _, _, _ := newTestRunner(t)
	sess := &usageSession{used: []acpd.ModelUsage{{Model: "gemini-3-pro", Input: 1000, Output: 50, Cached: 200, Total: 1050}}}
	r.StartSession = func(context.Context) (Session, error) { return sess, nil }
	rec, err := Parse([]byte(`
name: demo
start:
  steps:
    - ask: "first"
    - ask: "second"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.TaskDir, UsageFile))
	if err != nil {
		t.Fatal(err)
	}
	var got usageFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	m := got.Models["gemini-3-pro"]
	if m == nil || m.API.TotalRequests != 2 || m.Tokens.Input != 2000 || m.Tokens.Output != 100 || m.Tokens.Cached != 400 || m.Tokens.Total != 2100 {
		t.Errorf("token-usage.json = %s", data)
	}

	// An engine that reports nothing (antigravity) writes nothing.
	r2, _, _, _ := newTestRunner(t)
	r2.StartSession = func(context.Context) (Session, error) { return &usageSession{}, nil }
	if err := r2.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r2.TaskDir, UsageFile)); !os.IsNotExist(err) {
		t.Errorf("a session that reports no usage left %s: %v", UsageFile, err)
	}
}

// The adopt recipe: strategy reuse takes the PR's commits (no agent),
// reimplement has one ask; both end with the push, and their Change, which
// a run step writes and no agent does, adopts the PR, closing it when
// asked.
func TestBuiltinAdoptRecipe(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	_, rec, err := Builtin("adopt")
	if err != nil {
		t.Fatal(err)
	}
	// Any PR; `factory pr adopt` refuses one the adopter authored.
	if !rec.RunsOn(OnPR) || rec.TaskOutput == nil || rec.TaskOutput.Kind != "Change" {
		t.Fatalf("on %v, task-output %+v", rec.On, rec.TaskOutput)
	}
	// A run step writes the Change, which a task of it still declares.
	if d := rec.OutputDecl(); d == nil || d.Kind != "Change" {
		t.Fatalf("adopt's task declares %+v", d)
	}
	std := map[string]string{"pr_url": "https://github.com/o/r/pull/5", "pr_title": "t", "pr_body": "b", "pr_number": "5", "pr_base": "main", "repo_owner": "o", "repo_name": "r", "disclose": "true"}
	inputs, err := rec.ResolveInputs(std, nil)
	if err != nil || inputs["strategy"] != "reuse" {
		t.Fatalf("inputs %v, %v", inputs, err)
	}
	if err := rec.CheckRender(inputs); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		strategy string
		asks     int
		branch   string
	}{{"reuse", 0, "pull/${INPUT_PR_NUMBER}/head"}, {"reimplement", 1, "adopt-reimplement-pr-"}} {
		td := templateData{Inputs: map[string]string{"strategy": tc.strategy}, Steps: map[string]*StepResult{}}
		var steps []Step
		for i, s := range rec.Start.Steps {
			if s.If != "" {
				run, err := condition(fmt.Sprint(i), s.If, td, "")
				if err != nil {
					t.Fatal(err)
				}
				if !run {
					continue
				}
			}
			steps = append(steps, s)
		}
		asks, branch := 0, false
		var changeStep string
		for _, s := range steps {
			if s.Ask != "" {
				asks++
			}
			if strings.Contains(s.Run, tc.branch) {
				branch = true
			}
			if strings.Contains(s.Run, "closeAdopted") {
				changeStep = s.Run
			}
		}
		if asks != tc.asks || !branch || steps[len(steps)-1].Uses != "push" || changeStep == "" {
			t.Fatalf("%s: %d asks, branch step %v, last step %+v, change step found %v", tc.strategy, asks, branch, steps[len(steps)-1], changeStep != "")
		}
		if tc.strategy == "reimplement" {
			continue // one Change step, run once below
		}

		taskDir := t.TempDir()
		e := &SandboxExecutor{TaskDir: taskDir, RepoDir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}}
		var out strings.Builder
		code, err := e.Run(context.Background(), changeStep, map[string]string{
			"pr_url": "https://github.com/o/r/pull/5", "pr_title": "Fix the thing", "pr_body": "It's \"broken\".", "close": "true",
		}, &out)
		if err != nil || code != 0 {
			t.Fatalf("%s: change step exited %d, %v\n%s", tc.strategy, code, err, out.String())
		}
		data, err := os.ReadFile(filepath.Join(taskDir, "change.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := taskoutput.Wrap("Change", string(data), taskoutput.Target{URL: "https://github.com/o/r/pull/5"}, taskoutput.Source{})
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.strategy, err, data)
		}
		c, err := doc.ChangeSpec()
		if err != nil || c.Title != "adopt: Fix the thing" || c.Adopts != "https://github.com/o/r/pull/5" || !c.CloseAdopted ||
			!strings.Contains(c.Body, "adopted from original PR https://github.com/o/r/pull/5") || !strings.Contains(c.Body, `It's "broken".`) {
			t.Errorf("%s: Change %+v, %v", tc.strategy, c, err)
		}
	}
}

// What runs of a recipe's start, given the exit codes of its steps with
// an id (absent, 0): "ask", "push", "default" (checkout-default-branch),
// "change" (writes change.yaml), "journal" (the workflow's commit).
func stepsRun(t *testing.T, rec *Recipe, inputs map[string]string, exits map[string]int) string {
	t.Helper()
	td := templateData{Inputs: inputs, Steps: map[string]*StepResult{}}
	var got []string
	for i, s := range rec.Start.Steps {
		if s.If != "" {
			run, err := condition(fmt.Sprint(i), s.If, td, "")
			if err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			if !run {
				if s.ID != "" {
					td.Steps[s.ID] = &StepResult{Skipped: true}
				}
				continue
			}
		}
		switch {
		case s.ID != "":
			td.Steps[s.ID] = &StepResult{ExitCode: exits[s.ID]}
		case s.Ask != "":
			got = append(got, "ask")
		case s.Uses == "push":
			got = append(got, "push")
		case s.Uses == "checkout-default-branch":
			got = append(got, "default")
		case strings.Contains(s.Run, "change.yaml"):
			got = append(got, "change")
		case strings.Contains(s.Run, "chore(workflow)"):
			got = append(got, "journal")
		}
	}
	return strings.Join(got, ",")
}

// The agent recipe, a chore on the repository: a push and a Change only
// when there is something to push, nothing kept with skip_pr, and no
// precondition (a workflow's alone).
func TestBuiltinAgentRecipe(t *testing.T) {
	_, rec, err := Builtin(AgentRecipe)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.RunsOn(OnRepo) || rec.RunsOn(OnIssue) || rec.RunsOn(OnPR) || rec.TaskType != "agent" ||
		rec.TaskOutput == nil || rec.TaskOutput.Kind != "Change" || !rec.TaskOutput.Optional {
		t.Fatalf("on %v, task-type %q, task-output %+v", rec.On, rec.TaskType, rec.TaskOutput)
	}
	std := map[string]string{"repo_owner": "o", "repo_name": "r", "disclose": "true"}
	if _, err := rec.ResolveInputs(std, nil); err == nil {
		t.Error("the agent recipe ran with no prompt")
	}
	if _, ok := rec.Inputs["precondition"]; ok {
		t.Error("a chore takes a precondition")
	}
	for _, skip := range []string{"false", "true"} {
		inputs, err := rec.ResolveInputs(std, map[string]string{"prompt": "do it", "agent_name": "Docs Bot", "skip_pr": skip})
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.CheckRender(inputs); err != nil {
			t.Fatalf("skip_pr %s: %v", skip, err)
		}
	}
	for _, tc := range []struct {
		skip    string
		changed int
		want    string
	}{
		{"false", 0, "default,ask,change,push"},
		{"false", 1, "default,ask"},
		{"true", 0, "default,ask"},
	} {
		got := stepsRun(t, rec, map[string]string{"skip_pr": tc.skip}, map[string]int{"changed": tc.changed})
		if got != tc.want {
			t.Errorf("skip_pr %s (changed %d) ran %q, want %q", tc.skip, tc.changed, got, tc.want)
		}
	}
}

// The workflow recipe, a step of a workflow on its issue: no task output,
// the agent's shell gets SESSION_ID and RELATED_ISSUES, and its
// precondition, which sees them too, gates the step.
func TestBuiltinWorkflowRecipe(t *testing.T) {
	_, rec, err := Builtin(WorkflowRecipe)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.RunsOn(OnIssue) || rec.RunsOn(OnRepo) || rec.RunsOn(OnPR) || rec.TaskType != "workflow" || rec.TaskOutput != nil {
		t.Fatalf("on %v, task-type %q, task-output %+v", rec.On, rec.TaskType, rec.TaskOutput)
	}
	std := map[string]string{"repo_owner": "o", "repo_name": "r", "issue_number": "7", "disclose": "false"}
	inputs, err := rec.ResolveInputs(std, map[string]string{"prompt": "next step", "agent_name": "checklist", "session_id": "issue-7", "related_issues": "8,9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckRender(inputs); err != nil {
		t.Fatal(err)
	}
	env, err := rec.RenderAgentEnv(inputs)
	if err != nil || env["SESSION_ID"] != "issue-7" || env["RELATED_ISSUES"] != "8,9" {
		t.Errorf("agent env %v, %v", env, err)
	}
	for precondition, want := range map[int]string{0: "ask,journal", 1: ""} {
		if got := stepsRun(t, rec, map[string]string{"precondition": "exit 1"}, map[string]int{"precondition": precondition}); got != want {
			t.Errorf("precondition %d ran %q, want %q", precondition, got, want)
		}
	}
	if got := stepsRun(t, rec, map[string]string{"precondition": ""}, nil); got != "ask,journal" {
		t.Errorf("no precondition ran %q", got)
	}

	var step string
	for _, s := range rec.Start.Steps {
		if s.ID == "precondition" {
			step = s.Run
		}
	}
	for related, wantCode := range map[string]int{"8,9": 0, "": 3} {
		taskDir := t.TempDir()
		e := &SandboxExecutor{TaskDir: taskDir, RepoDir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}}
		var out strings.Builder
		code, err := e.Run(context.Background(), step, map[string]string{
			"precondition":   "#!/bin/bash\n[ \"$SESSION_ID\" = issue-7 ] && [ -n \"$RELATED_ISSUES\" ] || exit 3",
			"related_issues": related, "session_id": "issue-7",
		}, &out)
		if err != nil {
			t.Fatal(err)
		}
		if (code == 0) != (wantCode == 0) {
			t.Errorf("related %q: precondition step exited %d\n%s", related, code, out.String())
		}
	}
}

// The agent recipe's Change step: a chore's PR is titled after the
// agent, lists the commits and carries the labels.
func TestAgentChangeStep(t *testing.T) {
	for _, tool := range []string{"git", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	_, rec, err := Builtin(AgentRecipe)
	if err != nil {
		t.Fatal(err)
	}
	var step string
	for _, s := range rec.Start.Steps {
		if strings.Contains(s.Run, "change.yaml") {
			step = s.Run
		}
	}
	if step == "" {
		t.Fatal("no Change step")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=x@x", "-c", "user.name=x"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "chore: tidy the docs")

	taskDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(taskDir, "base"), []byte(base+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &SandboxExecutor{TaskDir: taskDir, RepoDir: repo, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}}
	var out strings.Builder
	code, err := e.Run(context.Background(), step, map[string]string{
		"agent_name": "Docs Bot", "agent_file": ".agents/docs.md", "repo_owner": "o", "repo_name": "r", "labels": "factory,docs",
	}, &out)
	if err != nil || code != 0 {
		t.Fatalf("exited %d, %v\n%s", code, err, out.String())
	}
	data, err := os.ReadFile(filepath.Join(taskDir, "change.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := taskoutput.Wrap("Change", string(data), taskoutput.Target{URL: "https://github.com/o/r"}, taskoutput.Source{})
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	c, err := doc.ChangeSpec()
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "chore: Docs Bot" || !strings.Contains(c.Body, "- chore: tidy the docs") || strings.Contains(c.Body, "- base") ||
		!strings.Contains(c.Body, "https://github.com/o/r/blob/HEAD/.agents/docs.md") || fmt.Sprint(c.Labels) != "[factory docs]" || c.Draft == nil || *c.Draft {
		t.Errorf("Change %+v", c)
	}
}

// adopt's branch step: the PR's head, from the repository's
// pull/<n>/head, becomes adopt-pr-<n>, its base where it branched off.
func TestAdoptBranchStep(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	_, rec, err := Builtin("adopt")
	if err != nil {
		t.Fatal(err)
	}
	var step string
	for _, s := range rec.Start.Steps {
		if strings.Contains(s.Run, "pull/${INPUT_PR_NUMBER}/head") {
			step = s.Run
		}
	}
	if step == "" {
		t.Fatal("no branch step")
	}
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=x@x", "-c", "user.name=x", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The repository: main, and a PR's head under refs/pull/5/head.
	up := filepath.Join(root, "up")
	git(root, "init", "-q", up)
	git(up, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(up, "rev-parse", "HEAD")
	git(up, "checkout", "-q", "-b", "contributor")
	git(up, "commit", "-q", "--allow-empty", "-m", "the change")
	head := git(up, "rev-parse", "HEAD")
	git(up, "update-ref", "refs/pull/5/head", head)
	git(up, "checkout", "-q", "main")
	git(up, "commit", "-q", "--allow-empty", "-m", "main moved on")
	// The checkout, its upstream the repository.
	co := filepath.Join(root, "co")
	git(root, "clone", "-q", up, co)
	git(co, "remote", "add", "upstream", up)

	taskDir := t.TempDir()
	e := &SandboxExecutor{TaskDir: taskDir, RepoDir: co, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}}
	var out strings.Builder
	code, err := e.Run(context.Background(), step, map[string]string{"pr_number": "5", "pr_base": "main"}, &out)
	if err != nil || code != 0 {
		t.Fatalf("exited %d, %v\n%s", code, err, out.String())
	}
	read := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(taskDir, name))
		return strings.TrimSpace(string(data))
	}
	if got := git(co, "rev-parse", "HEAD"); got != head || git(co, "symbolic-ref", "--short", "HEAD") != "adopt-pr-5" {
		t.Errorf("HEAD %s on %s, want the PR's head %s on adopt-pr-5", got, git(co, "symbolic-ref", "--short", "HEAD"), head)
	}
	if read("branch") != "adopt-pr-5" || read("base") != base {
		t.Errorf("branch %q, base %q; want adopt-pr-5, %s", read("branch"), read("base"), base)
	}
}

// agent-env: each value rendered against the inputs; a name acpd would
// refuse, or a value that is not a template, fails the recipe up front.
func TestAgentEnv(t *testing.T) {
	rec, err := Parse([]byte("name: a\nagent-env: {SESSION_ID: \"{{ .Inputs.session_id }}\"}\nstart: {steps: [{run: x}]}"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := rec.RenderAgentEnv(map[string]string{"session_id": "issue-7"})
	if err != nil || env["SESSION_ID"] != "issue-7" {
		t.Errorf("env %v, %v", env, err)
	}
	if env, err := rec.RenderAgentEnv(map[string]string{"session_id": ""}); err != nil || len(env) != 0 {
		t.Errorf("an empty value is set: env %v, %v", env, err)
	}
	if err := rec.CheckRender(map[string]string{}); err == nil {
		t.Error("CheckRender passed an agent-env reading an input the run does not set")
	}
	for _, bad := range []string{`{GITHUB_TOKEN: x}`, `{PATH: x}`, `{A: "{{ .Inputs.x "}`} {
		if _, err := Parse([]byte("name: a\nagent-env: " + bad + "\nstart: {steps: [{run: x}]}")); err == nil {
			t.Errorf("agent-env %s parsed", bad)
		}
	}
}

// The run recipes, a run's phases in the run's sandbox: the fork
// (runs-remote) before the checkout, the runs branch before the ask
// (run-prepare, told the phase), the commit after it (run-finish), each phase's own inputs, and the agent's shell
// told the member's GCP project and region and the run's resource prefix,
// unset when the member has none.
func TestBuiltinRunRecipes(t *testing.T) {
	for phase, own := range map[string][]string{"plan": {"intent", "runbook", "target_pr"}, "deploy": nil, "teardown": {"intent"}} {
		_, rec, err := Builtin(RunRecipe(phase))
		if err != nil {
			t.Fatal(err)
		}
		if !rec.RunsOn(OnRepo) || rec.RunsOn(OnIssue) || rec.RunsOn(OnPR) || rec.TaskType != "run" || rec.TaskOutput != nil {
			t.Fatalf("%s: on %v, task-type %q, task-output %+v", rec.Name, rec.On, rec.TaskType, rec.TaskOutput)
		}
		if sb := rec.Sandbox; sb == nil || sb.Prefix != "run" || !sb.Own() || sb.Disk != "30Gi" || sb.EphemeralStorage != "10Gi" || sb.CPURequest != "2" {
			t.Errorf("%s: sandbox %+v, want the run's own, 30Gi/10Gi, 2 CPU", rec.Name, rec.Sandbox)
		}
		var kinds []string
		for _, s := range rec.Start.Steps {
			switch {
			case s.Uses != "" && s.With["phase"] != "":
				kinds = append(kinds, s.Uses+"("+s.With["phase"]+")")
			case s.Uses != "":
				kinds = append(kinds, s.Uses)
			default:
				kinds = append(kinds, s.Kind())
			}
		}
		if got, want := strings.Join(kinds, ","), "setup-git,setup-repo,runs-remote,run,checkout-default-branch,configure-engine,run-prepare("+phase+"),ask,run-finish("+phase+")"; got != want {
			t.Errorf("%s: steps %s, want %s", rec.Name, got, want)
		}
		var inputs []string
		for name := range rec.Inputs {
			if name != "run" {
				inputs = append(inputs, name)
			}
		}
		slices.Sort(inputs)
		if fmt.Sprint(inputs) != fmt.Sprint(own) {
			t.Errorf("%s takes %v, want %v", rec.Name, inputs, own)
		}

		std := map[string]string{"repo_owner": "o", "repo_name": "in-cluster-storage", "repo_url": "https://github.com/o/r", "url": "https://github.com/o/r", "disclose": "true", "gcp_project": "p1", "gcp_region": "us-central1"}
		resolved, err := rec.ResolveInputs(std, map[string]string{"run": "gke-1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.CheckRender(resolved); err != nil {
			t.Fatalf("%s: %v", rec.Name, err)
		}
		env, err := rec.RenderAgentEnv(resolved)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"RUN_NAME": "gke-1", "RUN_RESOURCE_PREFIX": "ics-gke-1", "GOOGLE_CLOUD_PROJECT": "p1", "CLOUDSDK_CORE_PROJECT": "p1", "CLOUDSDK_COMPUTE_REGION": "us-central1"}
		if fmt.Sprint(env) != fmt.Sprint(want) {
			t.Errorf("%s: agent env %v, want %v", rec.Name, env, want)
		}
		resolved["gcp_project"], resolved["gcp_region"] = "", ""
		if env, _ := rec.RenderAgentEnv(resolved); env["CLOUDSDK_CORE_PROJECT"] != "" || len(env) != 2 {
			t.Errorf("%s, no project: agent env %v, want the project and region unset", rec.Name, env)
		}
	}
}

// A run's cloud resources are named shortName(repo)-run: a hyphenated
// repository's initials, else its name cut to 8.
func TestResourcePrefix(t *testing.T) {
	for repo, want := range map[string]string{"in-cluster-storage": "ics-r1", "k8s-config-connector": "kcc-r1", "granule": "granule-r1", "verylongname": "verylong-r1", "My_Repo": "mr-r1"} {
		if got := ResourcePrefix(repo, "r1"); got != want {
			t.Errorf("ResourcePrefix(%s) = %s, want %s", repo, got, want)
		}
	}
}

// Every built-in's result is a file its agent writes ({{ output "<from>" }}
// in an ask), or a run step does (adopt's Change): none is captured from
// a reply, so a later turn of the conversation can write it again.
// outputNameRE finds the files an ask tells the agent to write.
var outputNameRE = regexp.MustCompile(`\{\{ output "([^"]+)" \}\}`)

func TestBuiltinResultsAreWrittenFiles(t *testing.T) {
	for _, name := range BuiltinNames() {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, s := range rec.Start.Steps {
			if s.Capture != "" {
				t.Errorf("%s: step %s captures %s", name, s.Label(i), s.Capture)
			}
			// An ask that names a file the agent writes expects it.
			for _, m := range outputNameRE.FindAllStringSubmatch(s.Ask, -1) {
				if s.Expects != m[1] && !(rec.TaskOutput != nil && rec.TaskOutput.Optional && rec.TaskOutput.From == m[1]) {
					t.Errorf("%s: step %s names %s but does not expect it", name, s.Label(i), m[1])
				}
			}
		}
		if rec.TaskOutput == nil {
			continue
		}
		from := rec.TaskOutput.From
		named := fmt.Sprintf(`{{ output %q }}`, from)
		if !slices.ContainsFunc(rec.Start.Steps, func(s Step) bool {
			return strings.Contains(s.Ask, named) || strings.Contains(s.Run, from)
		}) {
			t.Errorf("%s: no ask names %s, and no run step writes %s", name, named, from)
		}
	}
}

// fullSession is a conversation too long to take another turn, as gemini
// answers one: the turn stops for max_tokens with nothing written.
type fullSession struct {
	fakeSession
	reply  string
	output int64
}

func (s *fullSession) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return s.reply, &StopError{Reason: "max_tokens"}
}

func (s *fullSession) TurnUsage() []acpd.ModelUsage {
	return []acpd.ModelUsage{{Model: "gemini-3.7-flash", Output: s.output}}
}

// A run continuing a conversation too long to take another turn (KCC PR
// 14000's care, 2026-10-15: six care-ci runs stopped for max_tokens with
// no tokens used) goes on in one of its own, sent the recipe's context
// as a first run is, and says so. Only at its first ask, once, and only
// for a turn that wrote nothing: a reply that ran out is not a full
// conversation.
func TestRunnerLeavesAFullConversation(t *testing.T) {
	rec, err := Parse([]byte(`
name: demo
context: RULES
start:
  steps:
    - ask: "fix CI"
    - ask: "write the result"
`))
	if err != nil {
		t.Fatal(err)
	}
	setup := func(t *testing.T, full *fullSession, continues bool) (*Runner, *fakeSession, *int) {
		r, _, _, _ := newTestRunner(t)
		fresh, rotations := &fakeSession{}, 0
		r.StartSession = func(context.Context) (Session, error) { return full, nil }
		r.Continues = continues
		r.NewSession = func(context.Context) (Session, error) { return fresh, nil }
		r.Rotated = func() error { rotations++; return nil }
		return r, fresh, &rotations
	}

	t.Run("left at the first ask, the context sent again", func(t *testing.T) {
		full := &fullSession{}
		r, fresh, rotations := setup(t, full, true)
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if *rotations != 1 || !full.closed {
			t.Errorf("rotations = %d, closed = %v", *rotations, full.closed)
		}
		if len(fresh.prompts) != 2 || fresh.prompts[0] != "RULES\n\n---\n\nfix CI" || fresh.prompts[1] != "write the result" {
			t.Errorf("new conversation's prompts = %q", fresh.prompts)
		}
	})

	for name, tc := range map[string]struct {
		full      *fullSession
		continues bool
	}{
		"a reply that ran out":       {&fullSession{reply: "Halfway thro", output: 65536}, true},
		"output tokens, no text":     {&fullSession{output: 12}, true},
		"the run's own conversation": {&fullSession{}, false},
	} {
		t.Run(name+" fails the run", func(t *testing.T) {
			r, fresh, rotations := setup(t, tc.full, tc.continues)
			err := r.Run(context.Background(), rec)
			var stop *StopError
			if !errors.As(err, &stop) || stop.Reason != "max_tokens" || *rotations != 0 || len(fresh.prompts) != 0 {
				t.Errorf("err = %v, rotations = %d, new conversation's prompts = %q", err, *rotations, fresh.prompts)
			}
		})
	}

	t.Run("not again when the new one is full too", func(t *testing.T) {
		r, _, rotations := setup(t, &fullSession{}, true)
		r.NewSession = func(context.Context) (Session, error) { return &fullSession{}, nil }
		if err := r.Run(context.Background(), rec); err == nil || *rotations != 1 {
			t.Errorf("err = %v, rotations = %d", err, *rotations)
		}
	})

	t.Run("not at a later ask", func(t *testing.T) {
		calls := 0
		sess := &askThenFull{fakeSession: &calls}
		r, fresh, rotations := setup(t, &fullSession{}, true)
		r.StartSession = func(context.Context) (Session, error) { return sess, nil }
		if err := r.Run(context.Background(), rec); err == nil || *rotations != 0 || len(fresh.prompts) != 0 {
			t.Errorf("err = %v, rotations = %d", err, *rotations)
		}
	})
}

// askThenFull answers its first turn and is full at the second: the run's
// own work is in the conversation, which a new one would lose.
type askThenFull struct{ fakeSession *int }

func (s *askThenFull) Ask(context.Context, string) (string, error) {
	*s.fakeSession++
	if *s.fakeSession == 1 {
		return "fixed", nil
	}
	return "", &StopError{Reason: "max_tokens"}
}
func (s *askThenFull) Close() error { return nil }

// A run: step runs in the checkout, so in every built-in recipe a step
// that makes it (setup-repo, setup-fork or clone; not setup-git, which only
// configures git and gh) comes first: on a sandbox without one, an earlier
// run: fails to start at all (the KCC release chore failed every run on a
// new sandbox, 2026-10-15 to 21).
func TestBuiltinRunStepsComeAfterTheCheckout(t *testing.T) {
	makesCheckout := map[string]bool{"setup-repo": true, "setup-fork": true, "clone": true}
	for _, name := range BuiltinNames() {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, step := range rec.Start.Steps {
			if makesCheckout[step.Uses] {
				break
			}
			if step.Run != "" {
				t.Errorf("%s: start step %d is a run: before any step makes the checkout", name, i+1)
				break
			}
		}
	}
}

// A recipe on a repository declares its sandbox, and only such a recipe
// does: a special kind with nothing else, or its own with its prefix and
// every size.
func TestSandboxDecl(t *testing.T) {
	full := "cpu-request: '4', cpu-limit: '8', memory-request: 12Gi, memory-limit: 16Gi, ephemeral-storage: 10Gi, disk: 40Gi"
	rec, err := Parse([]byte("name: r\non: [repo]\nsandbox: {prefix: kcc, " + full + "}\nstart: {steps: [{ask: a}]}\n"))
	if err != nil || rec.Sandbox.Prefix != "kcc" || !rec.Sandbox.Own() || rec.Sandbox.CPURequest != "4" || rec.Sandbox.Disk != "40Gi" {
		t.Fatalf("own = %+v, %v", rec, err)
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"repo, no sandbox": {"name: r\non: [repo]\nstart: {steps: [{ask: a}]}\n", "declares its sandbox:"},
		"on an issue":      {"name: r\non: [issue]\nsandbox: {kind: warm}\nstart: {steps: [{ask: a}]}\n", "sandbox: is for a recipe on: [repo]"},
		"no prefix":        {"name: r\non: [repo]\nsandbox: {" + full + "}\nstart: {steps: [{ask: a}]}\n", "sandbox prefix"},
		"a scope":          {"name: r\non: [repo]\nsandbox: {prefix: p, scope: key, " + full + "}\nstart: {steps: [{ask: a}]}\n", "field scope not found"},
		"a size missing":   {"name: r\non: [repo]\nsandbox: {prefix: p, cpu-request: '4'}\nstart: {steps: [{ask: a}]}\n", "declares no cpu-limit"},
		"not a size":       {"name: r\non: [repo]\nsandbox: {prefix: p, cpu-request: '4', cpu-limit: '8', memory-request: 1Gi, memory-limit: lots, ephemeral-storage: 1Gi, disk: 1Gi}\nstart: {steps: [{ask: a}]}\n", "sandbox memory-limit"},
		"other kind":       {"name: r\non: [repo]\nsandbox: {kind: research}\nstart: {steps: [{ask: a}]}\n", "is not one of: warm, agent"},
		"warm sized":       {"name: r\non: [repo]\nsandbox: {kind: warm, disk: 1Gi}\nstart: {steps: [{ask: a}]}\n", "declares no disk"},
		"bad prefix":       {"name: r\non: [repo]\nsandbox: {prefix: Bad-Prefix, " + full + "}\nstart: {steps: [{ask: a}]}\n", "sandbox prefix"},
	} {
		if _, err := Parse([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// A kept recipe is read without its sandbox:, whatever shape an older
	// factory gave it: the sandbox is made by then.
	for _, y := range []string{
		"name: research\non: [repo]\nstart: {steps: [{ask: a}]}\n",
		"name: r\non: [repo]\nsandbox: {kind: research}\nstart: {steps: [{ask: a}]}\n",
		"name: r\non: [repo]\nsandbox: {prefix: rsch, scope: key, disk: 30Gi}\nstart: {steps: [{ask: a}]}\n",
	} {
		if rec, err := ParseKept([]byte(y)); err != nil || rec.Sandbox != nil || len(rec.Start.Steps) != 1 {
			t.Errorf("kept %q = %+v, %v", y, rec, err)
		}
	}
	if _, err := ParseKept([]byte("name: r\non: [repo]\nbogus: 1\nstart: {steps: [{ask: a}]}\n")); err == nil {
		t.Error("a kept recipe's other fields are not held")
	}
}
