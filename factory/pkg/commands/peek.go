package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// newPeekCommand prints the tree SHA of each folder under a repository's
// .agents/ at its default branch: one GitHub call that says whether what
// factory reads from there (recipes, and the like) changed since a SHA
// was last seen, without reading any of it.
func newPeekCommand(ctx context.Context) *cobra.Command {
	var repoURL, output string
	cmd := &cobra.Command{
		Use:   "peek",
		Short: "Print the git tree SHA of each folder under a repository's .agents/ at its default branch",
		Long: `Print the git tree SHA of each folder under a repository's .agents/ at its
default branch (recipes, workflows, runbooks, …), in one GitHub call. A
folder's SHA changes exactly when something under it does: a reader that
kept the SHA it last read a folder at reads it again only when it differs.`,
		Example: `  factory peek --url https://github.com/owner/repo
  factory peek --url https://github.com/owner/repo -o json`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			it, err := parseRecipeTarget(repoURL)
			if err != nil {
				return err
			}
			if !it.IsRepo() {
				return fmt.Errorf("--url %s: want a repository URL", repoURL)
			}
			gh, err := github.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("creating github client: %w", err)
			}
			trees, err := github.AgentsTrees(ctx, gh, it.Owner, it.Repo)
			if err != nil {
				return err
			}
			if output == "json" {
				return json.NewEncoder(os.Stdout).Encode(trees)
			}
			names := make([]string, 0, len(trees))
			for n := range trees {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Printf("%s/%s\t%s\n", github.AgentsDir, n, trees[n])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoURL, "url", "", "A repository URL")
	cmd.Flags().StringVarP(&output, "output", "o", "", "json: a JSON object of folder name to tree SHA")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}
