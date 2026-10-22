package github

import (
	"context"
	"fmt"
	"net/http"

	githubv39 "github.com/google/go-github/v39/github"
)

// AgentsTrees is the git tree SHA of each folder directly under owner/repo's
// .agents/ at its default branch, by name (recipes, workflows, …), in one
// call. A folder's SHA changes exactly when something under it does, so a
// reader that kept the SHA it last read a folder at need not read the
// folder again while it is unchanged. A repository with no .agents/ has
// none; a folder that does not exist has no entry.
func AgentsTrees(ctx context.Context, gh *githubv39.Client, owner, repo string) (map[string]string, error) {
	_, entries, resp, err := gh.Repositories.GetContents(ctx, owner, repo, AgentsDir, nil)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s %s: %w", owner, repo, AgentsDir, err)
	}
	trees := map[string]string{}
	for _, e := range entries {
		if e.GetType() == "dir" {
			trees[e.GetName()] = e.GetSHA()
		}
	}
	return trees, nil
}
