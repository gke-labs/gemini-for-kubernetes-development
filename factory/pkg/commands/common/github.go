package common

import (
	"context"
	"regexp"
	"strconv"

	githubv39 "github.com/google/go-github/v39/github"
)

// GetReferencedIssues scans a pull request's branch name, title, and body for referenced issue numbers.
func GetReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name, ignoring epoch timestamps (num >= 10000000)
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\d+`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil && num < 10000000 {
				referenced[num] = true
			}
		}
	}

	// Check title and body for #1234 or "Fixes/Closes/Resolves/Issue 1234"
	re := regexp.MustCompile(`(?:#|(?i:\b(?:fixes|closes|resolves|issue)\s+))(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

// ListAllCheckRuns retrieves all check runs for a ref handling pagination and deduplicating by name.
func ListAllCheckRuns(ctx context.Context, client *githubv39.Client, owner, repo, ref string) ([]*githubv39.CheckRun, error) {
	var allRuns []*githubv39.CheckRun
	opts := &githubv39.ListCheckRunsOptions{
		ListOptions: githubv39.ListOptions{
			PerPage: 200,
		},
	}
	for {
		runs, resp, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
		if err != nil {
			return nil, err
		}
		allRuns = append(allRuns, runs.CheckRuns...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Deduplicate check runs by name, keeping only the latest run (highest ID)
	latestRuns := make(map[string]*githubv39.CheckRun)
	for _, run := range allRuns {
		name := run.GetName()
		if existing, ok := latestRuns[name]; ok {
			if run.GetID() > existing.GetID() {
				latestRuns[name] = run
			}
		} else {
			latestRuns[name] = run
		}
	}

	var deduplicated []*githubv39.CheckRun
	for _, run := range latestRuns {
		deduplicated = append(deduplicated, run)
	}
	return deduplicated, nil
}
