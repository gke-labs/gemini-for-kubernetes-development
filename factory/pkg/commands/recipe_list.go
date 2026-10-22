package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// RecipeInfo is one built-in recipe as `factory recipe list -o json`
// prints it: what a board needs to offer it and run it.
type RecipeInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// On is what it runs on (recipe.On); empty, anything.
	On     []string          `json:"on,omitempty"`
	Inputs []RecipeInputInfo `json:"inputs,omitempty"`
	// Kind is its task output's kind, if it declares one.
	Kind string `json:"kind,omitempty"`
	// TaskType is what its runs are recorded under on the sandbox
	// (sandbox.gemini.google.com/<taskType>-run), and Credentials its
	// recipe.Credentials when not full: a PR's credentials: clone recipe
	// runs in a sandbox of its own (RecipeSandboxName), not the PR's fix
	// sandbox.
	TaskType    string `json:"taskType"`
	Credentials string `json:"credentials,omitempty"`
	// Session is its session tag (recipe.Session), if it has one: the
	// group a board shows as one, whose runs share one conversation and
	// TaskType.
	Session string `json:"session,omitempty"`
	// ApplicableWhen is which items it applies to (recipe.ApplicableWhen),
	// its label spelled for this factory's trigger label; unset, every one
	// On names.
	ApplicableWhen *RecipeApplicableInfo `json:"applicableWhen,omitempty"`
	// OnFinish is the action a board's runner applies when a run ends
	// (task-output.on-finish), and Timeout how long a run may take
	// (recipe.Timeout); unset, none and the runner's default.
	OnFinish string `json:"onFinish,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
	// Source is where it comes from: "builtin", or the path of a
	// repository's recipe file (.agents/recipes/<name>.yaml).
	Source string `json:"source"`
	// Error says why a repository's file is not a recipe it can run; the
	// rest of its description is then empty but its name.
	Error string `json:"error,omitempty"`
}

// RecipeApplicableInfo is which items a recipe applies to: those with one
// of Labels.
type RecipeApplicableInfo struct {
	Labels []string `json:"labels"`
}

// RecipeInputInfo is one declared input.
type RecipeInputInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type,omitempty"`
	// From is the task output kind whose result the input takes.
	From string `json:"from,omitempty"`
}

func recipeInfo(rec *recipe.Recipe) RecipeInfo {
	info := RecipeInfo{Name: rec.Name, Label: rec.DisplayLabel(), On: rec.On, TaskType: rec.TaskType, Session: rec.Session, Timeout: rec.Timeout}
	if labels, err := applicableLabels(rec, watchTriggerLabel()); err == nil && len(labels) > 0 {
		info.ApplicableWhen = &RecipeApplicableInfo{Labels: labels}
	}
	if info.TaskType == "" {
		info.TaskType = "recipe-" + rec.SessionName()
	}
	if rec.Credentials != recipe.CredentialsFull {
		info.Credentials = rec.Credentials
	}
	if rec.TaskOutput != nil {
		info.Kind = rec.TaskOutput.Kind
		info.OnFinish = rec.TaskOutput.OnFinish
	}
	for _, name := range sortedInputNames(rec) {
		in := rec.Inputs[name]
		info.Inputs = append(info.Inputs, RecipeInputInfo{Name: name, Description: in.Description, Default: in.Default, Required: in.Required, Type: in.Type, From: in.From})
	}
	return info
}

// builtinRecipeInfos describes every built-in recipe, by name.
func builtinRecipeInfos() ([]RecipeInfo, error) {
	var infos []RecipeInfo
	var recs []*recipe.Recipe
	for _, name := range recipe.BuiltinNames() {
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			return nil, err
		}
		if sb := rec.Sandbox; sb != nil && (sb.Kind == recipe.SandboxWarm || sb.Kind == recipe.SandboxAgent) {
			// The watch runs warm (design/warm-workspace.md), factory
			// agent create runs agent: a board has no button for either.
			continue
		}
		recs = append(recs, rec)
		info := recipeInfo(rec)
		info.Source = "builtin"
		infos = append(infos, info)
	}
	if err := recipe.CheckSessions(recs); err != nil {
		return nil, err
	}
	return infos, nil
}

// repoRecipeInfos describes the recipes of the repository repoURL names:
// each of its .agents/recipes files, a recipe or why it is not one.
func repoRecipeInfos(ctx context.Context, repoURL string) ([]RecipeInfo, error) {
	it, err := parseRecipeTarget(repoURL)
	if err != nil {
		return nil, err
	}
	gh, err := github.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating github client: %w", err)
	}
	recs, err := listRepoRecipes(ctx, gh, it.Owner, it.Repo)
	if err != nil {
		return nil, err
	}
	var infos []RecipeInfo
	for _, r := range recs {
		if r.err != nil {
			infos = append(infos, RecipeInfo{Name: r.name, Source: r.path, Error: r.err.Error()})
			continue
		}
		info := recipeInfo(r.rec)
		info.Source = r.path
		infos = append(infos, info)
	}
	return infos, nil
}

func newRecipeListCommand(ctx context.Context) *cobra.Command {
	var output, repoURL string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the recipes: the built-in ones, and with --url a repository's; what each runs on, its result and its session",
		Example: `  factory recipe list
  factory recipe list --url https://github.com/owner/repo -o json`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			infos, err := builtinRecipeInfos()
			if err != nil {
				return err
			}
			if repoURL != "" {
				if _, err := ResolveRootFlags(c); err != nil {
					return err
				}
				repo, err := repoRecipeInfos(ctx, repoURL)
				if err != nil {
					return err
				}
				infos = append(infos, repo...)
			}
			switch output {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			case "":
			default:
				return fmt.Errorf("unknown output format %q (want json)", output)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSOURCE\tON\tRESULT\tSESSION")
			for _, info := range infos {
				if info.Error != "" {
					fmt.Fprintf(w, "%s\t%s\t-\t-\t(not a recipe: %s)\n", info.Name, info.Source, info.Error)
					continue
				}
				on := strings.Join(info.On, ",")
				if on == "" {
					on = "any"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", info.Name, info.Source, on, orDash(info.Kind), orDash(info.Session))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	cmd.Flags().StringVar(&repoURL, "url", "", "A repository URL: list its own recipes too (.agents/recipes at its default branch)")
	return cmd
}
