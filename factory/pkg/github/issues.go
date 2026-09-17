package github

import (
	"context"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// GetIssue returns a single issue. GitHub also serves pull requests from this
// endpoint, so the result may describe a PR; test PullRequestLinks to tell.
//
// Callers that need to tell "does not exist" apart from a transient failure
// should test the error with IsNotFound.
func (c *Client) GetIssue(ctx context.Context, number int) (*githubv39.Issue, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	issue, _, err := c.gh.Issues.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetching issue #%d: %w", number, err)
	}
	return issue, nil
}

// ListIssues returns one page of issues matching opts, along with the response
// so the caller can follow pagination.
//
// Pagination is left to the caller here, unlike the other list helpers on this
// type. Scanners page these queries themselves so they can stop early once they
// have enough candidates for a cycle, rather than draining a backlog of
// thousands of issues before doing any work.
func (c *Client) ListIssues(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	if !c.Ready() {
		return nil, nil, errNoClient
	}
	return c.gh.Issues.ListByRepo(ctx, c.owner, c.repo, opts)
}

// AddLabels applies labels to an issue or pull request. Labels already present
// are left alone rather than reported as an error.
func (c *Client) AddLabels(ctx context.Context, number int, labels []string) error {
	if !c.Ready() {
		return errNoClient
	}

	if _, _, err := c.gh.Issues.AddLabelsToIssue(ctx, c.owner, c.repo, number, labels); err != nil {
		return fmt.Errorf("adding labels %v to #%d: %w", labels, number, err)
	}
	return nil
}

// RemoveLabel removes a single label from an issue or pull request. Removing a
// label that is not applied reports a not-found error, which callers can
// recognise with IsNotFound and treat as success.
func (c *Client) RemoveLabel(ctx context.Context, number int, label string) error {
	if !c.Ready() {
		return errNoClient
	}

	if _, err := c.gh.Issues.RemoveLabelForIssue(ctx, c.owner, c.repo, number, label); err != nil {
		return fmt.Errorf("removing label %q from #%d: %w", label, number, err)
	}
	return nil
}

// AddAssignees assigns users to an issue or pull request.
func (c *Client) AddAssignees(ctx context.Context, number int, assignees []string) error {
	if !c.Ready() {
		return errNoClient
	}

	if _, _, err := c.gh.Issues.AddAssignees(ctx, c.owner, c.repo, number, assignees); err != nil {
		return fmt.Errorf("assigning %v to #%d: %w", assignees, number, err)
	}
	return nil
}

// RemoveAssignees unassigns users from an issue or pull request.
func (c *Client) RemoveAssignees(ctx context.Context, number int, assignees []string) error {
	if !c.Ready() {
		return errNoClient
	}

	if _, _, err := c.gh.Issues.RemoveAssignees(ctx, c.owner, c.repo, number, assignees); err != nil {
		return fmt.Errorf("unassigning %v from #%d: %w", assignees, number, err)
	}
	return nil
}

// SearchIssues runs a GitHub search query. The query is passed through as
// written, so callers are responsible for scoping it to this repository.
func (c *Client) SearchIssues(ctx context.Context, query string, opts *githubv39.SearchOptions) (*githubv39.IssuesSearchResult, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	result, _, err := c.gh.Search.Issues(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("searching issues: %w", err)
	}
	return result, nil
}
