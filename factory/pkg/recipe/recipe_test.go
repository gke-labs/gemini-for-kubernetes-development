package recipe

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

func TestBuiltinTriageParses(t *testing.T) {
	data, r, err := Builtin("triage")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || r.Name != "triage" || r.Context == "" {
		t.Fatalf("recipe = %+v", r)
	}
	last := r.Steps[len(r.Steps)-1]
	if last.Capture != "triage-output.yaml" || fmt.Sprint(r.Outputs) != "[triage-output.yaml]" {
		t.Errorf("captures %q, outputs %v; want triage-output.yaml as both (what the CLI reads)", last.Capture, r.Outputs)
	}
}

func TestParseRejects(t *testing.T) {
	for name, y := range map[string]string{
		"no name":          "steps: [{run: x}]",
		"no steps":         "name: a",
		"two kinds":        "name: a\nsteps: [{run: x, ask: y}]",
		"no kind":          "name: a\nsteps: [{id: x}]",
		"unknown uses":     "name: a\nsteps: [{uses: curl-the-token}]",
		"with on run":      "name: a\nsteps: [{run: x, with: {a: b}}]",
		"capture on run":   "name: a\nsteps: [{run: x, capture: out.txt}]",
		"capture path":     "name: a\nsteps: [{ask: x, capture: ../out.txt}]",
		"capture dotfile":  "name: a\nsteps: [{ask: x, capture: .bashrc}]",
		"duplicate id":     "name: a\nsteps: [{id: a, run: x}, {id: a, run: y}]",
		"bad id":           "name: a\nsteps: [{id: A-1, run: x}]",
		"unknown field":    "name: a\nsteps: [{run: x, shell: zsh}]",
		"continue on uses": "name: a\nsteps: [{uses: setup-git, continue-on-error: true}]",
		"bad input name":   "name: a\ninputs: {Focus: {}}\nsteps: [{run: x}]",
		"output path":      "name: a\noutputs: [../x]\nsteps: [{run: x}]",
		"output dotfile":   "name: a\noutputs: [.env]\nsteps: [{run: x}]",
		"required default": "name: a\ninputs: {focus: {required: true, default: x}}\nsteps: [{run: x}]",
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
steps: [{run: x}]
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

// TestBuiltinTriageRendersFromIssueInputs: `recipe run --recipe triage`
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
	for i, s := range r.Steps {
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
	r, err := Parse([]byte("name: a\noutputs: [diff.txt, reply.md]\nsteps: [{ask: x, capture: reply.md}]\n"))
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
		"uses":     "name: a\nsteps: [{uses: setup-repo, with: {fail: \"yes\"}}, {ask: never}]",
		"run":      "name: a\nsteps: [{run: exit 1}, {ask: never}]",
		"template": "name: a\nsteps: [{ask: \"{{ .Inputs.missing }}\"}, {run: never}]",
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
