package recipe

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// failingSession answers its turns with errs in order, then like a
// fakeSession.
type failingSession struct {
	fakeSession
	errs []error
}

func (s *failingSession) Ask(ctx context.Context, prompt string) (string, error) {
	if len(s.errs) > 0 {
		s.prompts = append(s.prompts, prompt)
		err := s.errs[0]
		s.errs = s.errs[1:]
		return "", err
	}
	return s.fakeSession.Ask(ctx, prompt)
}

const busy = "prompt failed: RPC error [503]: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later."

// A turn the engine could not serve (gemini at capacity, #14839) goes on
// in the same conversation on the next model of the run's list, told that
// another model continues; the first session is closed. A turn that fails
// on its own account does not fall back, and nor does a run whose list is
// spent: its error stands.
func TestRunnerFallsBackToTheNextModel(t *testing.T) {
	rec, err := Parse([]byte(`
name: demo
context: RULES
start:
  steps:
    - ask: fix issue {{ .Inputs.issue }}
`))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("at capacity: the next model continues", func(t *testing.T) {
		r, _, _, _ := newTestRunner(t)
		first := &failingSession{errs: []error{errors.New(busy)}}
		second := &failingSession{errs: []error{errors.New("prompt failed: RPC error [429]: RESOURCE_EXHAUSTED")}}
		third := &fakeSession{}
		r.StartSession = func(context.Context) (Session, error) { return first, nil }
		var log bytes.Buffer
		r.Log = &log
		models := []string{"gemini-3.6-flash", "gemini-3.5-flash"}
		next := []Session{second, third}
		r.Fallback = func(context.Context) (Session, string, error) {
			s, m := next[0], models[0]
			next, models = next[1:], models[1:]
			return s, m, nil
		}
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if !first.closed || !second.closed {
			t.Errorf("the sessions given up on were not closed: %v %v", first.closed, second.closed)
		}
		if len(third.prompts) != 1 || !strings.Contains(third.prompts[0], "You are a different model continuing the same conversation") || !strings.HasSuffix(third.prompts[0], "fix issue #7") || strings.Contains(third.prompts[0], "RULES") {
			t.Errorf("the next model was sent %q", third.prompts)
		}
		for _, want := range []string{"continuing the conversation on gemini-3.6-flash", "continuing the conversation on gemini-3.5-flash"} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("log lacks %q:\n%s", want, log.String())
			}
		}
	})

	t.Run("the list spent: the engine's error stands", func(t *testing.T) {
		r, _, _, _ := newTestRunner(t)
		first := &failingSession{errs: []error{errors.New(busy)}}
		r.StartSession = func(context.Context) (Session, error) { return first, nil }
		r.Fallback = func(context.Context) (Session, string, error) { return nil, "", ErrNoFallback }
		err := r.Run(context.Background(), rec)
		if err == nil || !strings.Contains(err.Error(), "high demand") || strings.Contains(err.Error(), "falling back") {
			t.Errorf("err = %v, want the engine's", err)
		}
	})

	t.Run("a turn's own failure: no fallback", func(t *testing.T) {
		r, _, _, _ := newTestRunner(t)
		first := &failingSession{errs: []error{errors.New("prompt failed: the agent stopped: max turns")}}
		r.StartSession = func(context.Context) (Session, error) { return first, nil }
		called := false
		r.Fallback = func(context.Context) (Session, string, error) { called = true; return &fakeSession{}, "x", nil }
		if err := r.Run(context.Background(), rec); err == nil || called {
			t.Errorf("err = %v, fell back %v", err, called)
		}
	})

	t.Run("the conversation could not be loaded: the new one gets the context", func(t *testing.T) {
		r, _, _, _ := newTestRunner(t)
		first := &failingSession{errs: []error{errors.New(busy)}}
		fresh := &freshFake{}
		r.StartSession = func(context.Context) (Session, error) { return first, nil }
		r.Fallback = func(context.Context) (Session, string, error) { return fresh, "gemini-3.6-flash", nil }
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if len(fresh.prompts) != 1 || !strings.HasPrefix(fresh.prompts[0], "RULES") {
			t.Errorf("a fresh conversation was sent %q", fresh.prompts)
		}
	})
}

type freshFake struct{ fakeSession }

func (*freshFake) Fresh() bool { return true }

func TestEngineUnavailable(t *testing.T) {
	for msg, want := range map[string]bool{
		busy: true,
		"prompt failed: RPC error [429]: RESOURCE_EXHAUSTED": true,
		// The model's API out of reach (#14849's care-ci, twice).
		"prompt failed: RPC error [500]: exception TypeError: fetch failed sending request": true,
		// The streamed answer cut off (#15013's 13:50 fix run).
		"prompt failed: RPC error [500]: Incomplete JSON segment at the end": true,
		"prompt failed: RPC error [500]: internal error in the tool":         false,
		"prompt failed: the agent stopped":                                   false,
		"exited 1":                                                           false,
	} {
		if got := EngineUnavailable(errors.New(msg)); got != want {
			t.Errorf("EngineUnavailable(%q) = %v", msg, got)
		}
	}
	if EngineUnavailable(nil) {
		t.Error("nil")
	}
}

