package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// reviseFlags are `factory recipe revise`'s flags.
type reviseFlags struct {
	task, runName, recipe string
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
apply the new result as any other.

The session must not be mid-turn; the revise fails before sending anything
if it is.`,
		Example: `  # Rewrite the newest plan in the issue's sandbox from its conversation
  factory recipe revise https://github.com/owner/repo/issues/123 plan

  # A given task, under a run name
  factory recipe revise fix-repo-123 plan --task recipe-plan-20261004-120000-ab12 --run-name revise-1`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runRevise(ctx, c, args[0], args[1], f)
		},
	}
	cmd.Flags().StringVar(&f.task, "task", "", "The task to revise (default: the newest whose recipe has this revise)")
	cmd.Flags().StringVar(&f.runName, "run-name", "", "Names this run: running again with the same name follows that task, or prints its result, instead of starting another")
	cmd.Flags().StringVar(&f.recipe, "recipe", "", "The recipe, a file or a built-in's name (default: the built-in the task ran)")
	return cmd
}

func runRevise(ctx context.Context, c *cobra.Command, target, reviseID string, f reviseFlags) error {
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
	sandboxName := sb.Name()

	var recipeBytes []byte
	var rec *recipe.Recipe
	if f.recipe != "" {
		if recipeBytes, rec, err = loadRecipe(f.recipe); err != nil {
			return err
		}
	}
	entries, err := sb.List(ctx)
	if err != nil {
		return err
	}
	started, err := revisedTask(entries, f.task, reviseID, rec)
	if err != nil {
		return fmt.Errorf("sandbox %s: %w", sandboxName, err)
	}
	if rec == nil {
		if recipeBytes, rec, err = recipe.Builtin(started.Recipe); err != nil {
			return fmt.Errorf("task %s ran recipe %s, which is not a built-in one: give it with --recipe", started.ID, started.Recipe)
		}
	}
	if rec.Name != started.Recipe {
		return fmt.Errorf("task %s ran recipe %s, not %s", started.ID, started.Recipe, rec.Name)
	}
	if _, err := rec.Steps(reviseID); err != nil {
		return err
	}
	// The conversation every revise of it continues.
	session := started.Session
	if session == "" {
		session = started.ID
	}
	it, err := parseRecipeTarget(started.URL)
	if err != nil {
		return fmt.Errorf("task %s: %w", started.ID, err)
	}

	if f.runName != "" {
		e, ok, err := runByName(entries, f.runName, rec.Name, started.URL)
		if err != nil {
			return err
		}
		if ok {
			return resumeNamedRun(ctx, sb, nil, rec, sandboxName, e, applyMode{})
		}
	}
	if err := refuseIfBusy(ctx, sb, sandboxName); err != nil {
		return err
	}

	inputsJSON, err := sb.ReadFile(ctx, started.ID, spool.InputsFile)
	if err != nil {
		return fmt.Errorf("reading task %s's inputs: %w", started.ID, err)
	}
	inputs := map[string]string{}
	if err := json.Unmarshal(inputsJSON, &inputs); err != nil {
		return fmt.Errorf("parsing task %s's inputs: %w", started.ID, err)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	// No secrets: a revise has no clone step (recipe.Validate), so a
	// credentials: clone recipe's token has nowhere to go.
	envMap, _, err := recipeEnv(secret, it, rec.Credentials)
	if err != nil {
		return err
	}

	task := newSpoolTask(rec.Name, f.runName, started.URL)
	task.Revise, task.Session = reviseID, session
	task.Output = rec.OutputDecl()
	task.TaskType = rec.TaskType
	fmt.Printf("Revising %s in the session of task %s (task %s)...\n", reviseID, session, task.ID)
	if done, err := startRecipeTask(ctx, kubeClient, sb, it, rec, task, recipeBytes, inputs, envMap, nil); err != nil || !done {
		return err
	}
	if rec.TaskOutput != nil {
		fmt.Printf("Its %s result, to look at and apply:\n  factory sandbox task output %s -n %s --task %s | factory apply -f -\n", rec.TaskOutput.Kind, sandboxName, rootFlags.Namespace, task.ID)
	}
	return nil
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
