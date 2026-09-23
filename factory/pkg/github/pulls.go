package github

import (
	"context"
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// GetPullRequest returns a single pull request. Callers that need to tell "does
// not exist" apart from a transient failure should test the error with
// IsNotFound.
func (c *Client) GetPullRequest(ctx context.Context, number int) (*githubv39.PullRequest, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	pr, _, err := c.gh.PullRequests.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetching PR #%d: %w", number, err)
	}
	return pr, nil
}

// ListOpenPRs returns every open pull request in the repository, following
// pagination.
func (c *Client) ListOpenPRs(ctx context.Context) ([]*githubv39.PullRequest, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.PullRequest
	opt := &githubv39.PullRequestListOptions{
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		prs, resp, err := c.gh.PullRequests.List(ctx, c.owner, c.repo, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, prs...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opt.Page = resp.NextPage
	}
}

// ListCommits returns every commit on a pull request, following pagination.
func (c *Client) ListCommits(ctx context.Context, number int) ([]*githubv39.RepositoryCommit, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.RepositoryCommit
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		commits, resp, err := c.gh.PullRequests.ListCommits(ctx, c.owner, c.repo, number, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, commits...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opt.Page = resp.NextPage
	}
}

// ListReviews returns every review submitted on a pull request, following
// pagination.
func (c *Client) ListReviews(ctx context.Context, number int) ([]*githubv39.PullRequestReview, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.PullRequestReview
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := c.gh.PullRequests.ListReviews(ctx, c.owner, c.repo, number, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, reviews...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opt.Page = resp.NextPage
	}
}

// ListReviewComments returns the inline comments belonging to one review of a
// pull request, following pagination.
//
// Prefer ListAllReviewComments when the comments of every review are wanted:
// this endpoint is per-review, so asking it for all of them costs one request
// per review, and a long-lived pull request accumulates reviews without bound.
func (c *Client) ListReviewComments(ctx context.Context, number int, reviewID int64) ([]*githubv39.PullRequestComment, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.PullRequestComment
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		comments, resp, err := c.gh.PullRequests.ListReviewComments(ctx, c.owner, c.repo, number, reviewID, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opt.Page = resp.NextPage
	}
}

// ListAllReviewComments returns every inline review comment on a pull request,
// following pagination.
//
// This is the whole-pull-request endpoint rather than the per-review one, which
// is the point: it answers in a single paginated call what ListReviewComments
// answers in one call per review. Each comment carries the review it belongs to
// in PullRequestReviewID, so the per-review grouping the caller wants is
// recovered locally instead of being bought a request at a time.
func (c *Client) ListAllReviewComments(ctx context.Context, number int) ([]*githubv39.PullRequestComment, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.PullRequestComment
	opt := &githubv39.PullRequestListCommentsOptions{
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := c.gh.PullRequests.ListComments(ctx, c.owner, c.repo, number, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opt.Page = resp.NextPage
	}
}

// IsInMergeQueue reports whether a pull request is currently sitting in the
// repository's merge queue.
func (c *Client) IsInMergeQueue(ctx context.Context, number int) (bool, error) {
	return IsPRInMergeQueue(ctx, c.owner, c.repo, number)
}