func TestEngineTransient(t *testing.T) {
	for msg, want := range map[string]bool{
		"prompt failed: RPC error [500]: exception TypeError: fetch failed sending request": true,
		"prompt failed: RPC error [500]: Incomplete JSON segment at the end":                true,
		"read ECONNRESET": true,
		// The engine's connection dropped (KCC #15081), or the turn's end
		// was lost from the transcript.
		"agent: prompt failed: connection to agent closed: bufio.Scanner: token too long": true,
		errTurnLost.Error(): true,
		busy:                false,
		"prompt failed: RPC error [429]: RESOURCE_EXHAUSTED": false,
		"prompt failed: the agent stopped":                   false,
	} {
		if got := EngineTransient(errors.New(msg)); got != want {
			t.Errorf("EngineTransient(%q) = %v", msg, got)
		}
	}
}

const cutOff = "prompt failed: RPC error [500]: Incomplete JSON segment at the end"

// A turn whose exchange with the model broke (the answer cut off, the
// request never sent) is asked again of the same model, in the same
// conversation reopened, told it was interrupted - twice, 30s then 2m
// apart - before the run falls back to the next model. A model at capacity
// falls back at once.
func TestRunnerRetriesABrokenExchangeOnTheSameModel(t *testing.T) {
	rec, err := Parse([]byte(`
name: demo
context: RULES
start:
  steps:
    - ask: fix issue {{ .Inputs.issue }}
`))
	if err != nil {
		t.Fatal(err)
	}
	setup := func(t *testing.T, first Session, reopened []Session) (*Runner, *[]time.Duration, *bool, *bytes.Buffer) {
		r, _, _, _ := newTestRunner(t)
		var slept []time.Duration
		fellBack := false
		var log bytes.Buffer
		r.Log = &log
		r.StartSession = func(context.Context) (Session, error) { return first, nil }
		r.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
		r.Reopen = func(context.Context) (Session, error) {
			s := reopened[0]
			reopened = reopened[1:]
			return s, nil
		}
		r.Fallback = func(context.Context) (Session, string, error) {
			fellBack = true
			return &fakeSession{}, "gemini-3.6-flash", nil
		}
		return r, &slept, &fellBack, &log
	}

	t.Run("asked again, the same model answers", func(t *testing.T) {
		first := &failingSession{errs: []error{errors.New(cutOff)}}
		again := &fakeSession{}
		r, slept, fellBack, log := setup(t, first, []Session{again})
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if *fellBack || !first.closed {
			t.Errorf("fell back %v, first closed %v", *fellBack, first.closed)
		}
		if len(again.prompts) != 1 || !strings.Contains(again.prompts[0], "interrupted by a connection error") || !strings.HasSuffix(again.prompts[0], "fix issue #7") || strings.Contains(again.prompts[0], "RULES") || strings.Contains(again.prompts[0], "different model") {
			t.Errorf("asked again with %q", again.prompts)
		}
		if len(*slept) != 1 || (*slept)[0] != 30*time.Second {
			t.Errorf("slept %v", *slept)
		}
		if !strings.Contains(log.String(), "asking the same model again in 30s (1 of 2)") {
			t.Errorf("log:\n%s", log.String())
		}
	})

	t.Run("still broken after two tries: the next model", func(t *testing.T) {
		first := &failingSession{errs: []error{errors.New(cutOff)}}
		r, slept, fellBack, _ := setup(t, first, []Session{
			&failingSession{errs: []error{errors.New("prompt failed: RPC error [500]: exception TypeError: fetch failed sending request")}},
			&failingSession{errs: []error{errors.New(cutOff)}},
		})
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if !*fellBack || len(*slept) != 2 || (*slept)[0] != 30*time.Second || (*slept)[1] != 2*time.Minute {
			t.Errorf("fell back %v, slept %v", *fellBack, *slept)
		}
	})

	t.Run("at capacity: the next model at once", func(t *testing.T) {
		first := &failingSession{errs: []error{errors.New(busy)}}
		r, slept, fellBack, _ := setup(t, first, nil)
		if err := r.Run(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if !*fellBack || len(*slept) != 0 {
			t.Errorf("fell back %v, slept %v", *fellBack, *slept)
		}
	})

	t.Run("no Reopen: a broken exchange falls back", func(t *testing.T) {
		first := &failingSession{errs: []error{errors.New(cutOff)}}
		r, _, fellBack, _ := setup(t, first, nil)
		r.Reopen = nil
		if err := r.Run(context.Background(), rec); err != nil || !*fellBack {
			t.Errorf("err %v, fell back %v", err, *fellBack)
		}
	})
}
