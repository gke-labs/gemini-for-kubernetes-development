package prs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
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
// disabled, or the engine decides otherwise.
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
			c, err := rules.NewControl([]string{"conflict"}, base)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, false},
		{"the engine, handed conflict, decides no rebase", func(t *testing.T) *rules.Control {
			c, err := rules.NewControl(nil, neverConflict(t))
			if err != nil {
				t.Fatal(err)
			}
			return c
		}, false},
		{"the engine, handed conflict, decides the rebase", func(t *testing.T) *rules.Control {
			base, _ := rules.Builtin()
			c, err := rules.NewControl(nil, base)
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
			if got := queue.TaskExists("task-pr-12-rule-conflict.yaml"); got != tc.rebase {
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
			c, _ = rules.NewControl([]string{"ready"}, base)
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
	c, err := rules.NewControl(nil, set)
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
	const fn = "task-pr-14-rule-ci.yaml"
	task := &api.QueueTask{Type: api.TypePRRule, Number: 14, CommitSHA: "sha-1"}
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
	assertRecorded(t, record, `"ci give-up"`)
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
	c, err := rules.NewControl(nil, base)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	record := filepath.Join(t.TempDir(), "d.jsonl")
	s, queue := newTestScanner(t, dir, testOpts{
		GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
		Recorder: rules.NewRecorder(record, 0, nil), Rules: c,
	})

	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	if !queue.TaskExists("task-pr-15-rule-ci.yaml") {
		t.Error("no investigation queued on the engine's run")
	}
	if f.count("POST /repos/test-owner/test-repo/issues/15/labels") != 0 {
		t.Error("the handler gave up on its own count")
	}
	assertRecorded(t, record, `"ci run"`)
	// The task carries the engine's decision: the runner runs the rule's
	// recipe and apply.
	data, err := os.ReadFile(filepath.Join(dir, "incoming", "task-pr-15-rule-ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var task api.QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	if task.Rule != "ci" || task.Recipe != "care-ci" || task.Apply != "post-replies" || task.Phase != api.PhaseInvestigate {
		t.Errorf("queued task = %+v, want the ci rule's run", task)
	}
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

// handedComments is a scanner over f whose engine decides comments, with
// rule set set, recording to a file.
func handedComments(t *testing.T, f *prFixture, set rules.Set) (*Scanner, *concurrency.TaskQueueManager, string) {
	t.Helper()
	c, err := rules.NewControl(nil, set)
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

// failComments leaves a failed comments run on head in queue's history, with
// the handler's retry count at retries.
func failComments(t *testing.T, queue *concurrency.TaskQueueManager, num int, head string, retries int) {
	t.Helper()
	fn := fmt.Sprintf("task-pr-%d-rule-comments.yaml", num)
	task := &api.QueueTask{Type: api.TypePRRule, Number: num, CommitSHA: head, RetryCount: retries}
	if err := queue.Enqueue(fn, task); err != nil {
		t.Fatal(err)
	}
	if err := queue.StartTask(fn, task); err != nil {
		t.Fatal(err)
	}
	task.CompletedAt = time.Now()
	if err := queue.FailTask(fn, task, "exit status 1"); err != nil {
		t.Fatal(err)
	}
}

// The engine gives comments up (its count of failed runs reached) while
// the handler's retry count is short of it: the watch gives up as it always
// has, the stop label and the pause comment, and queues no retry.
func TestEvaluate_EngineGivesCommentsUp(t *testing.T) {
	base, _ := rules.Builtin()
	var comments rules.Rule
	for _, r := range base {
		if r.Name == "comments" {
			comments = *r
		}
	}
	gu := *comments.Policy.GiveUp
	gu.Attempts, gu.Since = 1, nil
	comments.Policy.GiveUp = &gu
	set, err := rules.Overlay(base, []*rules.Rule{&comments})
	if err != nil {
		t.Fatal(err)
	}
	f := &prFixture{num: 41, headSHA: "sha-41", updated: time.Now()}
	s, queue, record := handedComments(t, f, set)
	failComments(t, queue, 41, "sha-41", 0)

	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	if queue.TaskExists("task-pr-41-rule-comments.yaml") {
		t.Error("a comments retry was queued after the engine gave up")
	}
	f.mu.Lock()
	stopped := slices.Contains(f.added, "factory/stop")
	f.mu.Unlock()
	if !stopped {
		t.Errorf("no stop label after the engine gave up (added %v)", f.added)
	}
	assertRecorded(t, record, `"comments give-up"`)
}

// The handler's retry count is at its limit, but the engine, counting the
// failed runs since the head (none since this head), retries: the engine's
// count stands, and no stop label goes on.
func TestEvaluate_EngineRetriesCommentsPastTheHandlersCount(t *testing.T) {
	base, _ := rules.Builtin()
	f := &prFixture{num: 42, headSHA: "sha-42", updated: time.Now()}
	s, queue, _ := handedComments(t, f, base)
	failComments(t, queue, 42, "sha-42", 3)

	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	if !queue.TaskExists("task-pr-42-rule-comments.yaml") {
		t.Error("no comments retry on the engine's run")
	}
	f.mu.Lock()
	stopped := slices.Contains(f.added, "factory/stop")
	f.mu.Unlock()
	if stopped {
		t.Error("the handler gave up on its own count")
	}
}

// A green, unreviewed PR that wants no review: the engine decides ready,
// and its writes go on the PR (the ready label, the pool's bot off).
func TestEvaluate_EngineReady(t *testing.T) {
	base, _ := rules.Builtin()
	c, err := rules.NewControl(nil, base)
	if err != nil {
		t.Fatal(err)
	}
	f := &prFixture{num: 51, headSHA: "sha-51", updated: time.Now()}
	record := filepath.Join(t.TempDir(), "d.jsonl")
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
		Recorder: rules.NewRecorder(record, 0, nil),
		Rules:    c,
	})
	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)
	assertRecorded(t, record, `"ready run"`)
	// The engine's ready is its writes: the ready label on, the pool's bot
	// off the PR.
	f.mu.Lock()
	added := slices.Clone(f.added)
	f.mu.Unlock()
	if !slices.Contains(added, "factory/ready-for-human") {
		t.Errorf("added %v, want the ready label", added)
	}
	if f.count("DELETE /repos/test-owner/test-repo/issues/51/assignees") != 1 {
		t.Errorf("the pool's bot was not unassigned: %v", f.calls)
	}
}

// The engine's inactive is its writes: the stop label and the rule's
// comment naming it, and nothing more decided on the PR. (Here inactive is made to hold on any pool PR: the fixture's
// commits are always new.)
func TestEvaluate_EngineInactiveStopsThePR(t *testing.T) {
	base, _ := rules.Builtin()
	var inactive rules.Rule
	for _, r := range base {
		if r.Name == "inactive" {
			inactive = *r
		}
	}
	inactive.When.InactiveFor = rules.Duration{}
	set, err := rules.Overlay(base, []*rules.Rule{&inactive})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	f := &prFixture{num: 52, headSHA: "sha-52", updated: time.Now()}
	record := filepath.Join(t.TempDir(), "d.jsonl")
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
		Recorder: rules.NewRecorder(record, 0, nil), Rules: c,
	})
	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)

	f.mu.Lock()
	added := slices.Clone(f.added)
	f.mu.Unlock()
	if !slices.Contains(added, "factory/stop") || slices.Contains(added, "factory/ready-for-human") {
		t.Errorf("added %v, want the stop label and nothing after it", added)
	}
	if f.count("POST /repos/test-owner/test-repo/issues/52/comments") != 1 {
		t.Errorf("want one pause comment: %v", f.calls)
	}
	assertRecorded(t, record, `"inactive run"`)
}
