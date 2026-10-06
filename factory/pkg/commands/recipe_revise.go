package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// reviseFlags are `factory recipe revise`'s flags.
type reviseFlags struct {
	task, runName, recipe string
	inputArgs             []string
}

// newRecipeReviseCommand runs one of a recipe's revises: its steps, asked
// into the session of a task that ran the recipe, as a new task with a
// result of its own.
func newRecipeReviseCommand(ctx context.Context) *cobra.Command {
	var f reviseFlags
	cmd := &cobra.Command{
		Use:   "revise <sandbox | issue or PR url> <revise>",
		Short: "Rewrite a recipe task's result from the conversation continued in its session",
		Long: `Rewrite a recipe task's result from its conversation.

A member can keep talking to a recipe task's agent once the task has ended
(the board's Continue session). A revise — one of the recipe's revise:
parts, such as the plan recipe's "plan" (Update plan) — asks into that
same session, as the next turn, and captures the result again.

It runs as a new task in the same sandbox, with the started task's inputs,
and leaves a task output of its own; the started task's stays as it was.
Revising a revise continues the same conversation. Nothing is posted:
apply the new result as any other. --input sets an input the revise
takes, such as the fix recipe's iterate instruction.

A fix's revises (iterate, address-comments, fix-ci, rebase) push to the
branch the fix pushed, leased against the head last pushed, and their
Change is about the fix's PR (the sandbox's PR alias): post-replies posts
its replies and report there.

The session must not be mid-turn; the revise fails before sending anything
if it is.`,
		Example: `  # Rewrite the newest plan in the issue's sandbox from its conversation
  factory recipe revise https://github.com/owner/repo/issues/123 plan

  # A given task, under a run name
  factory recipe revise fix-repo-123 plan --task recipe-plan-20261004-120000-ab12 --run-name revise-1

  # Change a fix's PR as asked, then post what it says
  factory recipe revise https://github.com/owner/repo/pull/456 iterate --input instruction="rename foo to bar"
  factory sandbox task output fix-repo-123 | factory apply -f -`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runRevise(ctx, c, args[0], args[1], f)
		},
	}
	cmd.Flags().StringVar(&f.task, "task", "", "The task to revise (default: the newest whose recipe has this revise)")
	cmd.Flags().StringVar(&f.runName, "run-name", "", "Names this run: running again with the same name follows that task, or prints its result, instead of starting another")
	cmd.Flags().StringVar(&f.recipe, "recipe", "", "The recipe, a file or a built-in's name (default: the built-in the task ran)")
	cmd.Flags().StringArrayVar(&f.inputArgs, "input", nil, "An input as name=value; overrides the started task's. Repeatable.")
	return cmd
}

func runRevise(ctx context.Context, c *cobra.Command, target, reviseID string, f reviseFlags) error {
	overrides, err := parseInputArgs(f.inputArgs)
	if err != nil {
		return err
	}
	sb, err := connectTaskSandbox(ctx, c, target)
	if err != nil {
		return err
	}
	defer sb.Close()
	if rootFlags.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
		defer cancel()
	}
	_, err = reviseIn(ctx, sb, reviseID, f, overrides)
	return err
}

