package api

import (
	"fmt"
	"sort"
)

// RecipeArgs are the arguments of the run the rules engine decided for t,
// after `recipe <t.Recipe>` and the CLI's own: --apply --action for its
// apply, --pr-labels for a {{pr-labels}} input, then each input, by name,
// as --input name=value. nil for a task no rule decided (no recipe).
func RecipeArgs(t *QueueTask) []string {
	if t.Recipe == "" {
		return nil
	}
	var args []string
	apply := t.Apply
	if apply == "" && t.Type == TypeAgentChore {
		apply = "open-pr"
	}
	if apply != "" {
		args = append(args, "--apply", "--action", apply)
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

// RuleRunName names the run of a rule task t: its rule, its item, the
// commit it is for and when t was enqueued. A watcher restarted mid-run
// requeues t under the same name, and the CLI follows that run, or applies
// its result, instead of starting another; t enqueued again (after a
// failure, say) starts a new one.
func RuleRunName(t *QueueTask) string {
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
	prefix := t.Rule
	if prefix == "" {
		prefix = t.Recipe
	}
	if prefix == "" {
		prefix = "rule"
	}
	return fmt.Sprintf("%s-%d-%s-%d", prefix, t.Number, sha, at.Unix())
}

// RunNameOf is t's run name (factory --run-name): the one stamped on it
// when it was queued (TaskQueueManager.Enqueue), else by what it runs -
// ChoreRunName for an agent definition, RuleRunName for a rule's recipe -
// "" for a task that runs neither.
func RunNameOf(t *QueueTask) string {
	switch {
	case t.RunName != "":
		return t.RunName
	case t.AgentFile != "" && (t.Recipe == "" || t.Rule == "workflow"):
		return ChoreRunName(t)
	case t.Recipe != "":
		return RuleRunName(t)
	}
	return ""
}

// ChoreRunName names a run of an agent-chore task t (factory agent create
// --run-name) as RuleRunName names a rule task's: by its issue, if it is a
// workflow's, and when t was enqueued. Each agent has a sandbox of its
// own, so the name need not say which agent it is.
func ChoreRunName(t *QueueTask) string {
	at := t.EnqueuedAt
	if at.IsZero() {
		at = t.CreatedAt
	}
	return fmt.Sprintf("agent-%d-%d", t.Number, at.Unix())
}
