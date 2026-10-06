package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// RecipeInfo is one built-in recipe as `factory recipe list -o json`
// prints it: what a board needs to offer it, run it and revise it.
type RecipeInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// On is what it runs on (recipe.On); empty, anything.
	On     []string          `json:"on,omitempty"`
	Inputs []RecipeInputInfo `json:"inputs,omitempty"`
	// Kind is its task output's kind, if it declares one.
	Kind    string             `json:"kind,omitempty"`
	Revises []RecipeReviseInfo `json:"revises,omitempty"`
}

// RecipeInputInfo is one declared input. Revise marks one only revises
// take, which a launch does not ask for.
type RecipeInputInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type,omitempty"`
	Revise      bool   `json:"revise,omitempty"`
}

// RecipeReviseInfo is one revise, with the inputs it asks for.
type RecipeReviseInfo struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Inputs []string `json:"inputs,omitempty"`
}

func recipeInfo(rec *recipe.Recipe) RecipeInfo {
	info := RecipeInfo{Name: rec.Name, Label: rec.DisplayLabel(), On: rec.On}
	if rec.TaskOutput != nil {
		info.Kind = rec.TaskOutput.Kind
	}
	for _, name := range sortedInputNames(rec) {
		in := rec.Inputs[name]
		info.Inputs = append(info.Inputs, RecipeInputInfo{Name: name, Description: in.Description, Default: in.Default, Required: in.Required, Type: in.Type, Revise: in.Revise})
	}
	for _, rv := range rec.Revise {
		info.Revises = append(info.Revises, RecipeReviseInfo{ID: rv.ID, Label: rv.Label, Inputs: rv.Inputs})
	}
	return info
}

// builtinRecipeInfos describes every built-in recipe, by name.
func builtinRecipeInfos() ([]RecipeInfo, error) {
	var infos []RecipeInfo
	for _, name := range recipe.BuiltinNames() {
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			return nil, err
		}
		infos = append(infos, recipeInfo(rec))
	}
	return infos, nil
}

func newRecipeListCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the built-in recipes: what each runs on, its result and its revises",
		Example: `  factory recipe list
  factory recipe list -o json`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			infos, err := builtinRecipeInfos()
			if err != nil {
				return err
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
			fmt.Fprintln(w, "NAME\tON\tRESULT\tREVISES")
			for _, info := range infos {
				on := strings.Join(info.On, ",")
				if on == "" {
					on = "any"
				}
				var revises []string
				for _, rv := range info.Revises {
					revises = append(revises, rv.ID)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", info.Name, on, orDash(info.Kind), orDash(strings.Join(revises, ",")))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}
