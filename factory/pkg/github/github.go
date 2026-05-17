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
