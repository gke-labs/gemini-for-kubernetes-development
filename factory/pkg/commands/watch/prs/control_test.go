package prs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// neverConflict is the prebaked rules with conflict changed to need a
// label nothing has: the engine, handed conflict, decides no rebase where
// the scanner would queue one.
func neverConflict(t *testing.T) rules.Set {
	t.Helper()
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var conflict rules.Rule
	for _, r := range base {
		if r.Name == "conflict" {
			conflict = *r
		}
	}
	conflict.When.Labels = &rules.Labels{All: []string{"never"}}
	set, err := rules.Overlay(base, []*rules.Rule{&conflict})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// A conflicting PR: the scanner queues a rebase, unless conflict is
// disabled, or handed to an engine that decides otherwise - and then only
// on items with the canary label, when there is one.
func TestEvaluate_RulesControl(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name    string
		control func(t *testing.T) *rules.Control
		rebase  bool
	}{
		{"no control: as before", func(*testing.T) *rules.Control { return nil }, true},
		{"conflict disabled", func(t *testing.T) *rules.Control {
			base, _ := rules.Builtin()
			c, err := rules.NewControl("", nil, "", []string{"conflict"}, base)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, false},
		{"the engine, handed conflict, decides no rebase", func(t *testing.T) *rules.Control {
			c, err := rules.NewControl("on", map[string]string{"conflict": "engine"}, "", nil, neverConflict(t))
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, false},
		{"the engine, handed conflict on a canary the PR lacks: the scanner's", func(t *testing.T) *rules.Control {
			c, err := rules.NewControl("on", map[string]string{"conflict": "engine"}, "overseer/engine", nil, neverConflict(t))
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, true},
		{"the engine on, conflict left legacy", func(t *testing.T) *rules.Control {
			c, err := rules.NewControl("on", nil, "", nil, neverConflict(t))
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, true},
		{"the engine, handed conflict, decides the rebase", func(t *testing.T) *rules.Control {
			base, _ := rules.Builtin()
			c, err := rules.NewControl("on", map[string]string{"conflict": "engine"}, "", nil, base)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &prFixture{num: 12, headSHA: "sha-1", updated: time.Now(), mergeable: &no}
			s, queue := newTestScanner(t, t.TempDir(), testOpts{
				GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    tc.control(t),
			})
			f.mu.Lock()
			listed := f.listingIssueLocked()
			f.mu.Unlock()
			s.evaluate(context.Background(), listed)
			if got := queue.TaskExists("task-pr-12-iterate.yaml"); got != tc.rebase {
				t.Errorf("rebase queued %v, want %v", got, tc.rebase)
			}
		})
	}
}

// A ready PR is handed to the humans unless ready is disabled.
func TestEvaluate_ReadyDisabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now()}
		var c *rules.Control
		if disabled {
			base, _ := rules.Builtin()
			c, _ = rules.NewControl("", nil, "", []string{"ready"}, base)
		}
		s, _ := newTestScanner(t, t.TempDir(), testOpts{
			GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory", Rules: c,
		})
		f.mu.Lock()
		listed := f.listingIssueLocked()
		f.mu.Unlock()
		s.evaluate(context.Background(), listed)
		labelled := f.count("/issues/10/labels") > 0
		if labelled == disabled {
			t.Errorf("ready disabled %v: labelled %v", disabled, labelled)
		}
	}
}
