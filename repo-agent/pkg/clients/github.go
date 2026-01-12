package clients

import (
	"context"
	"net/http"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
)

// NewGitHubClient creates a new GitHub client using a static token.
func NewGitHubClient(ctx context.Context, token string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

// NewGitHubClientFromHTTP creates a new GitHub client using an existing HTTP client.
// This is useful when the HTTP client is already configured with OAuth2 or for testing.
func NewGitHubClientFromHTTP(httpClient *http.Client) *github.Client {
	return github.NewClient(httpClient)
}
