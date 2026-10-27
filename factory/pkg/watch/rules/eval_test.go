package rules

import (
	"strings"
	"testing"
	"time"
)

var (
	now     = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	testEnv = Env{
		Trigger:  "overseer",
		Prefixes: []string{"overseer"},
		Watcher:  "argus-watcher-bot",
		Pool:     []string{"ada-coder-bot", "lovelace-coder-bot", "argus-watcher-bot"},
		PRLabels: []string{"ai-generated"},
		Now:      now,
	}
)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// decisions is the prebaked rules' decisions for an item, a line each.
func decisions(t *testing.T, it Item, env Env) []string {
	t.Helper()
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range set.Decide(it, env) {
		out = append(out, d.Rule+" "+d.Outcome)
	}
	return out
}

func issue(mod func(*Item)) Item {
	it := Item{Kind: OnIssue, Number: 13173, Labels: []string{"overseer", "priority/high"}, Author: "someone", UpdatedAt: ago(time.Hour)}
	if mod != nil {
		mod(&it)
	}
	return it
}

func pr(mod func(*Item)) Item {
	m := true
	it := Item{
		Kind: OnPR, Number: 14055, Labels: []string{"overseer"}, Author: "ada-coder-bot",
		Assignees: []string{"ada-coder-bot"}, Head: "h2", Mergeable: &m, Checks: "green",
		HeadChangedAt: ago(3 * time.Hour),
	}
	if mod != nil {
		mod(&it)
	}
	return it
}

