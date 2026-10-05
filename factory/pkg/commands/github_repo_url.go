package commands

import (
	"fmt"
	"net/url"
	"strings"
)

// parseGitHubRepoURL splits https://github.com/owner/repo[.git] and
// rejects anything whose owner or repository name is not GitHub's own
// character set — both halves reach a shell as a path and as part of a
// clone URL.
func parseGitHubRepoURL(repoURL string) (owner, repo string, err error) {
	u, perr := url.Parse(repoURL)
	if perr != nil {
		return "", "", fmt.Errorf("invalid repository URL: %w", perr)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("expected URL format https://github.com/owner/repo, got %s", repoURL)
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !validRepoPart(owner) || !validRepoPart(repo) {
		return "", "", fmt.Errorf("unsupported owner/repo in %s: expected GitHub-style names", repoURL)
	}
	return owner, repo, nil
}

// validRepoPart reports whether s is a GitHub owner or repository name:
// letters, digits, dot, dash, underscore, and not empty or a relative
// path component.
func validRepoPart(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
