package watch

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// taskFileRE is the task files of the rules the engine decides: a PR's
// (task-pr-<n>-<suffix>), an issue's fix (task-issue-<n>) and an issue's
// workflow (task-workflow-<name>-issue-<n>).
var taskFileRE = regexp.MustCompile(`^task-(?:pr-(\d+)-([a-z]+)|issue-(\d+)|workflow-.+-issue-(\d+))\.yaml$`)

// prTaskSuffixRules are the rules a PR's task files run, by their suffix
// (prs.prTaskRules).
var prTaskSuffixRules = map[string]string{"comments": "comments", "iterate": "conflict", "investigate": "ci", "review": "review"}

// taskRule is the rule a task file runs, the kind of item it is on and
// its number; "" for a task no rule decides (a chore, an agent, a
// fan-out's).
func taskRule(filename string) (rule, kind string, num int) {
	m := taskFileRE.FindStringSubmatch(filename)
	switch {
	case m == nil:
		return "", "", 0
	case m[1] != "":
		num, _ = strconv.Atoi(m[1])
		return prTaskSuffixRules[m[2]], rules.OnPR, num
	case m[3] != "":
		num, _ = strconv.Atoi(m[3])
		return "fix", rules.OnIssue, num
	default:
		num, _ = strconv.Atoi(m[4])
		return "workflow", rules.OnIssue, num
	}
}

// Redecide is the engine deciding again, just before a queued task starts,
// whether it decides the task's rule on the item's facts read now
// (Scanner.Facts), as if the task were not there: not queued, no task of
// the item active. A gate that now holds, a PR head that moved since the
// task was decided, or a rule that no longer decides (it fails, its policy
// holds it, an earlier rule takes the item, it gives up) drops it, and so
// does the rule being disabled (the emergency switch stops queued work
// too). A task whose facts cannot be read runs, as it would without the
// check.
func (c *watcherTaskCoordinator) Redecide(ctx context.Context, filename string, task *api.QueueTask) (bool, string) {
	w := c.w
	rule, kind, num := taskRule(filename)
	if rule == "" || w.rulesControl == nil {
		return false, ""
	}
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
		klog.Warningf("Deciding %s again before it starts: reading #%d: %v; running it", filename, num, err)
		return false, ""
	}
	if w.rulesControl.Off(rule) {
		return true, fmt.Sprintf("the %s rule is disabled", rule)
	}
	return atStart(w.rulesControl.Set(), rule, task, it, env)
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
// rule on the item, the task's own presence taken out, and why.
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
