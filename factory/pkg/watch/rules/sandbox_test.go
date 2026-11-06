package rules

import (
	"strings"
	"testing"
	"time"
)

// The built-in workspace-cleanup rule: an awake sandbox with no task of
// its own and less than 10 GiB free gets the workspace-cleanup recipe,
// asked for 20 GiB; a measured disk with room, a task running, a paused
// sandbox or a disk not measured get nothing; once run, it waits an hour.
func TestSandboxWorkspaceCleanupRule(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	const gb = 1 << 30
	now := time.Now()
	env := Env{Trigger: "overseer", Now: now}
	low := func(mod func(*Item)) Item {
		it := Item{Kind: OnSandbox, Sandbox: "fix-repo-1", Awake: true, DiskSize: 60 * gb, DiskFree: 5 * gb}
		if mod != nil {
			mod(&it)
		}
		return it
	}
	for _, tc := range []struct {
		name string
		it   Item
		run  bool
	}{
		{"low, awake, idle: cleaned", low(nil), true},
		{"room enough", low(func(it *Item) { it.DiskFree = 30 * gb }), false},
		{"a task running", low(func(it *Item) { it.TaskActive = true }), false},
		{"paused", low(func(it *Item) { it.Awake = false }), false},
		{"not measured", low(func(it *Item) { it.DiskSize, it.DiskFree = 0, 0 }), false},
		{"cleaned 10 minutes ago: waits", low(func(it *Item) {
			it.Runs = []PastRun{{Rule: "workspace-cleanup", StartedAt: now.Add(-15 * time.Minute), EndedAt: now.Add(-10 * time.Minute)}}
		}), false},
		{"cleaned 2 hours ago: again", low(func(it *Item) {
			it.Runs = []PastRun{{Rule: "workspace-cleanup", StartedAt: now.Add(-125 * time.Minute), EndedAt: now.Add(-2 * time.Hour)}}
		}), true},
	} {
		var got *Decision
		for _, d := range control.Decide(tc.it, env) {
			if d.Rule == "workspace-cleanup" && d.Outcome == OutcomeRun {
				got = &d
			}
		}
		if (got != nil) != tc.run {
			t.Errorf("%s: ran %v, want %v", tc.name, got != nil, tc.run)
			continue
		}
		if got != nil && (got.Recipe != "workspace-cleanup" || got.Inputs["target_bytes"] != "21474836480") {
			t.Errorf("%s: decision %+v", tc.name, got)
		}
	}
}

// A sandbox rule's predicates are its own: disk, task, awake on a rule on
// sandboxes, none of them on a rule on issues or PRs, and a disk threshold
// must be a quantity.
func TestSandboxPredicates(t *testing.T) {
	for y, want := range map[string]string{
		"name: s\non: sandbox\norder: 10\nwhen: {disk: {free-below: 5Gi}, task: none, awake: true}\nrun: {recipe: workspace-cleanup}\n": "",
		"name: s\non: pr\norder: 10\nwhen: {disk: {free-below: 5Gi}}\nrun: {recipe: care-ci}\n":                                         "not a predicate of a rule on pr",
		"name: s\non: sandbox\norder: 10\nwhen: {disk: {free-below: lots}}\nrun: {recipe: workspace-cleanup}\n":                         "free-below",
		"name: s\non: sandbox\norder: 10\nwhen: {task: busy}\nrun: {recipe: workspace-cleanup}\n":                                       "task",
	} {
		r, err := Parse([]byte(y))
		if err == nil {
			err = r.Validate()
		}
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", y, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v, want %q", y, err, want)
		}
	}
}
