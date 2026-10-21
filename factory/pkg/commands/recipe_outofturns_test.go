package commands

import (
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// A failed task's error names an agent that ran out of turns, and a task
// that needs a person, each when it is so.
func TestNeedsAPersonNamesOutOfTurns(t *testing.T) {
	if got := needsAPerson(spool.Entry{}); got != "" {
		t.Errorf("plain failure: %q", got)
	}
	got := needsAPerson(spool.Entry{StopReason: "step 8:ask: max_turn_requests"})
	if !strings.Contains(got, "ran out of turns (step 8:ask: max_turn_requests)") {
		t.Errorf("out of turns: %q", got)
	}
	got = needsAPerson(spool.Entry{StopReason: "step 8:ask: refusal", RequiresHuman: "step 8:ask: too big"})
	if strings.Contains(got, "ran out of turns") || !strings.Contains(got, "needs a person: step 8:ask: too big") {
		t.Errorf("refusal, needing a person: %q", got)
	}
}
