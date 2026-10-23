package rules

import (
	"slices"
	"testing"
	"time"
)

// Settled carries the walk as far as the facts read allow: up to the first
// rule a fact not read yet could change, so a rule after it waits whatever
// its own facts, and a rule before it is decided now.
func TestSettled(t *testing.T) {
	base, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := LoadFiles(map[string][]byte{
		"early.yaml": []byte("name: early\non: pr\norder: 3\nwhen: {author: pool, labels: {any: [area/kcc]}}\nrun: {label: [seen]}\n"),
		"late.yaml":  []byte("name: late\non: pr\norder: 45\nwhen: {author: pool}\nrun: {label: [late]}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv
	env.PRInactivityTimeout = 14 * 24 * time.Hour
	mergeable := true
	pr := func(mod func(*Item)) Item {
		it := Item{Kind: OnPR, Number: 30, Labels: []string{"overseer", "area/kcc"}, Author: "ada-coder-bot", Head: "abc", Mergeable: &mergeable,
			Closes:  []Closing{{Number: 7, Labels: []string{"area/kcc", "overseer/review"}}},
			Unknown: []string{FactHistory, FactFeedback, FactChecks}}
		if mod != nil {
			mod(&it)
		}
		return it
	}
	names := func(ds []Decision) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.Rule)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		it   Item
		env  Env
		want []string
	}{
		// Before history: early and inherit-labels; inactive waits on
		// the history, and every rule after it with it.
		{"stage 1", pr(nil), env, []string{"early", "inherit-labels"}},
		// A task active: the same - the care rules are skipped, inactive
		// still waits.
		{"task active", pr(func(it *Item) { it.TaskActive = true }), env, []string{"early", "inherit-labels"}},
		// No inactivity timeout: inactive cannot decide, so it does not
		// hold the walk; comments waits on the feedback.
		{"no timeout", pr(nil), testEnv, []string{"early", "inherit-labels"}},
		// History read, recent activity: inactive settled not deciding,
		// comments waits on the feedback.
		{"stage 2", pr(func(it *Item) {
			it.Unknown = []string{FactFeedback, FactChecks}
			it.LastTrustedActivity, it.HeadChangedAt = ago(time.Hour), ago(time.Hour)
		}), env, []string{"early", "inherit-labels"}},
		// Everything read, green: late and ready (review, with the closing
		// issue's review label, is first).
		{"all read", pr(func(it *Item) {
			it.Unknown = nil
			it.LastTrustedActivity, it.HeadChangedAt = ago(time.Hour), ago(time.Hour)
			it.Checks = "green"
		}), env, []string{"early", "inherit-labels", "review", "late"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(set.Settled(tc.it, tc.env)); !slices.Equal(got, tc.want) {
				t.Errorf("settled %v, want %v", got, tc.want)
			}
		})
	}

	// Inactive due, history read: its stop ends the walk; late never.
	it := pr(func(it *Item) {
		it.Unknown = []string{FactFeedback, FactChecks}
		it.LastTrustedActivity, it.HeadChangedAt = ago(30*24*time.Hour), ago(30*24*time.Hour)
	})
	if got := names(set.Settled(it, env)); !slices.Equal(got, []string{"early", "inherit-labels", "inactive"}) {
		t.Errorf("inactive due: settled %v", got)
	}

	// Deciding on facts not read: nothing turns on one.
	d := set.Decide(pr(nil), env)
	if slices.ContainsFunc(d, func(d Decision) bool { return d.Rule == "inactive" || d.Rule == "ready" || d.Rule == "review" }) {
		t.Errorf("decided on facts not read: %v", names(d))
	}
}

// A give-up counted since the head waits for the history: deciding, the
// rule is held; asking, it could run.
func TestGiveUpWaitsForHistory(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	mergeable := true
	it := Item{Kind: OnPR, Number: 31, Labels: []string{"overseer"}, Author: "ada-coder-bot", Head: "abc", Mergeable: &mergeable,
		Checks: "failing", Unknown: []string{FactHistory},
		Runs: []PastRun{{Rule: "ci", StartedAt: ago(5 * time.Hour), EndedAt: ago(4 * time.Hour)}, {Rule: "ci", StartedAt: ago(3 * time.Hour), EndedAt: ago(2 * time.Hour)}, {Rule: "ci", StartedAt: ago(90 * time.Minute), EndedAt: ago(80 * time.Minute)}}}
	for _, st := range set.Explain(it, testEnv).Steps {
		if st.Rule == "ci" && (st.Decision != nil || st.Held != "give-up: the history is not read") {
			t.Errorf("ci step %+v", st)
		}
	}
	it.Unknown = nil
	it.HeadChangedAt = ago(6 * time.Hour)
	if got := decisions(t, it, testEnv); !slices.Contains(got, "ci give-up") {
		t.Errorf("history read: %v", got)
	}
}
