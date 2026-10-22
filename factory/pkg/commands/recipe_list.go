package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
	// Replaces is set on a repository's recipe in place of the built-in of
	// its name (replace mode): it runs as factory recipe <name>.
	Replaces bool `json:"replaces,omitempty"`
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

// catalogEntry is one recipe of the catalog recipe list prints, with the
// recipe it describes (nil for a file that is not one).
type catalogEntry struct {
	info RecipeInfo
	rec  *recipe.Recipe
}

// boardHidden reports whether a board has no button for rec: the watch
// runs warm (design/warm-workspace.md), factory agent create runs agent.
func boardHidden(rec *recipe.Recipe) bool {
	sb := rec.Sandbox
	return sb != nil && (sb.Kind == recipe.SandboxWarm || sb.Kind == recipe.SandboxAgent)
}

// builtinCatalog is every built-in recipe a board launches, by name.
func builtinCatalog() ([]catalogEntry, error) {
	var out []catalogEntry
	var recs []*recipe.Recipe
	for _, name := range recipe.BuiltinNames() {
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			return nil, err
		}
		if boardHidden(rec) {
			continue
		}
		recs = append(recs, rec)
		info := recipeInfo(rec)
		info.Source = "builtin"
		out = append(out, catalogEntry{info, rec})
	}
	if err := recipe.CheckSessions(recs); err != nil {
		return nil, err
	}
	return out, nil
}

// repoCatalog is the recipes of the repository repoURL names: each of its
// .agents/recipes files, a recipe or why it is not one.
func repoCatalog(ctx context.Context, repoURL string) ([]catalogEntry, error) {
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
	var out []catalogEntry
	for _, r := range recs {
		if r.err != nil {
			out = append(out, catalogEntry{info: RecipeInfo{Name: r.name, Source: r.path, Error: r.err.Error()}})
			continue
		}
		info := recipeInfo(r.rec)
		info.Source = r.path
		out = append(out, catalogEntry{info, r.rec})
	}
	return out, nil
}

// mergeCatalog is the catalog a board gets: the built-ins, and the
// repository's recipes after them - in replace mode a repository recipe of
// a built-in's name in the built-in's place, and one a board has no button
// for (warm, agent) left out. Recipes that share a session tag are held
// together (recipe.CheckSessions), the repository's with the built-ins
// they join: a group that does not hold has its repository recipes listed
// as not recipes, and the built-ins they replaced back in their place.
func mergeCatalog(builtins, repo []catalogEntry, replace bool) []RecipeInfo {
	out := slices.Clone(builtins)
	at := map[string]int{}
	for i, e := range out {
		at[e.info.Name] = i
	}
	original := map[string]catalogEntry{}
	fromRepo := map[string]bool{}
	for _, e := range repo {
		switch i, builtin := at[e.info.Name]; {
		case e.rec == nil:
			out = append(out, e)
		case boardHidden(e.rec):
		case replace && builtin:
			original[e.info.Name] = out[i]
			e.info.Replaces = true
			out[i] = e
			fromRepo[e.info.Name] = true
		default:
			out = append(out, e)
			fromRepo[e.info.Name] = true
		}
	}
	groups := map[string][]*recipe.Recipe{}
	joined := map[string]bool{}
	for _, e := range out {
		if e.rec != nil && e.rec.Session != "" {
			groups[e.rec.Session] = append(groups[e.rec.Session], e.rec)
			joined[e.rec.Session] = joined[e.rec.Session] || fromRepo[e.info.Name]
		}
	}
	for i, e := range out {
		if !fromRepo[e.info.Name] || e.rec == nil || e.rec.Session == "" || !joined[e.rec.Session] {
			continue
		}
		err := recipe.CheckSessions(groups[e.rec.Session])
		if err == nil {
			continue
		}
		if b, ok := original[e.info.Name]; ok {
			out[i] = b
			out = append(out, catalogEntry{info: RecipeInfo{Name: e.info.Name, Source: e.info.Source, Error: err.Error()}})
		} else {
			out[i] = catalogEntry{info: RecipeInfo{Name: e.info.Name, Source: e.info.Source, Error: err.Error()}}
		}
	}
	infos := make([]RecipeInfo, len(out))
	for i, e := range out {
		infos[i] = e.info
	}
	return infos
}

func newRecipeListCommand(ctx context.Context) *cobra.Command {
	var output, repoURL string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the recipes: the built-in ones, and with --url a repository's (--repo-recipes: off, augment, or replace, a repository recipe of a built-in's name in its place); what each runs on, its result and its session",
		Example: `  factory recipe list
  factory recipe list --url https://github.com/owner/repo -o json`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := checkRepoRecipeMode(); err != nil {
				return err
			}
			builtins, err := builtinCatalog()
			if err != nil {
				return err
			}
			var repo []catalogEntry
			if repoURL != "" && repoRecipeMode() != "off" {
				if _, err := ResolveRootFlags(c); err != nil {
					return err
				}
				if repo, err = repoCatalog(ctx, repoURL); err != nil {
					return err
				}
			}
			infos := mergeCatalog(builtins, repo, repoRecipeMode() == "replace")
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
