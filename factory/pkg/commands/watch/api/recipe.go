package api

import (
	"fmt"
	"sort"
)

// RecipeTask is how the runner runs a task type as a `factory recipe`:
// how its runs are named. What it runs - the recipe, the action of its
// result the CLI applies, the inputs - is the rule's that decided the
// task, stamped on it (QueueTask.Recipe, Apply, Inputs, PRLabels;
// act.Task); the sandbox, the recipe's (sandbox.Service.ResolveName).
type RecipeTask struct {
	// RunPrefix starts its run names (RunName).
	RunPrefix string
}

// recipeTasks are the task types that run as `factory recipe`. The other,
// agent-chore, runs as `factory agent create`, which runs the agent or
// workflow recipe (ChoreRunName).
var recipeTasks = map[TaskType]RecipeTask{
	TypePRReview:      {RunPrefix: "review"},
	TypePRInvestigate: {RunPrefix: "investigate"},
	TypePRComments:    {RunPrefix: "comments"},
	TypePRIterate:     {RunPrefix: "iterate"},
	TypeIssueFix:      {RunPrefix: "fix"},
	// A rule task's run is named after its rule (RunName).
	TypePRRule:    {RunPrefix: "rule"},
	TypeIssueRule: {RunPrefix: "rule"},
}

// RecipeArgs are the arguments of the run the rules engine decided for t,
// after `recipe <t.Recipe>` and the CLI's own: --apply --action for its
// apply, --pr-labels for a {{pr-labels}} input, then each input, by name,
// as --input name=value. nil for a task no rule decided (no recipe).
func RecipeArgs(t *QueueTask) []string {
	if t.Recipe == "" {
		return nil
	}
	var args []string
	if t.Apply != "" {
		args = append(args, "--apply", "--action", t.Apply)
	}
	if t.PRLabels {
		args = append(args, "--pr-labels")
	}
	names := make([]string, 0, len(t.Inputs))
	for name := range t.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		args = append(args, "--input", name+"="+t.Inputs[name])
	}
	return args
}

// RecipeTaskOf is how tasks of type t run as a recipe, if they do.
func RecipeTaskOf(t TaskType) (RecipeTask, bool) {
	rt, ok := recipeTasks[t]
	return rt, ok
}

// RunName names the run of t: its PR, the commit it is for and when t was
// enqueued. A watcher restarted mid-run requeues t under the same name,
// and the CLI follows that run, or applies its result, instead of starting
// another; t enqueued again (after a failure, say) starts a new one.
func (rt RecipeTask) RunName(t *QueueTask) string {
	sha := t.CommitSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if sha == "" {
		sha = "head"
	}
	at := t.EnqueuedAt
	if at.IsZero() {
		at = t.CreatedAt
	}
	prefix := rt.RunPrefix
	if (t.Type == TypePRRule || t.Type == TypeIssueRule) && t.Rule != "" {
		prefix = t.Rule
	}
	return fmt.Sprintf("%s-%d-%s-%d", prefix, t.Number, sha, at.Unix())
}

// RunNameOf is t's run name (factory --run-name): the one stamped on it
// when it was queued (TaskQueueManager.Enqueue), else its type's - RunName
// for a recipe task, ChoreRunName for an agent chore - "" for neither.
func RunNameOf(t *QueueTask) string {
	if t.RunName != "" {
		return t.RunName
	}
	if rt, ok := RecipeTaskOf(t.Type); ok {
		return rt.RunName(t)
	}
	if t.Type == TypeAgentChore {
		return ChoreRunName(t)
	}
	return ""
}

// ChoreRunName names a run of an agent-chore task t (factory agent create
// --run-name) as RunName names a recipe task's: by its issue, if it is a
// workflow's, and when t was enqueued. Each agent has a sandbox of its
// own, so the name need not say which agent it is.
func ChoreRunName(t *QueueTask) string {
	at := t.EnqueuedAt
	if at.IsZero() {
		at = t.CreatedAt
	}
	return fmt.Sprintf("agent-%d-%d", t.Number, at.Unix())
}
