package rules

import (
	"strings"
	"testing"
	"time"
)

// step finds a rule's step in an explanation.
func step(t *testing.T, ex Explanation, rule string) Step {
	t.Helper()
	for _, st := range ex.Steps {
		if st.Rule == rule {
			return st
		}
	}
	t.Fatalf("no step for %s in %+v", rule, ex.Steps)
	return Step{}
}

// Explain says, rule by rule, what each decides or why not, and its
// decisions are Decide's.
func TestExplain(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}

	// Failing checks and new feedback: comments takes the PR, and the rules
	// that take it after comments are skipped.
	both := pr(func(it *Item) { it.Checks, it.NewTrustedFeedback = "failing", true })
	ex := set.Explain(both, testEnv)
	if st := step(t, ex, "comments"); st.Decision == nil || st.Decision.Outcome != OutcomeRun {
		t.Errorf("comments = %+v, want a run", st)
	}
	if st := step(t, ex, "ci"); st.Skipped != "comments took the item" {
		t.Errorf("ci = %+v, want skipped for comments", st)
	}
	if st := step(t, ex, "adopt-pr"); st.Skipped != "off" {
		t.Errorf("adopt-pr = %+v, want off", st)
	}

	// ci just ran OK on this head, unassigned: held by its cooldown.
	ciRun := func(start time.Duration, head string, failed bool) PastRun {
		return PastRun{Rule: "ci", StartedAt: ago(start), EndedAt: ago(start), Failed: failed, Head: head}
	}
	cooling := pr(func(it *Item) {
		it.Checks, it.Assignees = "failing", nil
		it.Runs = []PastRun{ciRun(70*time.Minute, "h2", false)}
	})
	ex = set.Explain(cooling, testEnv)
	if st := step(t, ex, "ci"); st.Held != "cooldown: 50m left of 2h after a success" {
		t.Errorf("ci held = %q", st.Held)
	}
	// conflict, before ci, fails: each predicate with what the PR has.
	if st := step(t, ex, "conflict"); len(st.Failed) != 1 || st.Failed[0] != "mergeable: false (has mergeable true)" {
		t.Errorf("conflict failed = %q", st.Failed)
	}

	// A task of the PR active: the rules that take it are skipped.
	active := pr(func(it *Item) { it.Checks, it.TaskActive = "failing", true })
	if st := step(t, set.Explain(active, testEnv), "ci"); st.Skipped != "a task of the item is queued or running" {
		t.Errorf("ci = %+v", st)
	}

	// The stop label: a gate, every rule skipped.
	stopped := pr(func(it *Item) { it.Labels = append(it.Labels, "overseer/stop") })
	if ex := set.Explain(stopped, testEnv); ex.Gate != "has the stop label" || !strings.HasPrefix(step(t, ex, "ci").Skipped, "the item has the stop label") {
		t.Errorf("stopped = %+v", ex)
	}

	// Closed, or below the overseer's minNumber: gates too.
	closed := pr(func(it *Item) { it.Checks, it.Closed = "failing", true })
	if ex := set.Explain(closed, testEnv); ex.Gate != "is closed" || len(ex.Decisions()) != 0 {
		t.Errorf("closed = %+v", ex)
	}
	floor := testEnv
	floor.MinNumber = 14000
	if ex := set.Explain(issue(nil), floor); ex.Gate != "is numbered below the overseer's minNumber" {
		t.Errorf("below minNumber = %+v", ex)
	}

	// Decide is Explain's decisions, on every item above.
	for _, it := range []Item{both, cooling, active, stopped, closed, issue(nil)} {
		var a, b []string
		for _, d := range set.Decide(it, testEnv) {
			a = append(a, d.Rule+" "+d.Outcome)
		}
		for _, d := range set.Explain(it, testEnv).Decisions() {
			b = append(b, d.Rule+" "+d.Outcome)
		}
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Errorf("Decide %v, Explain %v", a, b)
		}
	}
}

func TestWhenString(t *testing.T) {
	f := false
	w := When{Author: "pool", Fanout: &f, AnyOf: []When{{Feedback: "new-trusted"}, {LastRun: "failed"}}}
	if got, want := w.String(), "author: pool, fanout: false, any-of: [{feedback: new-trusted}, {last-run: failed}]"; got != want {
		t.Errorf("String = %q\nwant       %q", got, want)
	}
	if (When{}).String() != "" {
		t.Error("an empty when is not empty")
	}
}

func TestHuman(t *testing.T) {
	for d, want := range map[time.Duration]string{
		36 * time.Minute: "36m", 2 * time.Hour: "2h", 72*time.Minute + 10*time.Second: "1h12m", 10 * time.Second: "under a minute",
	} {
		if got := human(d); got != want {
			t.Errorf("human(%s) = %q, want %q", d, got, want)
		}
	}
}
