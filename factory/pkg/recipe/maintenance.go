package recipe

import (
	_ "embed"
	"fmt"
)

// WorkspaceCleanupRecipe is the recipe that makes room on a sandbox's
// workspace disk (diskcare): when to run it is the overseer's.
const WorkspaceCleanupRecipe = "workspace-cleanup"

// WorkspaceCleanupPath is where a repository keeps its own
// workspace-cleanup recipe, outside .agents/recipes: it is maintenance,
// not a recipe the board offers on issues and pull requests.
const WorkspaceCleanupPath = ".agents/maintenance/" + WorkspaceCleanupRecipe + ".yaml"

//go:embed maintenance/workspace-cleanup.yaml
var builtinWorkspaceCleanup []byte

// WorkspaceCleanup is the workspace-cleanup recipe to run: the
// repository's (its WorkspaceCleanupPath's content, repo) when it has one,
// else the built-in, which deletes nothing. It returns the recipe, its
// bytes and where it came from. It runs no agent: an ask: step is refused.
func WorkspaceCleanup(repo []byte) (*Recipe, []byte, string, error) {
	data, source := builtinWorkspaceCleanup, "built-in"
	if len(repo) > 0 {
		data, source = repo, WorkspaceCleanupPath
	}
	r, err := Parse(data)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", source, err)
	}
	for i, st := range r.Start.Steps {
		if st.Kind() == "ask" {
			return nil, nil, "", fmt.Errorf("%s: step %s asks the agent; a cleanup recipe runs no agent (run: and uses: steps only)", source, st.Label(i))
		}
	}
	return r, data, source, nil
}
