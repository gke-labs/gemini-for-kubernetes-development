package watch

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/act"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// ruleTaskFileRE is a rule task's file (api.RuleTaskFile):
// task-(pr|issue)-<n>-rule-<rule>.
var ruleTaskFileRE = regexp.MustCompile(`^task-(pr|issue)-(\d+)-rule-([a-z0-9][a-z0-9-]*)\.yaml$`)

// workflowTaskFileRE is a workflow step's file:
// task-workflow-<name>-issue-<n>.
var workflowTaskFileRE = regexp.MustCompile(`^task-workflow-.+-issue-(\d+)\.yaml$`)

// taskRule is the rule a task file runs, the kind of item it is on and
// its number: a rule task's file names its rule, a workflow step's is the
// workflow rule's; "" for a task no rule decides (a chore, an agent, a
// fan-out's).
func taskRule(filename string) (rule, kind string, num int) {
	if m := ruleTaskFileRE.FindStringSubmatch(filename); m != nil {
		num, _ = strconv.Atoi(m[2])
		kind = rules.OnIssue
		if m[1] == "pr" {
			kind = rules.OnPR
		}
		return m[3], kind, num
	}
	if m := workflowTaskFileRE.FindStringSubmatch(filename); m != nil {
		num, _ = strconv.Atoi(m[1])
		return "workflow", rules.OnIssue, num
	}
	return "", "", 0
}

// Redecide is the engine deciding again, just before a queued task starts,
// whether it decides the task's rule on the item's facts read now
// (Scanner.Facts), as if the task were not there: not queued, no task of
// the item active. A gate that now holds, a PR head that moved since the
// task was decided, or a rule that no longer decides (it fails, its policy
// holds it, an earlier rule takes the item, it gives up) drops it, and so
// does the rule being disabled (the emergency switch stops queued work
// too). A task whose facts cannot be read runs, as it would without the
// check. A task that carries no recipe (queued before the rules stamped
// their tasks, or by hand) is stamped with the engine's decision when it
// decides the rule, and dropped when its facts cannot be read: the runner
// runs only what a rule decided (api.RecipeArgs).
func (c *watcherTaskCoordinator) Redecide(ctx context.Context, filename string, task *api.QueueTask) (bool, string) {
	w := c.w
	rule, kind, num := taskRule(filename)
	if rule == "" || w.rulesControl == nil {
		return false, ""
	}
	// What the task does on GitHub as it starts and ends is the rule's as
	// it is now: a task queued by an older watch, or by the nudge, carries
	// none of its own.
	act.Declare(task, w.rulesControl.Set().Named(rule))
	var it rules.Item
	var env rules.Env
	var err error
	switch {
	case kind == rules.OnPR && w.prScanner != nil:
		it, env, err = w.prScanner.Facts(ctx, num)
	case kind == rules.OnIssue && w.issueScanner != nil:
		it, env, err = w.issueScanner.Facts(ctx, num)
	default:
		return false, ""
	}
	if err != nil {
		if task.Recipe == "" {
			why := fmt.Sprintf("it carries no recipe and #%d could not be read: %v", num, err)
			w.rulesSummary.Dropped(rule, why)
			return true, why
		}
		klog.Warningf("Deciding %s again before it starts: reading #%d: %v; running it", filename, num, err)
		return false, ""
	}
	drop, why := true, fmt.Sprintf("the %s rule is disabled", rule)
	if !w.rulesControl.Off(rule) {
		drop, why = atStart(w.rulesControl.Set(), rule, task, it, env)
	}
	if drop {
		w.rulesSummary.Dropped(rule, why)
		return drop, why
	}
	// What the watcher writes when the task ends is the rule's as it is
	// now, filled in for the item as it is now.
	act.Hooks(task, rules.Hooks(w.rulesControl.Set().Named(rule), it, env))
	return drop, why
}

// atStart is Redecide on the facts read: a task held to the gates alone
// (gatesOnly) is dropped only on a gate; any other, on redecide's answer.
func atStart(set rules.Set, rule string, task *api.QueueTask, it rules.Item, env rules.Env) (bool, string) {
	if why := gatesOnly(rule, task); why != "" {
		if g, ok := rules.GateOf(it, env); ok {
			return true, "the item " + g.Why
		}
		klog.V(2).Infof("Deciding %s on #%d again before it starts: only the gates, %s", rule, it.Number, why)
		return false, ""
	}
	return redecide(set, rule, task, it, env)
}

// gatesOnly says why a task is held to the gates alone at start, not to its
// rule, "" when its rule decides again:
//
//   - a workflow step the nudge queued (a linked issue closed): the nudge
//     steps the workflow at once, inside the workflow rule's cooldown, and
//     is not on the engine yet, so the workflow rule's answer is not its.
//
// A comments task decides again: it acknowledges the feedback it answers
// when it starts (NotifyTaskStarted), so the feedback is still new at start.
func gatesOnly(rule string, task *api.QueueTask) string {
	if rule == "workflow" && task.TriggerReason == api.TriggerReasonLinkedIssueClosed {
		return "the nudge queued it"
	}
	return ""
}

// redecide is Redecide on facts read: whether the set no longer decides
// rule on the item, the task's own presence taken out, and why. A task it
// still decides that carries no recipe is stamped with the decision.
func redecide(set rules.Set, rule string, task *api.QueueTask, it rules.Item, env rules.Env) (bool, string) {
	if g, ok := rules.GateOf(it, env); ok {
		return true, "the item " + g.Why
	}
	if it.Kind == rules.OnPR && task.CommitSHA != "" && it.Head != "" && it.Head != task.CommitSHA {
		return true, fmt.Sprintf("head moved %s→%s", shortSHA(task.CommitSHA), shortSHA(it.Head))
	}
	// As if the task were not there: it is what is queued, and what makes
	// a task of the item active.
	it.TaskActive = false
	it.Queued = slices.DeleteFunc(slices.Clone(it.Queued), func(r string) bool { return r == rule })
	for _, st := range set.Explain(it, env).Steps {
		if st.Rule != rule {
			continue
		}
		switch {
		case st.Decision != nil && st.Decision.Outcome == rules.OutcomeRun:
			if task.Recipe == "" {
				act.Task(task, *st.Decision, set.Named(rule))
			}
			return false, ""
		case st.Decision != nil:
			return true, fmt.Sprintf("the rules now decide %s %s", rule, st.Decision.Outcome)
		case st.Skipped != "":
			return true, fmt.Sprintf("the rules no longer decide %s: %s", rule, st.Skipped)
		case len(st.Failed) > 0:
			return true, fmt.Sprintf("the rules no longer decide %s: %s", rule, strings.Join(st.Failed, "; "))
		default:
			return true, fmt.Sprintf("the rules no longer decide %s: %s", rule, st.Held)
		}
	}
	// The rule is not on the item's kind (or not in the set): nothing to
	// say about it.
	return false, ""
}
