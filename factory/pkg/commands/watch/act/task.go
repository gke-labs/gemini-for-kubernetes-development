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
}
