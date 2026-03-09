// Package github provides a wrapper around the Google go-github library.
// It includes helper functions for parsing URLs, authenticating, and interacting with GitHub APIs.
package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	githubv39 "github.com/google/go-github/v39/github"
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
func NewClient(ctx context.Context) (*Client, error) {
	token, err := GetGithubToken(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		Client: clients.NewGitHubClient(ctx, token),
	}, nil
}

// Client is a wrapper around the github.Client.
type Client struct {
	*githubv39.Client
}

// parseIssueURL extracts owner, repo, and issue number from a GitHub issue URL.
func parseIssueURL(url string) (owner string, repo string, number int, err error) {
	u := strings.TrimPrefix(url, "https://")
	tokens := strings.Split(u, "/")
	// e.g. https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/6010
	// or https://github.com/gke-labs/gateway-api-reference-implementation/pull/92
	if len(tokens) >= 5 && tokens[0] == "github.com" && (tokens[3] == "issues" || tokens[3] == "pull") {
		owner := tokens[1]
		repo := tokens[2]
		number, err := strconv.Atoi(tokens[4])
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid issue/pr number %q: %w", tokens[4], err)
		}
		return owner, repo, number, nil
	}
	return "", "", 0, fmt.Errorf("issue format %q not recognized", url)
}

// parseHTMLUrl extracts owner and repo from a GitHub HTML URL.
func parseHTMLUrl(url string) (owner string, repo string, err error) {
	u := strings.TrimPrefix(url, "https://")
	u = strings.TrimSuffix(u, ".git")
	tokens := strings.Split(u, "/")
	if len(tokens) >= 3 && tokens[0] == "github.com" {
		return tokens[1], tokens[2], nil
	}
	return "", "", fmt.Errorf("url format %q not recognized", url)
}

// GetIssue retrieves an issue and optionally its comments.
func (c *Client) GetIssue(ctx context.Context, url string, includeComments bool) (*Issue, error) {
	owner, repo, number, err := parseIssueURL(url)
	if err != nil {
		return nil, err
	}
	issue, _, err := c.Client.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("failed to get github issue: %w", err)
	}
	issueComments := []IssueComment{}
	if includeComments {
		issueComments, err = c.GetIssueComments(ctx, url)
		if err != nil {
			return nil, err
		}
	}
	return &Issue{
		issue:         issue,
		IssueComments: issueComments,
	}, nil
}

// GetIssueComments retrieves all comments for an issue specified by URL.
func (c *Client) GetIssueComments(ctx context.Context, url string) ([]IssueComment, error) {
	owner, repo, number, err := parseIssueURL(url)
	if err != nil {
		return nil, err
	}
	return c.GetIssueCommentsByNumber(ctx, owner, repo, number)
}

// GetIssueCommentsByNumber retrieves all comments for an issue specified by number.
func (c *Client) GetIssueCommentsByNumber(ctx context.Context, owner, repo string, number int) ([]IssueComment, error) {
	comments, _, err := c.Client.Issues.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github issue comments: %w", err)
	}
	issueComments := []IssueComment{}
	for _, comment := range comments {
		issueComments = append(issueComments, IssueComment{
			issuecomment: comment,
		})
	}
	return issueComments, nil
}

// GetPullRequest retrieves a pull request by number.
func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	pr, _, err := c.Client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("failed to get github pull request: %w", err)
	}
	return &PullRequest{
		pr: pr,
	}, nil
}

// GetPullRequestCommits retrieves commits of a pull request.
func (c *Client) GetPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]RepositoryCommit, error) {
	commits, _, err := c.Client.PullRequests.ListCommits(ctx, owner, repo, number, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request commits: %w", err)
	}
	var pullRequestCommits []RepositoryCommit
	for _, commit := range commits {
		pullRequestCommits = append(pullRequestCommits, RepositoryCommit{
			commit: commit,
		})
	}
	return pullRequestCommits, nil
}

// GetPullRequestComments retrieves comments on a pull request.
func (c *Client) GetPullRequestComments(ctx context.Context, owner, repo string, number int) ([]PullRequestComment, error) {
	comments, _, err := c.Client.PullRequests.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request comments: %w", err)
	}
	var pullRequestComments []PullRequestComment
	for _, comment := range comments {
		pullRequestComments = append(pullRequestComments, PullRequestComment{
			comment: comment,
		})
	}
	return pullRequestComments, nil
}

// GetPullRequestReviews retrieves reviews on a pull request.
func (c *Client) GetPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]PullRequestReview, error) {
	reviews, _, err := c.Client.PullRequests.ListReviews(ctx, owner, repo, number, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request reviews: %w", err)
	}
	var pullRequestReviews []PullRequestReview
	for _, review := range reviews {
		pullRequestReviews = append(pullRequestReviews, PullRequestReview{
			review: review,
		})
	}
	return pullRequestReviews, nil
}

// GetRepositoryFromIssueURL retrieves a repository based on an issue URL.
func (c *Client) GetRepositoryFromIssueURL(ctx context.Context, url string) (*Repository, error) {

	owner, repo, _, err := parseIssueURL(url)
	if err != nil {
		return nil, err
	}
	repository, _, err := c.Client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get github repository: %w", err)
	}
	return &Repository{
		repository: repository,
	}, nil
}

// GetRepositoryFromHTMLUrl retrieves a repository based on its HTML URL.
func (c *Client) GetRepositoryFromHTMLUrl(ctx context.Context, url string) (*Repository, error) {
	owner, repo, err := parseHTMLUrl(url)
	if err != nil {
		return nil, err
	}
	repository, _, err := c.Client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get github repository: %w", err)
	}
	return &Repository{
		repository: repository,
	}, nil
}

// MapPRCommentsToReview maps pull request comments to their corresponding reviews.
// This is useful for associating inline comments with the review that contains them.
func MapPRCommentsToReview(comments []PullRequestComment, reviews []PullRequestReview) {
	commentsByReviewID := make(map[int64][]PullRequestComment)
	for _, comment := range comments {
		rid := comment.PullRequestReviewID()
		commentsByReviewID[rid] = append(commentsByReviewID[rid], comment)
	}

	for i := range reviews {
		reviews[i].PullRequestComments = commentsByReviewID[reviews[i].ID()]
	}
}
