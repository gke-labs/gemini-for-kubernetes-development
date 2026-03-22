package github

import (
	"context"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
)

// GetLastHumanCommitTime returns the timestamp of the last commit made by a human (not the bot).
// It uses pagination to ensure it checks all commits in the PR.
// It uses the Author date as it's a more reliable indicator of when the human made the change.
func GetLastHumanCommitTime(ctx context.Context, client *github.Client, owner, repo string, prNumber int, botLogin string) (time.Time, error) {
	var lastHumanCommitTime time.Time
	opts := &github.ListOptions{PerPage: 100}

	for {
		commits, resp, err := client.PullRequests.ListCommits(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return time.Time{}, err
		}

		for _, c := range commits {
			// Check if author is not the bot
			if c.GetAuthor() != nil && c.GetAuthor().GetLogin() != botLogin {
				if c.GetCommit() != nil && c.GetCommit().GetAuthor() != nil {
					t := c.GetCommit().GetAuthor().GetDate()
					if t.After(lastHumanCommitTime) {
						lastHumanCommitTime = t
					}
				}
			} else if c.GetAuthor() == nil {
				// Fallback to git commit author name if no GitHub account is linked
				if c.GetCommit() != nil && c.GetCommit().GetAuthor() != nil {
					authorName := c.GetCommit().GetAuthor().GetName()
					if authorName != botLogin && !strings.Contains(authorName, "[bot]") {
						t := c.GetCommit().GetAuthor().GetDate()
						if t.After(lastHumanCommitTime) {
							lastHumanCommitTime = t
						}
					}
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return lastHumanCommitTime, nil
}

// CountInvestigationReportsSince counts the number of investigation reports posted as comments since a given time.
func CountInvestigationReportsSince(ctx context.Context, client *github.Client, owner, repo string, prNumber int, since time.Time) (int, error) {
	count := 0
	opts := &github.IssueListCommentsOptions{
		Since:       &since,
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return 0, err
		}

		for _, c := range comments {
			if c.GetCreatedAt().After(since) {
				if strings.Contains(c.GetBody(), "--- INVESTIGATION REPORT ---") {
					count++
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return count, nil
}

// HasLimitReachedComment checks if a comment indicating the retry limit was reached has been posted since a given time.
func HasLimitReachedComment(ctx context.Context, client *github.Client, owner, repo string, prNumber int, since time.Time) (bool, error) {
	opts := &github.IssueListCommentsOptions{
		Since:       &since,
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return false, err
		}

		for _, c := range comments {
			if c.GetCreatedAt().After(since) {
				if strings.Contains(c.GetBody(), "reached its retry limit and human intervention is required") {
					return true, nil
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return false, nil
}
