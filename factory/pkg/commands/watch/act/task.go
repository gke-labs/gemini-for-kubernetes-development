package act

import (
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// Task puts the engine's run decision on the task its scanner built for
// it: the rule, the recipe, its apply and inputs (an input of
// {{pr-labels}} as PRLabels: the CLI labels the PR itself, when it opens
// it, rather than with the labels of the moment the rule was decided),
// and the rule's phase. The runner then runs what the rule says
// (api.RecipeArgs). r is the decided rule, for its inputs as written.
func Task(task *api.QueueTask, d rules.Decision, r *rules.Rule) {
	if d.Outcome != rules.OutcomeRun || d.Recipe == "" {
		return
	}
	task.Rule, task.Recipe, task.Apply = d.Rule, d.Recipe, d.Apply
	task.Inputs, task.PRLabels = nil, false
	for name, v := range d.Inputs {
		if r != nil && strings.TrimSpace(r.Run.Inputs[name]) == "{{pr-labels}}" {
			task.PRLabels = true
			continue
		}
		if r != nil && strings.TrimSpace(r.Run.Inputs[name]) == rules.ReviewInstructions {
			// The scanner's to give, as --instruction flags.
			continue
		}
		if task.Inputs == nil {
			task.Inputs = map[string]string{}
		}
		task.Inputs[name] = v
	}
	if d.Phase > 0 {
		task.Phase = api.TaskPhase(d.Phase)
	}
	Declare(task, r)
	Hooks(task, d.Hooks)
}

// Hooks stamps the writes the watcher makes at points of the task's life
// (the rule's run.on-* hooks, filled in for the item): when it is decided,
// and again when it starts, on the facts read then.
func Hooks(task *api.QueueTask, h rules.HookWrites) {
	task.OnStart, task.OnSuccess, task.OnFailure, task.OnEnd = taskWrites(h.OnStart), taskWrites(h.OnSuccess), taskWrites(h.OnFailure), taskWrites(h.OnEnd)
}

// HookWrites is a task's stamped writes as rules.Writes, for ApplyWrites.
func HookWrites(tw *api.TaskWrites) rules.Writes {
	if tw == nil {
		return rules.Writes{}
	}
	return rules.Writes{Label: tw.Label, Unlabel: tw.Unlabel, Assign: tw.Assign, Unassign: tw.Unassign, Comment: tw.Comment, Stop: tw.Stop}
}

func taskWrites(w rules.Writes) *api.TaskWrites {
	if len(w.Label)+len(w.Unlabel)+len(w.Assign)+len(w.Unassign) == 0 && w.Comment == "" && !w.Stop {
		return nil
	}
	return &api.TaskWrites{Label: w.Label, Unlabel: w.Unlabel, Assign: w.Assign, Unassign: w.Unassign, Comment: w.Comment, Stop: w.Stop}
}

// Declare puts what the rule's task does on GitHub as it starts and ends
// on the task (claim, acknowledge, start-comment): when the rule decides
// it, and again just before it starts, from the rule as it is then (a task
// queued by an older watch, or one the nudge queued, gets them too).
func Declare(task *api.QueueTask, r *rules.Rule) {
	if r == nil {
		return
	}
	task.Claim, task.Acknowledge, task.StartComment = r.Run.Claim, r.Run.Acknowledge, r.Run.StartComment
}
