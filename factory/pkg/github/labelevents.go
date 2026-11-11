package github

import (
	"context"
	"fmt"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// LabelledItems is every issue and pull request, open or closed, carrying
// label, following pagination: the per-PR retro's listing of PRs labelled
// for a retro, which stays short because the label comes off when a
// retro ends.
func (c *Client) LabelledItems(ctx context.Context, label string) ([]*githubv39.Issue, error) {
	if !c.Ready() {
		return nil, errNoClient
	}
	var all []*githubv39.Issue
	opts := &githubv39.IssueListByRepoOptions{State: "all", Labels: []string{label}, ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		items, resp, err := c.gh.Issues.ListByRepo(ctx, c.owner, c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing items labelled %s: %w", label, err)
		}
		all = append(all, items...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

// LastLabeled is the latest event that added label to issue or PR number,
// following pagination; nil when there is none.
func (c *Client) LastLabeled(ctx context.Context, number int, label string) (*githubv39.IssueEvent, error) {
	if !c.Ready() {
		return nil, errNoClient
	}
	var last *githubv39.IssueEvent
	opts := &githubv39.ListOptions{PerPage: 100}
	for {
		events, resp, err := c.gh.Issues.ListIssueEvents(ctx, c.owner, c.repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("listing the events of #%d: %w", number, err)
		}
		for _, e := range events {
			if e.GetEvent() == "labeled" && e.Label != nil && strings.EqualFold(e.Label.GetName(), label) {
				if last == nil || !e.GetCreatedAt().Before(last.GetCreatedAt()) {
					last = e
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return last, nil
		}
		opts.Page = resp.NextPage
	}
}

// IsCollaborator reports whether login may push to or administer the
// repository, directly, through a team, or as an owner of its
// organisation: GitHub's collaborators, which include the members with
// access.
func (c *Client) IsCollaborator(ctx context.Context, login string) (bool, error) {
	if !c.Ready() {
		return false, errNoClient
	}
	ok, _, err := c.gh.Repositories.IsCollaborator(ctx, c.owner, c.repo, login)
	if err != nil {
		return false, fmt.Errorf("checking whether %s is a collaborator: %w", login, err)
	}
	return ok, nil
}
