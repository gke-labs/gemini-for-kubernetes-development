package recipe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// erringSession fails every turn with err.
type erringSession struct {
	fakeSession
	err error
}

func (s *erringSession) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return "", s.err
}

// A turn the model refused because one tool output was over its token
// limit (KCC 14014's fix, 2026-10-16) fails again however often it runs:
// the task records that it needs a person. Any other failure does not.
func TestRunnerRecordsAFailureThatNeedsAPerson(t *testing.T) {
	rec, err := Parse([]byte("name: demo\nstart:\n  steps:\n    - ask: fix it\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"over the token limit": {errors.New(`agent: prompt failed: RPC error [400]: {"error":{"message":"The input token count exceeds the maximum number of tokens allowed 1048576."}}`), "step 1:ask: the agent's input went over the model's token limit"},
		"anything else":        {errors.New("agent: prompt failed: RPC error [503]: unavailable"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _, _ := newTestRunner(t)
			r.StartSession = func(context.Context) (Session, error) { return &erringSession{err: tc.err}, nil }
			if err := r.Run(context.Background(), rec); err == nil {
				t.Fatal("the run did not fail")
			}
			data, err := os.ReadFile(filepath.Join(r.TaskDir, spool.RequiresHumanFile))
			switch {
			case tc.want == "" && !errors.Is(err, os.ErrNotExist):
				t.Errorf("recorded %q, want nothing", data)
			case tc.want != "" && !strings.HasPrefix(string(data), tc.want):
				t.Errorf("recorded %q (%v), want %q…", data, err, tc.want)
			}
		})
	}
}

// A turn the engine cut off at its limit of model turns for one request
// (KCC 12959 and 13090's fixes, 2026-10-20) fails the step as the agent
// running out of turns, says so, and records the stop reason for the
// task's status; it does not need a person (a new run may well finish).
// Another stop reason is recorded too, and an error with none is not.
func TestRunnerRecordsAnAgentOutOfTurns(t *testing.T) {
	rec, err := Parse([]byte("name: demo\nstart:\n  steps:\n    - ask: fix it\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		err      error
		stop     string
		failText string
	}{
		"out of turns": {&StopError{Reason: StopMaxTurnRequests}, "step 1:ask: max_turn_requests", "ran out of turns (max_turn_requests)"},
		"refused":      {&StopError{Reason: "refusal"}, "step 1:ask: refusal", "turn stopped: refusal"},
		"not a stop":   {errors.New("agent: prompt failed: RPC error [503]: unavailable"), "", "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _, _ := newTestRunner(t)
			r.StartSession = func(context.Context) (Session, error) { return &erringSession{err: tc.err}, nil }
			err := r.Run(context.Background(), rec)
			if err == nil || !strings.Contains(err.Error(), tc.failText) {
				t.Fatalf("run error = %v, want %q in it", err, tc.failText)
			}
			data, rerr := os.ReadFile(filepath.Join(r.TaskDir, spool.StopReasonFile))
			switch {
			case tc.stop == "" && !errors.Is(rerr, os.ErrNotExist):
				t.Errorf("recorded stop reason %q, want none", data)
			case tc.stop != "" && strings.TrimSpace(string(data)) != tc.stop:
				t.Errorf("recorded stop reason %q (%v), want %q", data, rerr, tc.stop)
			}
			if _, err := os.Stat(filepath.Join(r.TaskDir, spool.RequiresHumanFile)); !errors.Is(err, os.ErrNotExist) {
				t.Error("an agent out of turns was marked as needing a person")
			}
		})
	}
}
