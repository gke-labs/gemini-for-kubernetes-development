package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// scriptedSession answers each ask with the next of errs (nil: a reply),
// and records what it was asked.
type scriptedSession struct {
	name    string
	prompts []string
	errs    []error
	closed  bool
}

func (s *scriptedSession) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if n := len(s.prompts); n <= len(s.errs) && s.errs[n-1] != nil {
		return "", s.errs[n-1]
	}
	return fmt.Sprintf("%s reply %d", s.name, len(s.prompts)), nil
}

func (s *scriptedSession) Close() error { s.closed = true; return nil }

// namedRunner is newTestRunner with step sessions: each start of a step
// session is recorded as "<name>@<fallback>" and gets the next session
// made for that name (or a fresh one).
func namedRunner(t *testing.T, script map[string][]*scriptedSession) (*Runner, *fakeSession, *[]string, map[string][]*scriptedSession) {
	t.Helper()
	r, _, sess, _ := newTestRunner(t)
	starts := &[]string{}
	made := map[string][]*scriptedSession{}
	r.Sleep = func(context.Context, time.Duration) error { return nil }
	r.StartNamed = func(_ context.Context, name string, fallback int) (Session, string, error) {
		if fallback > 1 {
			return nil, "", ErrNoFallback
		}
		*starts = append(*starts, fmt.Sprintf("%s@%d", name, fallback))
		var s *scriptedSession
		if q := script[name]; len(q) > 0 {
			s, script[name] = q[0], q[1:]
		} else {
			s = &scriptedSession{name: name}
		}
		made[name] = append(made[name], s)
		return s, fmt.Sprintf("model-%d", fallback), nil
	}
	return r, sess, starts, made
}

