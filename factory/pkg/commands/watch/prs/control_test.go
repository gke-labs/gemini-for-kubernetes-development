package prs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
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

// ciGivesUpAfter is the prebaked rules with ci's give-up after n runs of
// any time, so a test can reach it with one finished run.
func ciGivesUpAfter(t *testing.T, n int) rules.Set {
	t.Helper()
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var ci rules.Rule
	for _, r := range base {
		if r.Name == "ci" {
			ci = *r
		}
	}
	gu := *ci.Policy.GiveUp
	gu.Attempts, gu.Since = n, nil
	ci.Policy.GiveUp = &gu
	set, err := rules.Overlay(base, []*rules.Rule{&ci})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// handedCI is a scanner over f whose engine decides ci, recording to a
// file the test reads back.
func handedCI(t *testing.T, f *prFixture, set rules.Set) (*Scanner, interface {
	TaskExists(string) bool
}, string) {
	t.Helper()
	c, err := rules.NewControl("on", map[string]string{"ci": "engine"}, "", nil, set)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "d.jsonl")
	s, queue := newTestScanner(t, t.TempDir(), testOpts{
		GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
		Recorder: rules.NewRecorder(record, 0, nil),
		Rules:    c,
	})
	return s, queue, record
}

// The engine gives ci up while the handler's own count is short of it
// (one run in the journal; no "started investigating" comments): the
// watch gives up, as on its own count, and queues no investigation.
func TestEvaluate_EngineGivesCIUp(t *testing.T) {
	f := &prFixture{num: 14, headSHA: "sha-1", updated: time.Now(), failing: true}
	s, queue, record := handedCI(t, f, ciGivesUpAfter(t, 1))
	const fn = "task-pr-14-investigate.yaml"
	task := &api.QueueTask{Type: api.TypePRInvestigate, Number: 14, CommitSHA: "sha-1"}
	q := s.queue.(interface {
		Enqueue(string, *api.QueueTask) error
		StartTask(string, *api.QueueTask) error
		CompleteTask(string, *api.QueueTask) error
	})
	if err := q.Enqueue(fn, task); err != nil {
		t.Fatal(err)
	}
	if err := q.StartTask(fn, task); err != nil {
		t.Fatal(err)
	}
	task.CompletedAt = time.Now()
	if err := q.CompleteTask(fn, task); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	if queue.TaskExists(fn) {
		t.Error("an investigation was queued after the engine gave up")
	}
	if f.count("POST /repos/test-owner/test-repo/issues/14/labels") == 0 {
		t.Error("no stop label after the engine gave up")
	}
	if f.count("POST /repos/test-owner/test-repo/issues/14/comments") == 0 {
		t.Error("no give-up comment")
	}
	assertRecorded(t, record, `"ci give-up"`, `"handed":[{"rule":"ci","legacy":true}]`)
}

// The handler's own count says give up (three "started investigating"
// comments since the head) but the engine, with no runs in its journal,
// says run: the engine's count stands, and the investigation is queued.
func TestEvaluate_EngineRunsCIPastTheHandlersCount(t *testing.T) {
	later := time.Now().Add(time.Hour)
	var comments []*githubv39.IssueComment
	for i := 0; i < 3; i++ {
		comments = append(comments, &githubv39.IssueComment{
			User:      &githubv39.User{Login: githubv39.String("bot1")},
			Body:      githubv39.String("🤖 AI Factory started investigating CI check failures"),
			CreatedAt: &later,
		})
	}
	f := &prFixture{num: 15, headSHA: "sha-1", updated: time.Now(), failing: true, comments: comments}
	base, _ := rules.Builtin()
	s, queue, record := handedCI(t, f, base)

	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	if !queue.TaskExists("task-pr-15-investigate.yaml") {
		t.Error("no investigation queued on the engine's run")
	}
	if f.count("POST /repos/test-owner/test-repo/issues/15/labels") != 0 {
		t.Error("the handler gave up on its own count")
	}
	assertRecorded(t, record, `"ci run"`, `"handed":[{"rule":"ci","legacy":true}]`)
}

func assertRecorded(t *testing.T, path string, wants ...string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wants {
		if !strings.Contains(string(b), w) {
			t.Errorf("record lacks %s:\n%s", w, b)
		}
	}
}

// The canary: a pull request the rules pick by number gets the canary label,
// so the engine takes it in the same pass; one a person took the label off,
// one handed to the humans, or a dry run, does not.
func TestEvaluate_LabelsTheCanary(t *testing.T) {
	base, _ := rules.Builtin()
	control := func(t *testing.T, percent int) *rules.Control {
		c, err := rules.NewControl("on", map[string]string{"ci": "engine"}, "factory/rules-canary", nil, base)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetCanaryPercent(percent); err != nil {
			t.Fatal(err)
		}
		return c
	}
	removed := []*githubv39.Timeline{{Event: githubv39.String("unlabeled"), Label: &githubv39.Label{Name: githubv39.String("factory/rules-canary")}}}
	for _, tc := range []struct {
		name     string
		percent  int
		timeline []*githubv39.Timeline
		ready    bool
		dryRun   bool
		want     bool
	}{
		{"picked: labelled", 100, nil, false, false, true},
		{"not picked (0%)", 0, nil, false, false, false},
		{"a person took it off: not again", 100, removed, false, false, false},
		{"handed to the humans: left alone", 100, nil, true, false, false},
		{"dry run: nothing written", 100, nil, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &prFixture{num: 31, headSHA: "sha-31", updated: time.Now(), pending: true, timeline: tc.timeline, readyForHuman: tc.ready}
			s, _ := newTestScanner(t, t.TempDir(), testOpts{
				GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    control(t, tc.percent),
			})
			s.cfg.DryRun = tc.dryRun
			f.mu.Lock()
			listed := f.listingIssueLocked()
			f.mu.Unlock()
			s.evaluate(context.Background(), listed)
			f.mu.Lock()
			got := slices.Contains(f.added, "factory/rules-canary")
			f.mu.Unlock()
			if got != tc.want {
				t.Errorf("labelled %v, want %v (added %v)", got, tc.want, f.added)
			}
			if inPR := slices.ContainsFunc(listed.Labels, func(l *githubv39.Label) bool { return l.GetName() == "factory/rules-canary" }); inPR != tc.want {
				t.Errorf("label on the evaluated PR %v, want %v (the engine takes it this pass)", inPR, tc.want)
			}
		})
	}
}
