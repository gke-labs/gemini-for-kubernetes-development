package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/google/go-github/v39/github"
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
	githubAPI := clients.NewGitHubClient(ctx, token)
	return &Client{Client: githubAPI}, nil
}

type Client struct {
	*github.Client
}