func TestDecideIssues(t *testing.T) {
	fixed := func(d time.Duration, failed bool) PastRun {
		return PastRun{Rule: "fix", StartedAt: ago(d + time.Hour), EndedAt: ago(d), Failed: failed}
	}
	for _, tc := range []struct {
		name string
		it   Item
		want string
	}{
		{"labelled", issue(nil), "fix run"},
		{"opened by the watcher, unlabelled (adoption)", issue(func(it *Item) { it.Labels, it.Author = nil, "argus-watcher-bot" }), "fix run"},
		{"assigned to the pool, unlabelled", issue(func(it *Item) { it.Labels, it.Assignees = nil, []string{"lovelace-coder-bot"} }), "fix run"},
		{"an open PR closes it (13173 after #14055)", issue(func(it *Item) { it.LinkedPR = "open" }), ""},
		{"stopped", issue(func(it *Item) { it.Labels = append(it.Labels, "overseer/stop") }), ""},
		{"names a workflow it cannot load: stopped, not stepped or fixed", issue(func(it *Item) { it.Workflow, it.WorkflowMissing = "gone", true }), "workflow-missing run"},
		{"names a workflow, an open PR closes it: held", issue(func(it *Item) { it.Workflow, it.LinkedPR = "kcc-example", "open" }), ""},
		{"fixed 5m ago: cooldown", issue(func(it *Item) { it.Runs = []PastRun{fixed(5*time.Minute, false)}; it.UpdatedAt = ago(time.Minute) }), ""},
		{"fixed 15m ago, updated since", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, false)}; it.UpdatedAt = ago(time.Minute) }), "fix run"},
		{"fixed 15m ago, not updated", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, false)} }), ""},
		{"failed 25m ago, not updated: retried", issue(func(it *Item) { it.Runs = []PastRun{fixed(25*time.Minute, true)}; it.UpdatedAt = ago(2 * time.Hour) }), "fix run"},
		{"failed 15m ago: failure cooldown", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, true)} }), ""},
		{"fan-out parent", issue(func(it *Item) { it.FanoutParent = true }), ""},
		{"a fan-out child, created when it is to be worked on, unlabelled (no triage)", issue(func(it *Item) { it.Labels, it.Author = nil, "argus-watcher-bot" }), "fix run"},
		{"names a workflow", issue(func(it *Item) { it.Workflow = "kcc-greenfield" }), "workflow run"},
		{"workflow inside its definition's cooldown", issue(func(it *Item) {
			it.Workflow, it.WorkflowCooldown = "kcc-greenfield", time.Hour
			it.Runs = []PastRun{{Rule: "workflow", StartedAt: ago(40 * time.Minute), EndedAt: ago(30 * time.Minute)}}
		}), ""},
		{"workflow after the default cooldown", issue(func(it *Item) {
			it.Workflow = "kcc-greenfield"
			it.Runs = []PastRun{{Rule: "workflow", StartedAt: ago(20 * time.Minute), EndedAt: ago(15 * time.Minute)}}
		}), "workflow run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(decisions(t, tc.it, testEnv), ", "); got != tc.want {
				t.Errorf("decisions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideFixInputs(t *testing.T) {
	set, _ := Builtin()
	ds := set.Decide(issue(nil), testEnv)
	if len(ds) != 1 || ds[0].Recipe != "fix" || ds[0].Apply != "open-pr" {
		t.Fatalf("decisions = %v", ds)
	}
	if got := ds[0].Inputs["labels"]; got != "overseer,ai-generated,priority/high" {
		t.Errorf("labels = %q", got)
	}
	if ds[0].Inputs["draft"] != "false" {
		t.Errorf("inputs = %v", ds[0].Inputs)
	}
}

func TestDecidePRs(t *testing.T) {
	ciRun := func(start time.Duration, head string, failed bool) PastRun {
		return PastRun{Rule: "ci", StartedAt: ago(start), EndedAt: ago(start - 10*time.Minute), Head: head, Failed: failed}
	}
	for _, tc := range []struct {
		name string
		it   Item
		want string
	}{
		{"green, nothing wanted: ready", pr(nil), "ready run"},
		{"already ready: nothing to write", pr(func(it *Item) { it.Labels = append(it.Labels, "overseer/ready-for-human"); it.Assignees = nil }), ""},
		{"failing checks", pr(func(it *Item) { it.Checks = "failing" }), "ci run"},
		{"feedback and failing checks: comments first", pr(func(it *Item) { it.Checks, it.NewTrustedFeedback = "failing", true }), "comments run"},
		{"conflicting", pr(func(it *Item) { m := false; it.Mergeable = &m }), "conflict run"},
		{"a task running: the group waits", pr(func(it *Item) { it.Checks, it.TaskActive = "failing", true }), ""},
		{"assigned only", pr(func(it *Item) { it.Labels, it.Checks = nil, "failing" }), "ci run"},
		{"ci 3 times since the head: give up", pr(func(it *Item) {
			it.Checks = "failing"
			it.Runs = []PastRun{ciRun(150*time.Minute, "h2", false), ciRun(100*time.Minute, "h2", false), ciRun(50*time.Minute, "h2", false)}
		}), "ci give-up"},
		{"ci 3 times, a human spoke since", pr(func(it *Item) {
			it.Checks, it.HumanSpokeAt = "failing", ago(20*time.Minute)
			it.Runs = []PastRun{ciRun(150*time.Minute, "h2", false), ciRun(100*time.Minute, "h2", false), ciRun(50*time.Minute, "h2", false)}
		}), "ci run"},
		{"ci 3 times, put on hold and let go since", pr(func(it *Item) {
			it.Checks, it.PausedAt = "failing", ago(30*time.Minute)
			it.Runs = []PastRun{ciRun(150*time.Minute, "h2", false), ciRun(100*time.Minute, "h2", false), ciRun(50*time.Minute, "h2", false)}
		}), "ci run"},
		{"ci 1h ago on this head, unassigned: cooldown", pr(func(it *Item) {
			it.Checks, it.Assignees = "failing", nil
			it.Runs = []PastRun{ciRun(70*time.Minute, "h2", false)}
		}), ""},
		{"ci 1h ago on the last head: new head runs", pr(func(it *Item) {
			it.Checks, it.Assignees = "failing", nil
			it.Runs = []PastRun{ciRun(70*time.Minute, "h1", false)}
		}), "ci run"},
		{"ci 1h ago, assigned to the pool: no cooldown", pr(func(it *Item) {
			it.Checks = "failing"
			it.Runs = []PastRun{ciRun(70*time.Minute, "h2", false)}
		}), "ci run"},
		{"ci failed 1h ago: retried", pr(func(it *Item) {
			it.Checks, it.Assignees = "failing", nil
			it.Runs = []PastRun{ciRun(70*time.Minute, "h2", true)}
		}), "ci run"},
		{"ci failed 15m ago: failure cooldown", pr(func(it *Item) {
			it.Checks, it.Assignees = "failing", nil
			it.Runs = []PastRun{ciRun(25*time.Minute, "h2", true)}
		}), ""},
		{"ci refused 3 times since the head: no attempt, no give-up", pr(func(it *Item) {
			it.Checks = "failing"
			for _, d := range []time.Duration{150, 100, 50} {
				r := ciRun(d*time.Minute, "h2", true)
				r.NotRun = true
				it.Runs = append(it.Runs, r)
			}
		}), "ci run"},
		{"comments failed on this head: retried", pr(func(it *Item) {
			it.Runs = []PastRun{{Rule: "comments", StartedAt: ago(time.Hour), EndedAt: ago(50 * time.Minute), Head: "h2", Failed: true}}
		}), "comments run"},
		{"comments failed on the last head: not retried", pr(func(it *Item) {
			it.Runs = []PastRun{{Rule: "comments", StartedAt: ago(time.Hour), EndedAt: ago(50 * time.Minute), Head: "h1", Failed: true}}
		}), "ready run"},
		{"*/review wanted: review, not ready", pr(func(it *Item) { it.Labels = append(it.Labels, "overseer/review") }), "review run"},
		{"*/review on a closing issue: inherited, then review", pr(func(it *Item) {
			it.Closes = []Closing{{Number: 13173, Labels: []string{"overseer", "overseer/review"}}}
		}), "inherit-labels run, review run"},
		{"reviewed at head: ready", pr(func(it *Item) {
			it.Labels, it.ReviewedAtHead = append(it.Labels, "overseer/review"), true
		}), "ready run"},
		{"approved, */review: no review, and not ready", pr(func(it *Item) {
			it.Labels, it.Approved = append(it.Labels, "overseer/review"), true
		}), ""},
		{"stopped", pr(func(it *Item) { it.Checks, it.Labels = "failing", append(it.Labels, "overseer/stop") }), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(decisions(t, tc.it, testEnv), ", "); got != tc.want {
				t.Errorf("decisions = %q, want %q", got, tc.want)
			}
		})
	}
}