// A step session is a conversation of its own: its asks go to it, the
// others to the run's, each continuing its own; it is not sent the
// recipe's context; and it is closed when the run ends.
func TestStepSessionsAreConversationsOfTheirOwn(t *testing.T) {
	r, sess, starts, made := namedRunner(t, nil)
	rec, err := Parse([]byte(`
name: demo
context: RULES
start:
  steps:
    - ask: "fix {{ .Inputs.issue }}"
    - id: review
      ask: "review the diff"
      session: review-1
    - ask: "address: {{ .Steps.review.Reply }}"
    - ask: "anything else?"
      session: review-1
    - ask: "second opinion"
      session: review-2
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sess.prompts, " | "); got != "RULES\n\n---\n\nfix #7 | address: review-1 reply 1" {
		t.Errorf("the run's conversation was asked %q", got)
	}
	if got := strings.Join(*starts, ","); got != "review-1@0,review-2@0" {
		t.Errorf("step sessions started %s, want each once", got)
	}
	r1 := made["review-1"][0]
	if got := strings.Join(r1.prompts, " | "); got != "review the diff | anything else?" {
		t.Errorf("review-1 was asked %q (no recipe context, both its asks)", got)
	}
	if !r1.closed || !made["review-2"][0].closed || !sess.closed {
		t.Error("a session was left open after the run")
	}
}

// A step session's reply can be captured, and a file it is told to write
// is checked as the run's conversation's is.
func TestStepSessionCaptureAndExpects(t *testing.T) {
	r, _, _, made := namedRunner(t, nil)
	rec, err := Parse([]byte(`
name: demo
start:
  steps:
    - ask: "review"
      session: reviewer
      capture: review.txt
    - ask: "write findings.yaml"
      session: reviewer
      expects: findings.yaml
`))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), "the agent wrote no findings.yaml") {
		t.Fatalf("Run = %v, want the missing findings.yaml", err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.TaskDir, "review.txt")); string(got) != "reviewer reply 1" {
		t.Errorf("captured %q", got)
	}
	if n := len(made["reviewer"][0].prompts); n != 3 {
		t.Errorf("reviewer was asked %d times, want its two asks and the reminder", n)
	}
}

// A step session whose exchange broke is started again and asked again;
// one the engine could not serve starts again on the next model; past the
// list, the step fails with the engine's error.
func TestStepSessionRetriesAndFallsBack(t *testing.T) {
	broke := errors.New("RPC error [500]: exception TypeError: fetch failed sending request")
	busy := errors.New("RPC error [503]: This model is currently experiencing high demand")
	rec, err := Parse([]byte("name: demo\nstart: {steps: [{ask: review, session: reviewer}]}"))
	if err != nil {
		t.Fatal(err)
	}

	r, _, starts, made := namedRunner(t, map[string][]*scriptedSession{
		"reviewer": {{name: "a", errs: []error{broke}}, {name: "b", errs: []error{busy}}, {name: "c"}},
	})
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(*starts, ","); got != "reviewer@0,reviewer@0,reviewer@1" {
		t.Errorf("starts %s: again on the same model after a broken exchange, then the next", got)
	}
	for _, s := range made["reviewer"] {
		if len(s.prompts) != 1 || s.prompts[0] != "review" {
			t.Errorf("session %s was asked %q, want the ask whole, once", s.name, s.prompts)
		}
	}

	r, _, _, _ = namedRunner(t, map[string][]*scriptedSession{
		"reviewer": {{name: "a", errs: []error{busy}}, {name: "b", errs: []error{busy}}},
	})
	if err := r.Run(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "high demand") {
		t.Errorf("Run = %v, want the engine's error once the models are spent", err)
	}
}

func TestStepSessionNeedsARunnerThatOpensThem(t *testing.T) {
	r, _, _, _ := newTestRunner(t)
	r.Log = io.Discard
	rec, err := Parse([]byte("name: demo\nstart: {steps: [{ask: review, session: reviewer}]}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), rec); err == nil || !strings.Contains(err.Error(), `cannot open the step session "reviewer"`) {
		t.Errorf("Run = %v", err)
	}
}

func TestStepSessionValidation(t *testing.T) {
	for _, bad := range []string{
		"name: d\nstart: {steps: [{run: x, session: s}]}",
		"name: d\nstart: {steps: [{ask: x, session: Bad_Name}]}",
		"name: d\nstart: {steps: [{ask: x, session: ../x}]}",
		"name: d\non: [my-pr]\nsession: care\nstart: {steps: [{ask: x, session: care}]}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse accepted %q", bad)
		}
	}
	if _, err := Parse([]byte("name: d\nstart: {steps: [{ask: x, session: review-1}]}")); err != nil {
		t.Errorf("Parse refused a step session: %v", err)
	}
}

// A step session runs its own engine beside the run's, in the checkout,
// and keeps its transcript apart: <taskDir>/session-<name>/.
func TestNamedACPSessionKeepsItsOwnTranscript(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(fakeACPAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	acpd.Engines["recipe-fake"] = acpd.Engine{Command: "python3", Args: []string{script}, APIKeyEnv: "FAKE_KEY", ModelFlag: "--model"}
	t.Cleanup(func() { delete(acpd.Engines, "recipe-fake") })

	taskDir, repo := t.TempDir(), t.TempDir()
	run, err := StartACPSession(context.Background(), "recipe-fake", "m-1", "k", repo, taskDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	review, err := StartNamedACPSession(context.Background(), "review-1", "recipe-fake", "m-2", "k", repo, taskDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()

	if got, err := review.Ask(context.Background(), "look"); err != nil || got != "turn 1: look argv=--model m-2 gh=none" {
		t.Errorf("review asked: %q, %v", got, err)
	}
	if got, err := run.Ask(context.Background(), "fix"); err != nil || got != "turn 1: fix argv=--model m-1 gh=none" {
		t.Errorf("run asked: %q, %v (its own first turn, not the reviewer's second)", got, err)
	}
	for _, dir := range []string{"session", "session-review-1"} {
		if _, err := os.Stat(filepath.Join(taskDir, dir, "stream.ndjson")); err != nil {
			t.Errorf("no transcript in %s: %v", dir, err)
		}
	}
}
