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
	// Args are more of the command's arguments, after the recipe's.
	Args []string
	// Recorded is the task type the recipe records on its sandbox, when
	// its task-type: names one; else it records recipe-<Recipe>.
	Recorded string
}

// recipeTasks are the task types that run as recipes. The others run
// the classic factory commands.
var recipeTasks = map[TaskType]RecipeTask{
	TypePRReview:      {Recipe: "review", Action: "submit-review", RunPrefix: "review", OwnSandbox: true},
	TypePRInvestigate: {Recipe: "care-ci", Action: "post-replies", RunPrefix: "investigate"},
	TypePRComments:    {Recipe: "care-comments", Action: "post-replies", RunPrefix: "comments"},
	TypePRIterate:     {Recipe: "care-rebase", Action: "post-replies", RunPrefix: "iterate"},
	// The PR opens ready for review, labelled as factory fix labelled it:
	// the watch scans only labelled PRs.
	TypeIssueFix: {Recipe: "fix", Action: "open-pr", RunPrefix: "fix", Args: []string{"--pr-labels", "--input", "draft=false"}, Recorded: "fix"},
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
