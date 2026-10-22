package watch

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

func TestTaskRule(t *testing.T) {
	for file, want := range map[string]string{
		"task-pr-7-comments.yaml":                             "comments pr 7",
		"task-pr-7-iterate.yaml":                              "conflict pr 7",
		"task-pr-7-investigate.yaml":                          "ci pr 7",
		"task-pr-7-review.yaml":                               "review pr 7",
		"task-issue-12.yaml":                                  "fix issue 12",
		"task-workflow-kcc-greenfield-issue-13587.yaml":       "workflow issue 13587",
		"task-chore-dependency-update-scheduler.yaml":         "  0",
		"task-pr-7-unknown.yaml":                              " pr 7",
		"task-workflow-kcc-greenfield-oneparent-issue-9.yaml": "workflow issue 9",
		"task-pr-7-rule-deploy-preview.yaml":                  "deploy-preview pr 7",
		"task-issue-12-rule-triage-2.yaml":                    "triage-2 issue 12",
	} {
		rule, kind, num := taskRule(file)
		if got := strings.Join([]string{rule, kind, strconv.Itoa(num)}, " "); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
}

// The engine decides again on the facts read at start, its own task taken
// out: a ci task runs on a PR whose checks still fail, and is dropped when
// feedback came meanwhile (comments, earlier, takes the PR), the checks
// went green, the head moved, or the PR was closed.
func TestRedecide(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	env := rules.Env{Trigger: "overseer", Prefixes: conventions.Prefixes("overseer"), Pool: []string{"ada-coder-bot"}, Now: now}
	yes := true
	ci := func(mod func(*rules.Item)) rules.Item {
		it := rules.Item{Kind: rules.OnPR, Number: 7, Author: "ada-coder-bot", Labels: []string{"overseer"}, Head: "aaaaaaaa",
			Mergeable: &yes, Checks: "failing", UpdatedAt: now.Add(-time.Hour), LastTrustedActivity: now.Add(-time.Hour),
			// What the queue says while the task waits to start.
			TaskActive: true, Queued: []string{"ci"}}
		if mod != nil {
			mod(&it)
		}
		return it
	}
	task := &api.QueueTask{Number: 7, CommitSHA: "aaaaaaaa"}
	for _, tc := range []struct {
		name string
		it   rules.Item
		drop string
	}{
		{"checks still failing: runs", ci(nil), ""},
		{"feedback came: comments takes the PR", ci(func(it *rules.Item) { it.NewTrustedFeedback = true }), "comments took the item"},
		{"checks went green", ci(func(it *rules.Item) { it.Checks = "green" }), "checks: failing (has checks green)"},
		{"head moved", ci(func(it *rules.Item) { it.Head = "bbbbbbbb" }), "head moved aaaaaaa→bbbbbbb"},
		{"closed", ci(func(it *rules.Item) { it.Closed = true }), "the item is closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drop, why := redecide(set, "ci", task, tc.it, env)
			if drop != (tc.drop != "") || !strings.Contains(why, tc.drop) {
				t.Errorf("drop %v (%s), want %q", drop, why, tc.drop)
			}
		})
	}

	// A fix on an issue the overseer is no longer pointed at (the trigger
	// label taken off, no pool assignee) is dropped; with a PR linked
	// since, too.
	issue := rules.Item{Kind: rules.OnIssue, Number: 12, Labels: []string{"overseer"}, UpdatedAt: now.Add(-time.Hour), TaskActive: true, Queued: []string{"fix"}}
	unstamped := &api.QueueTask{Number: 12}
	if drop, why := redecide(set, "fix", unstamped, issue, env); drop {
		t.Errorf("a labelled issue's fix dropped: %s", why)
	}
	// A task that carries no recipe (queued before the rules stamped
	// their tasks) runs what the engine decides now; a stamped one keeps
	// its stamp.
	if unstamped.Rule != "fix" || unstamped.Recipe != "fix" || unstamped.Apply != "open-pr" || !unstamped.PRLabels || unstamped.Inputs["draft"] != "false" {
		t.Errorf("an unstamped fix was not stamped with the decision: %+v", unstamped)
	}
	stamped := &api.QueueTask{Number: 12, Rule: "fix", Recipe: "fix-v2", Apply: "open-pr"}
	if drop, _ := redecide(set, "fix", stamped, issue, env); drop || stamped.Recipe != "fix-v2" {
		t.Errorf("a stamped fix: drop %v, recipe %s", drop, stamped.Recipe)
	}
	unlabelled := issue
	unlabelled.Labels = []string{"bug"}
	if drop, why := redecide(set, "fix", &api.QueueTask{Number: 12}, unlabelled, env); !drop || !strings.Contains(why, "any-of") {
		t.Errorf("an issue no longer pointed at: drop %v (%s)", drop, why)
	}
	linked := issue
	linked.LinkedPR = "open"
	if drop, why := redecide(set, "fix", &api.QueueTask{Number: 12}, linked, env); !drop || !strings.Contains(why, "linked-pr") {
		t.Errorf("an issue with a linked PR: drop %v (%s)", drop, why)
	}
}

