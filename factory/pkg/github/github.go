package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
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
		token = os.Getenv(constants.KeyGithubToken)
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

// NewClient creates a new GitHub client using the token retrieved by
// GetGithubToken. Its GETs are cached per token (cachedHTTPClient).
func NewClient(ctx context.Context) (*githubv39.Client, error) {
	token, err := GetGithubToken(ctx)
	if err != nil {
		return nil, err
	}
	return NewClientWithToken(ctx, token), nil
}

// NewClientWithToken creates a new GitHub client using an explicit access
// token. Its GETs are cached per token (cachedHTTPClient), so a repeated
// read is a conditional request.
func NewClientWithToken(ctx context.Context, token string) *githubv39.Client {
	return githubv39.NewClient(cachedHTTPClient(ctx, token))
}
