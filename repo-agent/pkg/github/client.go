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

func NewClient(ctx context.Context) (*Client, error) {
	githubCommand := exec.CommandContext(ctx, "gh", "auth", "token")
	var stdout bytes.Buffer
	githubCommand.Stdout = &stdout
	githubCommand.Stderr = os.Stderr
	if err := githubCommand.Run(); err != nil {
		return nil, fmt.Errorf("unable to get github credentials (with gh auth token command): %w", err)
	}

	token := strings.TrimSpace(stdout.String())
	return &Client{
		Client: clients.NewGitHubClient(ctx, token),
	}, nil
}

type Client struct {
	*githubv39.Client
}

func parseIssueURL(url string) (owner string, repo string, number int, err error) {
	u := strings.TrimPrefix(url, "https://")
	tokens := strings.Split(u, "/")
	// e.g. https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/6010
	if len(tokens) == 5 && tokens[0] == "github.com" && tokens[3] == "issues" {
		owner := tokens[1]
		repo := tokens[2]
		number, err := strconv.Atoi(tokens[4])
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid issue number %q: %w", tokens[4], err)
		}
		return owner, repo, number, nil
	}
	return "", "", 0, fmt.Errorf("issue format %q not recognized", url)
}

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

func (c *Client) GetIssueComments(ctx context.Context, url string) ([]IssueComment, error) {
	owner, repo, number, err := parseIssueURL(url)
	if err != nil {
		return nil, err
	}

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
