package github

import (
	"context"

	githubv39 "github.com/google/go-github/v39/github"
)

// ListCheckRuns returns the check runs for a ref, deduplicated by name.
//
// A rerun does not replace the previous run, it adds another one with the same
// name, so the raw list reports both the stale failure and the fresh success.
// Only the highest ID per name is kept, which is the most recent attempt.
func (c *Client) ListCheckRuns(ctx context.Context, ref string) ([]*githubv39.CheckRun, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.CheckRun
	opts := &githubv39.ListCheckRunsOptions{
		ListOptions: githubv39.ListOptions{PerPage: 200},
	}
	for {
		runs, resp, err := c.gh.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, ref, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, runs.CheckRuns...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	latest := make(map[string]*githubv39.CheckRun)
	for _, run := range all {
		name := run.GetName()
		if existing, ok := latest[name]; ok {
			if run.GetID() > existing.GetID() {
				latest[name] = run
			}
		} else {
			latest[name] = run
		}
	}

	var deduplicated []*githubv39.CheckRun
	for _, run := range latest {
		deduplicated = append(deduplicated, run)
	}
	return deduplicated, nil
}

// ListStatuses returns the commit statuses for a ref, deduplicated by context.
//
// Statuses come back reverse-chronologically, so the first one seen for a
// context is the current one and later entries are superseded history.
func (c *Client) ListStatuses(ctx context.Context, ref string) ([]*githubv39.RepoStatus, error) {
	if !c.Ready() {
		return nil, errNoClient
	}

	var all []*githubv39.RepoStatus
	opts := &githubv39.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := c.gh.Repositories.ListStatuses(ctx, c.owner, c.repo, ref, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, statuses...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	seen := make(map[string]bool)
	var deduplicated []*githubv39.RepoStatus
	for _, status := range all {
		name := status.GetContext()
		if seen[name] {
			continue
		}
		seen[name] = true
		deduplicated = append(deduplicated, status)
	}
	return deduplicated, nil
}
