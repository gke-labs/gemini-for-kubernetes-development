package github

import (
	"context"
	"errors"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// ErrUnreadableFile is a file ReadFile cannot return: missing on the default
// branch, a directory, or too large for the contents API. It is the file's
// fault, not GitHub's, so retrying does not help.
var ErrUnreadableFile = errors.New("unreadable file")

// ReadFile returns a file on the default branch, as of the last commit that
// changed it, and that commit's SHA.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, string, error) {
	if !c.Ready() {
		return nil, "", errNoClient
	}

	commits, _, err := c.gh.Repositories.ListCommits(ctx, c.owner, c.repo, &githubv39.CommitsListOptions{
		Path:        path,
		ListOptions: githubv39.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, "", fmt.Errorf("finding the last commit to %s: %w", path, err)
	}
	if len(commits) == 0 {
		return nil, "", fmt.Errorf("%s: no such file on the default branch: %w", path, ErrUnreadableFile)
	}
	sha := commits[0].GetSHA()

	content, _, _, err := c.gh.Repositories.GetContents(ctx, c.owner, c.repo, path, &githubv39.RepositoryContentGetOptions{Ref: sha})
	switch {
	case IsNotFound(err):
		return nil, "", fmt.Errorf("%s at %.7s: no such file: %w", path, sha, ErrUnreadableFile)
	case err != nil:
		return nil, "", fmt.Errorf("fetching %s at %.7s: %w", path, sha, err)
	case content == nil:
		return nil, "", fmt.Errorf("%s is a directory: %w", path, ErrUnreadableFile)
	}
	text, err := content.GetContent()
	if err != nil {
		// The contents API leaves files above 1 MB undecoded.
		return nil, "", fmt.Errorf("%s at %.7s: %v: %w", path, sha, err, ErrUnreadableFile)
	}
	return []byte(text), sha, nil
}
