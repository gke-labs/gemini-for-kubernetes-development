package api

import (
	"fmt"
	"sort"
)

// RecipeTask is how a task type runs as a `factory recipe`: the recipe,
// the one action of its result the CLI applies, how its runs are named,
// and the sandbox it runs in.
type RecipeTask struct {
	Recipe string
	// Action is applied by the CLI once the run finishes (--apply
	// --action): the watch writes nothing of the result itself.
	Action string
	// RunPrefix starts its run names (RunName).
	RunPrefix string
	// OwnSandbox is for a credentials: clone recipe, which runs in a
	// sandbox of its own (<recipe>-<repo>-<N>); the others run in the
	// PR's fix sandbox.
	OwnSandbox bool
	// Args are more of the command's arguments, after the recipe's.
	Args []string
	// Recorded is the task type the recipe records on its sandbox, when
	// its task-type: names one; else it records recipe-<Recipe>.
	Recorded string
}

// recipeTasks are the task types that run as `factory recipe`. The other,
// agent-chore, runs as `factory agent create`, which runs the agent or
// workflow recipe (ChoreRunName).
var recipeTasks = map[TaskType]RecipeTask{
	TypePRReview:      {Recipe: "review", Action: "submit-review", RunPrefix: "review", OwnSandbox: true},
	TypePRInvestigate: {Recipe: "care-ci", Action: "post-replies", RunPrefix: "investigate"},
	TypePRComments:    {Recipe: "care-comments", Action: "post-replies", RunPrefix: "comments"},
	TypePRIterate:     {Recipe: "care-rebase", Action: "post-replies", RunPrefix: "iterate"},
	// The PR opens ready for review, labelled as the classic factory fix labelled it:
	// the watch scans only labelled PRs.
	TypeIssueFix: {Recipe: "fix", Action: "open-pr", RunPrefix: "fix", Args: []string{"--pr-labels", "--input", "draft=false"}, Recorded: "fix"},
}

// For is how task t runs: rt, its type's, with the recipe, the action and
// the arguments the rules engine decided for it when it did (t.Recipe):
// --pr-labels for a {{pr-labels}} input, then each input, by name, as
// --input name=value. The run's name, sandbox and recorded type stay its
// type's.
func (rt RecipeTask) For(t *QueueTask) RecipeTask {
	if t.Recipe == "" {
		return rt
	}
	out := rt
	out.Recipe, out.Action, out.Args = t.Recipe, t.Apply, nil
	if t.PRLabels {
		out.Args = append(out.Args, "--pr-labels")
	}
	names := make([]string, 0, len(t.Inputs))
	for name := range t.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Args = append(out.Args, "--input", name+"="+t.Inputs[name])
	}
	return out
}

// RecordedType is the task type a run of rt records on its sandbox.
func (rt RecipeTask) RecordedType() string {
	if rt.Recorded != "" {
		return rt.Recorded
	}
	return "recipe-" + rt.Recipe
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
	return fmt.Sprintf("%s-%d-%s-%d", rt.RunPrefix, t.Number, sha, at.Unix())
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