// A workflow step the nudge queued is held to the gates alone at start: the
// workflow rule's cooldown does not drop a nudge (#14192), a gate does. A
// comments task decides again (it acknowledges its feedback only when it
// starts, so #13777's drop cannot happen).
func TestAtStartGatesOnly(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	env := rules.Env{Trigger: "overseer", Prefixes: conventions.Prefixes("overseer"), Pool: []string{"ada-coder-bot"}, Now: now}
	// A workflow that succeeded 30m ago: inside its 2h cooldown.
	cooling := rules.Item{Kind: rules.OnIssue, Number: 14192, Labels: []string{"overseer"}, Workflow: "kcc-greenfield", WorkflowCooldown: 2 * time.Hour,
		UpdatedAt: now.Add(-time.Hour), TaskActive: true, Queued: []string{"workflow"},
		Runs: []rules.PastRun{{Rule: "workflow", StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-30 * time.Minute)}}}
	nudged := &api.QueueTask{Number: 14192, TriggerReason: api.TriggerReasonLinkedIssueClosed}
	if drop, why := atStart(set, "workflow", nudged, cooling, env); drop {
		t.Errorf("a nudge dropped by the workflow cooldown: %s", why)
	}
	if drop, why := atStart(set, "workflow", &api.QueueTask{Number: 14192}, cooling, env); !drop || !strings.Contains(why, "cooldown") {
		t.Errorf("a scanned workflow step inside its cooldown: drop %v (%s)", drop, why)
	}
	closed := cooling
	closed.Closed = true
	if drop, why := atStart(set, "workflow", nudged, closed, env); !drop || why != "the item is closed" {
		t.Errorf("a nudge on a closed issue: drop %v (%s)", drop, why)
	}

	// A comments task decides again: it acknowledges its feedback when it
	// starts, so at start the feedback is still new and it runs; with none
	// left (the comment deleted, say), it is dropped; a gate drops it too.
	yes := true
	answering := rules.Item{Kind: rules.OnPR, Number: 13777, Author: "ada-coder-bot", Labels: []string{"overseer"}, Head: "aaaaaaaa",
		Mergeable: &yes, Checks: "green", UpdatedAt: now, LastTrustedActivity: now, NewTrustedFeedback: true, TaskActive: true, Queued: []string{"comments"}}
	if drop, why := atStart(set, "comments", &api.QueueTask{Number: 13777}, answering, env); drop {
		t.Errorf("a comments task with its feedback still new dropped: %s", why)
	}
	gone := answering
	gone.NewTrustedFeedback = false
	if drop, why := atStart(set, "comments", &api.QueueTask{Number: 13777}, gone, env); !drop || !strings.Contains(why, "feedback") {
		t.Errorf("a comments task with no feedback left: drop %v (%s)", drop, why)
	}
	stopped := answering
	stopped.Labels = append(stopped.Labels, "overseer/stop")
	if drop, why := atStart(set, "comments", &api.QueueTask{Number: 13777}, stopped, env); !drop || why != "the item has the stop label" {
		t.Errorf("a comments task on a stopped PR: drop %v (%s)", drop, why)
	}
}