// reviseIn runs revise reviseID in sb, as runRevise does, and returns
// the task it started, or the run name's.
func reviseIn(ctx context.Context, sb taskapi.Sandbox, reviseID string, f reviseFlags, overrides map[string]string) (string, error) {
	sandboxName := sb.Name()
	var err error

	var recipeBytes []byte
	var rec *recipe.Recipe
	if f.recipe != "" {
		if recipeBytes, rec, err = loadRecipe(f.recipe); err != nil {
			return "", err
		}
	}
	entries, err := sb.List(ctx)
	if err != nil {
		return "", err
	}
	started, err := revisedTask(entries, f.task, reviseID, rec)
	if err != nil {
		return "", fmt.Errorf("sandbox %s: %w", sandboxName, err)
	}
	if rec == nil {
		if recipeBytes, rec, err = recipe.Builtin(started.Recipe); err != nil {
			return "", fmt.Errorf("task %s ran recipe %s, which is not a built-in one: give it with --recipe", started.ID, started.Recipe)
		}
	}
	if rec.Name != started.Recipe {
		return "", fmt.Errorf("task %s ran recipe %s, not %s", started.ID, started.Recipe, rec.Name)
	}
	if _, err := rec.Steps(reviseID); err != nil {
		return "", err
	}
	// The conversation every revise of it continues.
	session := started.Session
	if session == "" {
		session = started.ID
	}
	it, err := parseRecipeTarget(started.URL)
	if err != nil {
		return "", fmt.Errorf("task %s: %w", started.ID, err)
	}

	if f.runName != "" {
		e, ok, err := runByName(entries, f.runName, rec.Name, started.URL)
		if err != nil {
			return "", err
		}
		if ok {
			return e.ID, resumeNamedRun(ctx, sb, nil, rec, sandboxName, e, applyMode{})
		}
	}
	if err := refuseIfBusy(ctx, sb, sandboxName); err != nil {
		return "", err
	}

	// The start's inputs, so that one revise's --input is not the next's.
	inputsFrom := session
	inputsJSON, err := sb.ReadFile(ctx, inputsFrom, spool.InputsFile)
	if err != nil {
		inputsFrom = started.ID
		inputsJSON, err = sb.ReadFile(ctx, inputsFrom, spool.InputsFile)
	}
	if err != nil {
		return "", fmt.Errorf("reading task %s's inputs: %w", inputsFrom, err)
	}
	inputs := map[string]string{}
	if err := json.Unmarshal(inputsJSON, &inputs); err != nil {
		return "", fmt.Errorf("parsing task %s's inputs: %w", inputsFrom, err)
	}
	if inputs, err = rec.ResolveInputs(inputs, overrides); err != nil {
		return "", err
	}
	if rec.TaskOutput != nil && rec.TaskOutput.Kind == "Change" {
		read := func(id, name string) ([]byte, error) { return sb.ReadFile(ctx, id, name) }
		if err := changeInputs(inputs, entries, session, rec.Name, read); err != nil {
			return "", fmt.Errorf("sandbox %s: %w", sandboxName, err)
		}
		if u := sandboxPRURL(ctx, sandboxName, it); u != "" {
			inputs["pr_url"] = u
		}
	}
	// address-comments is handed the feedback still waiting on the PR,
	// which it answers, and which is acknowledged (👀) once handed over,
	// so `factory pr watch` does not pick it up again meanwhile.
	var handed []feedback.Item
	var reactor feedback.Reactor
	if _, ok := rec.Inputs["feedback"]; ok && reviseID == addressCommentsRevise && inputs["pr_url"] != "" {
		if handed, reactor, err = handFeedback(ctx, inputs, it.Owner, it.Repo); err != nil {
			return "", err
		}
		fmt.Printf("Handing the revise %d piece(s) of feedback still waiting on %s\n", len(handed), inputs["pr_url"])
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return "", fmt.Errorf("creating k8s client: %w", err)
	}
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	// No secrets: a revise has no clone step (recipe.Validate), so a
	// credentials: clone recipe's token has nowhere to go.
	envMap, _, err := recipeEnv(secret, it, rec.Credentials)
	if err != nil {
		return "", err
	}

	task := newSpoolTask(rec.Name, f.runName, started.URL)
	task.Revise, task.Session = reviseID, session
	task.Output = rec.OutputDecl()
	task.TaskType = rec.TaskType
	fmt.Printf("Revising %s in the session of task %s (task %s)...\n", reviseID, session, task.ID)
	if len(handed) > 0 {
		if err := feedback.React(ctx, reactor, handed, conventions.ReactionAcknowledged); err != nil {
			fmt.Printf("Warning: acknowledging the feedback: %v\n", err)
		}
	}
	if done, err := startRecipeTask(ctx, kubeClient, sb, it, rec, task, recipeBytes, inputs, envMap, nil); err != nil || !done {
		return task.ID, err
	}
	if rec.TaskOutput != nil {
		fmt.Printf("Its %s result, to look at and apply:\n  factory sandbox task output %s -n %s --task %s | factory apply -f -\n", rec.TaskOutput.Kind, sandboxName, rootFlags.Namespace, task.ID)
	}
	return task.ID, nil
}

