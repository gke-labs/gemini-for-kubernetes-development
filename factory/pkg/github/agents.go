package github

import (
	"context"
	"fmt"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// AgentsDir is the repository directory holding agent definitions.
const AgentsDir = ".agents"

// ListAgentFiles returns the repository paths of the agent definitions under
// .agents/, e.g. ".agents/triage.md".
//
// A repository without that directory simply declares no agents, which GitHub
// reports as a 404 and this reports as an empty list: it is the ordinary state
// of most repositories, not a failure to look.
func (c *Client) ListAgentFiles(ctx context.Context) ([]string, error) {
	if !c.ready() {
		return nil, errNoClient
	}

	_, entries, _, err := c.gh.Repositories.GetContents(ctx, c.owner, c.repo, AgentsDir, &githubv39.RepositoryContentGetOptions{})
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s: %w", AgentsDir, err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.GetType() != "file" {
			continue
		}
		name := entry.GetName()
		if isAgentDefinition(name) {
			paths = append(paths, AgentsDir+"/"+name)
		}
	}
	return paths, nil
}

// ReadAgentFile returns the decoded contents of the agent definition at path.
func (c *Client) ReadAgentFile(ctx context.Context, path string) (string, error) {
	if !c.ready() {
		return "", errNoClient
	}

	content, _, _, err := c.gh.Repositories.GetContents(ctx, c.owner, c.repo, path, &githubv39.RepositoryContentGetOptions{})
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", path, err)
	}
	// GetContents returns a directory listing instead of file content when the
	// path is a directory, in which case there is nothing to decode.
	if content == nil {
		return "", fmt.Errorf("%s is not a file", path)
	}
	return content.GetContent()
}

// isAgentDefinition reports whether a file under .agents/ is a definition rather
// than a README or some other file that happens to live alongside them.
func isAgentDefinition(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".md")
}
