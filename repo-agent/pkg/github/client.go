// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package github provides a wrapper around the Google go-github library.
// It includes helper functions for parsing URLs, authenticating, and interacting with GitHub APIs.
package github

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
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

func parseURL(s string) (host, owner, repo string, number int, urlType string, err error) {
	// Handle SSH URLs like git@github.com:owner/repo/issues/123 or git@github.com:owner/repo.git
	if strings.HasPrefix(s, "git@") && strings.Contains(s, ":") {
		parts := strings.SplitN(s, ":", 2)
		host = strings.TrimPrefix(parts[0], "git@")
		trimmed := strings.TrimSuffix(parts[1], ".git")
		pathParts := strings.Split(trimmed, "/")
		if len(pathParts) >= 2 {
			owner = pathParts[0]
			repo = pathParts[1]
			if len(pathParts) >= 4 && (pathParts[2] == "issues" || pathParts[2] == "pull") {
				urlType = pathParts[2]
				number, _ = strconv.Atoi(pathParts[3])
			}
			return host, owner, repo, number, urlType, nil
		}
		return "", "", "", 0, "", fmt.Errorf("ssh url format %q not recognized", s)
	}

	// Handle standard URLs
	u, err := url.Parse(s)
	if err != nil {
		return "", "", "", 0, "", err
	}

	if u.Hostname() == "" {
		// Fallback for URLs like github.com/owner/repo (missing scheme)
		if strings.Contains(s, "/") && !strings.HasPrefix(s, "/") {
			return parseURL("https://" + s)
		}
		return "", "", "", 0, "", fmt.Errorf("invalid URL: missing hostname in %q", s)
	}

	host = u.Hostname()
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, "/changes")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		owner = parts[0]
		repo = strings.TrimSuffix(parts[1], ".git")
		if len(parts) >= 4 && (parts[2] == "issues" || parts[2] == "pull") {
			urlType = parts[2]
			number, err = strconv.Atoi(parts[3])
			if err != nil {
				return host, owner, repo, 0, urlType, fmt.Errorf("invalid issue/pr number %q: %w", parts[3], err)
			}
		}
		return host, owner, repo, number, urlType, nil
	}
	return "", "", "", 0, "", fmt.Errorf("url format %q not recognized", s)
}

// ParseIssueURL extracts owner, repo, and issue number from a GitHub issue URL or SSH path.
func ParseIssueURL(s string) (owner string, repo string, number int, err error) {
	_, owner, repo, number, _, err = parseURL(s)
	if err != nil {
		return "", "", 0, err
	}
	if number == 0 {
		return "", "", 0, fmt.Errorf("issue/pull-request number not found in %q", s)
	}
	return owner, repo, number, nil
}

// ParseHTMLUrl extracts owner and repo from a GitHub HTML URL or SSH path.
func ParseHTMLUrl(s string) (owner string, repo string, err error) {
	_, owner, repo, _, _, err = parseURL(s)
	return owner, repo, err
}

// GetIssue retrieves an issue and optionally its comments.
func (c *Client) GetIssue(ctx context.Context, url string, includeComments bool) (*Issue, error) {
	owner, repo, number, err := ParseIssueURL(url)
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
	owner, repo, number, err := ParseIssueURL(url)
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

	owner, repo, _, err := ParseIssueURL(url)
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
	owner, repo, err := ParseHTMLUrl(url)
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
