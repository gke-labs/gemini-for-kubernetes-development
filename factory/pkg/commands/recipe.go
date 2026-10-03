package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
)

// NewRecipeCommand groups the recipe runner's commands.
func NewRecipeCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "recipe",
		Short:  "Run tasks as recipes: steps around one agent session (experimental)",
		Hidden: true,
	}
	cmd.AddCommand(newRecipeExecCommand(ctx))
	return cmd
}

// newRecipeExecCommand runs a recipe inside a sandbox. The CLI starts it
// through envd where it would start a task script, with the same
// environment, so detaching, the log and restart recovery are unchanged.
func newRecipeExecCommand(ctx context.Context) *cobra.Command {
	var recipePath, inputsPath, taskDir string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run a recipe in this sandbox (started by the CLI, not by hand)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runRecipeExec(c.Context(), recipePath, inputsPath, taskDir)
		},
	}
	cmd.Flags().StringVar(&recipePath, "recipe", "", "Recipe file")
	cmd.Flags().StringVar(&inputsPath, "inputs", "", "Inputs file (a JSON object of strings)")
	cmd.Flags().StringVar(&taskDir, "task-dir", "", "Task directory: step logs, the session transcript, captured replies")
	_ = cmd.MarkFlagRequired("recipe")
	_ = cmd.MarkFlagRequired("task-dir")
	return cmd
}

func runRecipeExec(ctx context.Context, recipePath, inputsPath, taskDir string) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return err
	}
	rec, err := recipe.Parse(data)
	if err != nil {
		return err
	}
	inputs := map[string]string{}
	if inputsPath != "" {
		b, err := os.ReadFile(inputsPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &inputs); err != nil {
			return fmt.Errorf("parsing inputs: %w", err)
		}
	}
	repoName := os.Getenv("REPO_NAME")
	if repoName == "" {
		return fmt.Errorf("REPO_NAME is not set")
	}
	repoDir := filepath.Join("/workspaces", repoName)
	stepScript, err := tasks.GetRecipeStepScript()
	if err != nil {
		return err
	}

	engine := os.Getenv("ENGINE")
	if engine == "" {
		engine = "gemini"
	}
	// The first model only: runEngine's fallback down the list is not
	// here yet.
	model := ""
	if models := strings.Fields(os.Getenv("MODELS")); len(models) > 0 && models[0] != "default" {
		model = models[0]
	}

	r := &recipe.Runner{
		Exec: &recipe.SandboxExecutor{
			StepScript: stepScript,
			TaskDir:    taskDir,
			RepoDir:    repoDir,
			Env:        os.Environ(),
		},
		StartSession: func(ctx context.Context) (recipe.Session, error) {
			return recipe.StartACPSession(ctx, engine, model, os.Getenv("GEMINI_API_KEY"), repoDir, taskDir)
		},
		TaskDir: taskDir,
		Inputs:  inputs,
		Log:     os.Stdout,
	}
	return r.Run(ctx, rec)
}
