package github

import (
	"context"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// FileContent returns the decoded contents of a file in the bound repository.
// ref may be a branch, tag or SHA, or empty to read the default branch.
func (c *Client) FileContent(ctx context.Context, path, ref string) (string, error) {
	if !c.Ready() {
		return "", errNoClient
	}
	return c.FileContentIn(ctx, c.owner, c.repo, path, ref)
}

// FileContentIn reads a file out of a repository other than the bound one,
// using this client's credentials.
//
// It exists for the one kind of input that names its own repository: a workflow
// or agent definition referenced by a github.com URL. Reading a file is
// deliberately the only thing this type will do outside the repository it is
// bound to - anything else has to be done by a Client bound to that repository.
func (c *Client) FileContentIn(ctx context.Context, owner, repo, path, ref string) (string, error) {
	if !c.Ready() {
		return "", errNoClient
	}

	content, _, _, err := c.gh.Repositories.GetContents(ctx, owner, repo, path, &githubv39.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return "", fmt.Errorf("fetching %s from %s/%s: %w", path, owner, repo, err)
	}
	// GetContents returns a directory listing instead of file content when the
	// path is a directory, in which case there is nothing to decode.
	if content == nil {
		return "", fmt.Errorf("%s is not a file", path)
	}
	return content.GetContent()
}
