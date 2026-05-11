package tokens

import "os"

// GetGitHubToken returns the effective GitHub token from environment variables.
// It follows the hierarchy: GITHUB_USER_TOKEN > MANUAL_PAT > OAUTH_PAT > GITHUB_TOKEN.
func GetGitHubToken() string {
	if token := os.Getenv("GITHUB_USER_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("MANUAL_PAT"); token != "" {
		return token
	}
	if token := os.Getenv("OAUTH_PAT"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}
