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
	yes = true
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
		{"none of the overseer's", issue(func(it *Item) { it.Labels = []string{"bug"} }), ""},
		{"an open PR closes it (13173 after #14055)", issue(func(it *Item) { it.LinkedPR = "open" }), ""},
		{"stopped", issue(func(it *Item) { it.Labels = append(it.Labels, "overseer/stop") }), ""},
		{"fixed 5m ago: cooldown", issue(func(it *Item) { it.Runs = []PastRun{fixed(5*time.Minute, false)}; it.UpdatedAt = ago(time.Minute) }), ""},
		{"fixed 15m ago, updated since", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, false)}; it.UpdatedAt = ago(time.Minute) }), "fix run"},
		{"fixed 15m ago, not updated", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, false)} }), ""},
		{"failed 25m ago, not updated: retried", issue(func(it *Item) { it.Runs = []PastRun{fixed(25*time.Minute, true)}; it.UpdatedAt = ago(2 * time.Hour) }), "fix run"},
		{"failed 15m ago: failure cooldown", issue(func(it *Item) { it.Runs = []PastRun{fixed(15*time.Minute, true)} }), ""},
		{"fan-out parent", issue(func(it *Item) { it.FanoutParent = true }), ""},
		{"fan-out child not released", issue(func(it *Item) { it.FanoutChild, it.Labels, it.Author = true, nil, "argus-watcher-bot" }), ""},
		{"fan-out child released", issue(func(it *Item) { it.FanoutChild = true }), "fix run"},
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
		{"neither labelled nor assigned", pr(func(it *Item) { it.Labels, it.Assignees = nil, nil }), ""},
		{"assigned only", pr(func(it *Item) { it.Labels, it.Checks = nil, "failing" }), "ci run"},
		{"ci 3 times since the head: give up", pr(func(it *Item) {
			it.Checks = "failing"
			it.Runs = []PastRun{ciRun(150*time.Minute, "h2", false), ciRun(100*time.Minute, "h2", false), ciRun(50*time.Minute, "h2", false)}
		}), "ci give-up"},
		{"ci 3 times, a human spoke since", pr(func(it *Item) {
			it.Checks, it.HumanSpokeAt = "failing", ago(20*time.Minute)
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
			it.Runs = append(it.Runs, PastRun{Rule: "comments", StartedAt: ago(start), EndedAt: ago(start - time.Minute), Head: "h2"})
		}
	})
	ds := set.Decide(it, testEnv)
	if len(ds) != 1 || ds[0].Outcome != OutcomeGiveUp || !ds[0].Writes.Stop || !strings.Contains(ds[0].Writes.Comment, "Pausing after 3 attempts") {
		t.Errorf("decisions = %v", ds)
	}
}

func TestDecideScheduleAndEvent(t *testing.T) {
	if got := decisions(t, Item{Kind: OnSchedule, Name: "weekly", Due: true}, testEnv); strings.Join(got, ",") != "chore run" {
		t.Errorf("due schedule = %v", got)
	}
	if got := decisions(t, Item{Kind: OnSchedule, Name: "weekly"}, testEnv); len(got) != 0 {
		t.Errorf("schedule not due = %v", got)
	}
	set, _ := Builtin()
	ds := set.Decide(Item{Kind: OnEvent, Number: 13244, Event: "linked-issue-closed"}, testEnv)
	if len(ds) != 1 || ds[0].Rule != "nudge" || !ds[0].SkipCooldown {
		t.Errorf("event = %v", ds)
	}
}

// The off rules decide nothing until something turns them on; turned on,
// inactive pauses a PR nobody touched for the overseer's timeout.
func TestDecideOffRulesAndSettings(t *testing.T) {
	stale := pr(func(it *Item) { it.LastTrustedActivity = ago(200 * time.Hour); it.Checks = "pending" })
	if got := decisions(t, stale, testEnv); len(got) != 0 {
		t.Errorf("inactive is off, got %v", got)
	}
	base, _ := Builtin()
	set, err := Overlay(base, []*Rule{{Name: "inactive", Enabled: &yes}})
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv
	if ds := set.Decide(stale, env); len(ds) != 0 {
		t.Errorf("no timeout set: %v", ds)
	}
	env.PRInactivityTimeout = 168 * time.Hour
	ds := set.Decide(stale, env)
	if len(ds) != 1 || ds[0].Rule != "inactive" || !ds[0].Writes.Stop {
		t.Errorf("decisions = %v", ds)
	}
}
