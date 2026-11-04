package taskapi

import (
	"strings"
	"testing"
)

// The last step a task's log says failed ("::failed <step>: <error>", as
// the recipe runner prints it) is what a failed task's error carries, not
// its exit status alone: a board row read "care-comments failed: running
// recipe: task failed with exit code 1", the 503 behind it lost.
func TestFailedStep(t *testing.T) {
	log := "::step 2:run\nok\n::step 3:ask (ask)\n" +
		"::failed 3:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand.\n" +
		"Error: step 3:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand.\n"
	// Arriving in pieces that split lines anywhere.
	for _, size := range []int{1, 7, 64, len(log)} {
		var f failedStep
		for i := 0; i < len(log); i += size {
			f.observe([]byte(log[i:min(i+size, len(log))]))
		}
		if got, want := f.reason(), "3:ask: agent: prompt failed: RPC error [503]: This model is currently experiencing high demand."; got != want {
			t.Errorf("chunks of %d: reason %q, want %q", size, got, want)
		}
	}

	var later failedStep
	later.observe([]byte("::failed 2:run: exited 1\n::failed 9:push: no commit on top of abc\n"))
	if later.reason() != "9:push: no commit on top of abc" {
		t.Errorf("the last failure: %q", later.reason())
	}
	var none failedStep
	none.observe([]byte("all fine\nError: something else\n"))
	if none.reason() != "" {
		t.Errorf("no failed step: %q", none.reason())
	}
	var long failedStep
	long.observe([]byte("::failed 3:ask: " + strings.Repeat("x", 1000) + "\n"))
	if r := long.reason(); len(r) > maxFailedReason+len("…") || !strings.HasSuffix(r, "…") {
		t.Errorf("not clipped: %d chars", len(r))
	}
	var unfinished failedStep
	unfinished.observe([]byte("::failed 3:ask: cut off without a newline"))
	if unfinished.reason() != "" {
		t.Errorf("a line not yet ended counted: %q", unfinished.reason())
	}
}
