package api

import "fmt"

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
}

// recipeTasks are the task types that run as recipes. The others run
// the classic factory commands.
var recipeTasks = map[TaskType]RecipeTask{
	TypePRReview: {Recipe: "review", Action: "submit-review", RunPrefix: "review", OwnSandbox: true},
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
