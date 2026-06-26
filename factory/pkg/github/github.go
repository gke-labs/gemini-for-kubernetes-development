package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
)

// GetGithubToken retrieves the GitHub token from environment variables or the gh CLI.
// The precedence order is:
// 1. MANUAL_PAT (Manually provided Personal Access Token)
// 2. GITHUB_TOKEN (Standard GitHub Actions or environment token)
// 3. OAUTH_PAT (Token from OAuth flow)
// 4. gh auth token (Fallback to gh CLI credential helper)
func GetGithubToken(ctx context.Context) (string, error) {
	token := os.Getenv("MANUAL_PAT")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("OAUTH_PAT")
	}
	if token == "" {
		githubCommand := exec.CommandContext(ctx, "gh", "auth", "token")
		var stdout bytes.Buffer
		githubCommand.Stdout = &stdout
		githubCommand.Stderr = os.Stderr
		if err := githubCommand.Run(); err != nil {
			return "", fmt.Errorf("unable to get github credentials (with gh auth token command): %w", err)
		}

		token = strings.TrimSpace(stdout.String())
	}
	return token, nil
}

// NewClient creates a new GitHub client using the token retrieved by GetGithubToken.
func NewClient(ctx context.Context) (*githubv39.Client, error) {
	token, err := GetGithubToken(ctx)
	if err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	return githubv39.NewClient(tc), nil
}

// ListAllIssueComments retrieves all comments for an issue or pull request handling pagination.
func ListAllIssueComments(ctx context.Context, client *githubv39.Client, owner, repo string, num int) ([]*githubv39.IssueComment, error) {
	var allComments []*githubv39.IssueComment
	opt := &githubv39.IssueListCommentsOptions{
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, num, opt)
		if err != nil {
			return nil, err
		}
		allComments = append(allComments, comments...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return allComments, nil
}

// ListAllCommits retrieves all commits for a pull request handling pagination.
func ListAllCommits(ctx context.Context, client *githubv39.Client, owner, repo string, num int) ([]*githubv39.RepositoryCommit, error) {
	var allCommits []*githubv39.RepositoryCommit
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		commits, resp, err := client.PullRequests.ListCommits(ctx, owner, repo, num, opt)
		if err != nil {
			return nil, err
		}
		allCommits = append(allCommits, commits...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return allCommits, nil
}

// ListAllReviews retrieves all reviews for a pull request handling pagination.
func ListAllReviews(ctx context.Context, client *githubv39.Client, owner, repo string, num int) ([]*githubv39.PullRequestReview, error) {
	var allReviews []*githubv39.PullRequestReview
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, num, opt)
		if err != nil {
			return nil, err
		}
		allReviews = append(allReviews, reviews...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return allReviews, nil
}

// ListAllReviewComments retrieves all review comments for a pull request review handling pagination.
func ListAllReviewComments(ctx context.Context, client *githubv39.Client, owner, repo string, prNum int, reviewID int64) ([]*githubv39.PullRequestComment, error) {
	var allComments []*githubv39.PullRequestComment
	opt := &githubv39.ListOptions{PerPage: 100}
	for {
		comments, resp, err := client.PullRequests.ListReviewComments(ctx, owner, repo, prNum, reviewID, opt)
		if err != nil {
			return nil, err
		}
		allComments = append(allComments, comments...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return allComments, nil
}