// ready's writes: the ready label in the deployment's spelling, */review
// off in either, the closing issues' humans on, the pool's bot off.
func TestDecideReadyWrites(t *testing.T) {
	env := testEnv
	env.Trigger, env.Prefixes = "kcc", []string{"overseer", "kcc"}
	it := pr(func(it *Item) {
		it.Labels = []string{"kcc", "overseer/review", "kcc/review"}
		it.ReviewedAtHead = true
		it.Closes = []Closing{{Number: 1, Labels: []string{"kcc"}, Assignees: []string{"maintainer", "ada-coder-bot"}}}
	})
	set, _ := Builtin()
	var ready *Decision
	for _, d := range set.Decide(it, env) {
		if d.Rule == "ready" {
			ready = &d
		}
	}
	if ready == nil {
		t.Fatal("ready did not decide")
	}
	w := ready.Writes
	if strings.Join(w.Label, ",") != "kcc/ready-for-human" || strings.Join(w.Unlabel, ",") != "overseer/review,kcc/review" ||
		strings.Join(w.Assign, ",") != "maintainer" || strings.Join(w.Unassign, ",") != "ada-coder-bot" {
		t.Errorf("writes = %+v", w)
	}
}

func TestDecideGiveUpWrites(t *testing.T) {
	set, _ := Builtin()
	it := pr(func(it *Item) {
		it.NewTrustedFeedback = true
		for i := range 3 {
			start := time.Duration(150-50*i) * time.Minute
			it.Runs = append(it.Runs, PastRun{Rule: "comments", StartedAt: ago(start), EndedAt: ago(start - time.Minute), Head: "h2", Failed: true})
		}
	})
	ds := set.Decide(it, testEnv)
	if len(ds) != 1 || ds[0].Outcome != OutcomeGiveUp || !ds[0].Writes.Stop || !strings.Contains(ds[0].Writes.Comment, "Pausing after 3 attempts") {
		t.Errorf("decisions = %v", ds)
	}
}

