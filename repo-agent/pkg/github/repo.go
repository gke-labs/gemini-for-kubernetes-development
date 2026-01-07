package github

import (
	"fmt"
	"strings"
)

type Repo struct {
	Host  string
	Owner string
	Name  string
}

func ParseRepo(repo string) (*Repo, error) {
	repo = strings.TrimPrefix(repo, "https://")
	tokens := strings.Split(repo, "/")

	if len(tokens) == 2 {
		return &Repo{
			Host:  "github.com",
			Owner: tokens[0],
			Name:  tokens[1],
		}, nil
	}

	if len(tokens) == 3 {
		return &Repo{
			Host:  tokens[0],
			Owner: tokens[1],
			Name:  tokens[2],
		}, nil
	}

	return nil, fmt.Errorf("repo format %q not recognized", repo)
}

// FilesystemName returns a directory name for the repository checkout from git.
func (r *Repo) FilesystemName() string {
	return r.Name
}

// GitCloneURL returns the git clone URL for the repository.
func (r *Repo) GitCloneURL() string {
	return fmt.Sprintf("https://%s/%s/%s.git", r.Host, r.Owner, r.Name)
}
