package common

import (
	"context"
	"regexp"
	"sort"
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

// ListAllStatuses retrieves all commit statuses for a ref handling pagination and deduplicating by context (keeping the latest status per context).
func ListAllStatuses(ctx context.Context, client *githubv39.Client, owner, repo, ref string) ([]*githubv39.RepoStatus, error) {
	var allStatuses []*githubv39.RepoStatus
	opts := &githubv39.ListOptions{
		PerPage: 100,
	}
	for {
		statuses, resp, err := client.Repositories.ListStatuses(ctx, owner, repo, ref, opts)
		if err != nil {
			return nil, err
		}
		allStatuses = append(allStatuses, statuses...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Deduplicate statuses by context, keeping only the latest status (first encountered since ListStatuses is reverse-chronological)
	seenContexts := make(map[string]bool)
	var deduplicated []*githubv39.RepoStatus
	for _, status := range allStatuses {
		ctxName := status.GetContext()
		if seenContexts[ctxName] {
			continue
		}
		seenContexts[ctxName] = true
		deduplicated = append(deduplicated, status)
	}
	return deduplicated, nil
}

// ExtractRelatedIssuesAndPRs parses the issue body and comments to find all referenced issue and PR numbers.
// It returns a sorted, deduplicated slice of integer issue/PR numbers.
func ExtractRelatedIssuesAndPRs(body string, comments []string, excludeNum int) []int {
	referenced := make(map[int]bool)

	// Combine body and comments for processing
	allTexts := append([]string{body}, comments...)

	// We match patterns like:
	// 1. #123
	// 2. GH-123
	// 3. /issues/123
	// 4. /pull/123 or /pulls/123
	reHash := regexp.MustCompile(`(?:#|(?i:\bgh-))(\d+)\b`)
	reURL := regexp.MustCompile(`\b(?:issues|pull|pulls)/(\d+)\b`)
	reKeyword := regexp.MustCompile(`(?i:\b(?:fixes|closes|resolves|issue|pr)\s+)(\d+)\b`)

	for _, text := range allTexts {
		// Hash / GH- references
		for _, match := range reHash.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
		// URL / path references
		for _, match := range reURL.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
		// Keyword references
		for _, match := range reKeyword.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
	}

	// Convert map keys to a sorted slice
	var result []int
	for num := range referenced {
		result = append(result, num)
	}
	sort.Ints(result)
	return result
}