// inactive is on but needs the overseer's timeout: without one it decides
// nothing; with one, it pauses a PR nobody touched for that long. Turned
// off by a repository, it decides nothing either way.
func TestDecideInactive(t *testing.T) {
	stale := pr(func(it *Item) { it.LastTrustedActivity = ago(200 * time.Hour); it.Checks = "pending" })
	if got := decisions(t, stale, testEnv); len(got) != 0 {
		t.Errorf("no timeout set: %v", got)
	}
	env := testEnv
	env.PRInactivityTimeout = 168 * time.Hour
	if got := strings.Join(decisions(t, stale, env), ","); got != "inactive run" {
		t.Errorf("decisions = %v", got)
	}
	base, _ := Builtin()
	off := false
	set, err := Overlay(base, []*Rule{{Name: "inactive", Enabled: &off}})
	if err != nil {
		t.Fatal(err)
	}
	if ds := set.Decide(stale, env); len(ds) != 0 {
		t.Errorf("turned off: %v", ds)
	}
}

// A PR already handed to the humans that carries */review again loses it,
// once the automated reviewer reviewed the head without asking for
// changes; nothing else is written.
func TestDecideReadyUnreview(t *testing.T) {
	ready := func(mod func(*Item)) Item {
		return pr(func(it *Item) {
			it.Labels = []string{"overseer", "overseer/ready-for-human", "overseer/review"}
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
		{"reviewed at head", ready(func(it *Item) { it.ReviewedAtHead = true }), "ready-unreview run"},
		{"not reviewed yet: review", ready(nil), "review run"},
		{"review asked for changes", ready(func(it *Item) { it.ReviewedAtHead, it.ChangesRequested = true, true }), ""},
		{"ready without */review", pr(func(it *Item) { it.Labels = []string{"overseer", "overseer/ready-for-human"} }), ""},
		{"not ready, reviewed but changes asked", pr(func(it *Item) {
			it.Labels = []string{"overseer", "overseer/review"}
			it.ReviewedAtHead, it.ChangesRequested = true, true
		}), ""},
		{"in the merge queue", pr(func(it *Item) { it.MergeQueue = true }), ""},
		{"opened by someone outside the pool", pr(func(it *Item) { it.Author = "contributor" }), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(decisions(t, tc.it, testEnv), ", "); got != tc.want {
				t.Errorf("decisions = %q, want %q", got, tc.want)
			}
		})
	}
}

// inherit-labels writes the closing issues' labels the PR lacks, never
// ready-for-human; on a PR already handed to the humans, everything but
// */review too (an issue's stop label reaches it; bot reviews are not set
// going again).
func TestInheritLabelsOnReadyPRs(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	closes := []Closing{{Number: 19, Labels: []string{"overseer", "overseer/review", "overseer/stop", "overseer/ready-for-human", "priority/high"}}}
	writes := func(labels []string) []string {
		for _, d := range set.Decide(pr(func(it *Item) { it.Labels, it.Closes = labels, closes }), testEnv) {
			if d.Rule == "inherit-labels" {
				return d.Writes.Label
			}
		}
		return nil
	}
	if got := strings.Join(writes([]string{"overseer"}), ","); got != "overseer/review,overseer/stop,priority/high" {
		t.Errorf("a PR not yet ready inherits %q", got)
	}
	if got := strings.Join(writes([]string{"overseer", "overseer/ready-for-human"}), ","); got != "overseer/stop,priority/high" {
		t.Errorf("a ready PR inherits %q, want the stop label and priority, not review", got)
	}
}
