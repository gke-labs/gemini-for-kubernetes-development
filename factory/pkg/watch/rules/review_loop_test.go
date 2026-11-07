package rules

import (
	"strings"
	"testing"
	"time"
)

// review-loop is off until a repository turns it on; on, it takes a
// grouped graph's draft PR that wants a review before review does, once
// per head: not again on the head it ran on, once more on the head its
// own push left.
func TestReviewLoopRule(t *testing.T) {
	base, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if r := base.Named("review-loop"); r == nil || r.IsEnabled() {
		t.Fatal("review-loop is not a built-in rule that is off")
	}
	on := true
	set, err := Overlay(base, []*Rule{{Name: "review-loop", Enabled: &on}})
	if err != nil {
		t.Fatal(err)
	}
	loop := func(head string, failed bool) PastRun {
		return PastRun{Rule: "review-loop", StartedAt: ago(time.Hour), EndedAt: ago(30 * time.Minute), Head: head, Failed: failed}
	}
	step := func(mod func(*Item)) Item {
		return pr(func(it *Item) {
			it.Draft = true
			it.Labels = append(it.Labels, "overseer/review")
			it.Closes = []Closing{{Number: 13173, Labels: []string{"overseer", "overseer/review"}}}
			if mod != nil {
				mod(it)
			}
		})
	}
	for _, tc := range []struct {
		name string
		it   Item
		want string
	}{
		{"a step's draft PR: the loop, not review", step(nil), "review-loop run"},
		{"checks pending: the loop does not wait", step(func(it *Item) { it.Checks = "pending" }), "review-loop run"},
		{"checks failing: ci first", step(func(it *Item) { it.Checks = "failing" }), "ci run"},
		{"it ran on the head: review", step(func(it *Item) { it.Runs = []PastRun{loop("h2", false)} }), "review run"},
		{"a new head since (its own push, or anyone's): the loop again", step(func(it *Item) { it.Runs = []PastRun{loop("h1", false)} }), "review-loop run"},
		{"it failed on the head: again", step(func(it *Item) { it.Runs = []PastRun{loop("h2", true)} }), "review-loop run"},
		{"not a draft: review", step(func(it *Item) { it.Draft = false }), "review run"},
		{"no review wanted: nothing", step(func(it *Item) {
			it.Labels = []string{"overseer"}
			it.Closes = []Closing{{Number: 13173, Labels: []string{"overseer"}}}
		}), ""},
		{"reviewed at head: neither", step(func(it *Item) { it.ReviewedAtHead = true }), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, d := range set.Decide(tc.it, testEnv) {
				if d.Rule != "ready" {
					got = append(got, d.Rule+" "+d.Outcome)
				}
			}
			if strings.Join(got, ", ") != tc.want {
				t.Errorf("decisions = %q, want %q", strings.Join(got, ", "), tc.want)
			}
		})
	}
	if why, _ := set.Held("review-loop", step(func(it *Item) { it.Runs = []PastRun{loop("h2", false)} }), testEnv); why != "dedupe: head: it already ran on h2" {
		t.Errorf("held %q", why)
	}
}