// revisedTask is the task a revise revises: the one named, or else the
// newest (entries are newest first) that ran a recipe with the revise —
// rec, or with none, a built-in one.
func revisedTask(entries []spool.Entry, id, reviseID string, rec *recipe.Recipe) (spool.Entry, error) {
	if id != "" {
		e, err := spool.Find(entries, id, "")
		if err != nil {
			return e, err
		}
		if e.Recipe == "" {
			return e, fmt.Errorf("task %s ran no recipe", id)
		}
		return e, nil
	}
	for _, e := range entries {
		r := rec
		if r == nil {
			var err error
			if _, r, err = recipe.Builtin(e.Recipe); err != nil {
				continue
			}
		}
		if e.Recipe != r.Name {
			continue
		}
		if _, err := r.Steps(reviseID); err == nil {
			return e, nil
		}
	}
	return spool.Entry{}, fmt.Errorf("no task ran a recipe with a revise %q", reviseID)
}

// changeInputs hands a Change recipe's revise the push its session last
// made — the newest of its tasks that pushed — as pushed_fork,
// pushed_branch, pushed_base and pushed_head: the branch it pushes to,
// and the head it leases against. pushed_title and pushed_body are that
// task's Change's, which the revise's Change keeps unless its agent
// writes new ones.
func changeInputs(inputs map[string]string, entries []spool.Entry, session, recipeName string, read func(id, name string) ([]byte, error)) error {
	for _, e := range entries {
		if e.Recipe != recipeName || (e.ID != session && e.Session != session) {
			continue
		}
		data, err := read(e.ID, taskoutput.PushedFile)
		if err != nil || len(data) == 0 {
			continue
		}
		var p taskoutput.Pushed
		if err := json.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("task %s's %s: %w", e.ID, taskoutput.PushedFile, err)
		}
		if p.Branch == "" || p.Head == "" {
			return fmt.Errorf("task %s's %s records no branch or head", e.ID, taskoutput.PushedFile)
		}
		inputs["pushed_fork"], inputs["pushed_branch"], inputs["pushed_base"], inputs["pushed_head"] = p.Fork, p.Branch, p.Base, p.Head
		inputs["pushed_title"], inputs["pushed_body"] = "", ""
		if out, err := read(e.ID, taskoutput.File); err == nil {
			if docs, err := taskoutput.Parse(out); err == nil && len(docs) > 0 {
				if c, err := docs[0].ChangeSpec(); err == nil {
					inputs["pushed_title"], inputs["pushed_body"] = c.Title, c.Body
				}
			}
		}
		return nil
	}
	return fmt.Errorf("no task in session %s pushed anything; there is no branch to revise", session)
}

// sandboxPRURL is the PR a sandbox is aliased to (open-pr's htmlURL and
// pr annotations), or "".
func sandboxPRURL(ctx context.Context, name string, it githubItem) string {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return ""
	}
	sb, err := k8s.NewManager(kubeClient).GetSandbox(ctx, rootFlags.Namespace, name)
	if err != nil {
		return ""
	}
	return prURLOf(sb.GetAnnotations(), it)
}

func prURLOf(annotations map[string]string, it githubItem) string {
	if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
		return u
	}
	if n, err := strconv.Atoi(annotations["pr"]); err == nil && n > 0 {
		return fmt.Sprintf("https://github.com/%s/%s/pull/%d", it.Owner, it.Repo, n)
	}
	return ""
}
