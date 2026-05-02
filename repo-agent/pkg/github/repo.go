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

package github

import (
	"context"
	"fmt"
	"strings"

	githubapi "github.com/google/go-github/v39/github"
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

func (r *Repo) FetchInfo(ctx context.Context, client *Client) (*RepoInfo, error) {
	info, _, err := client.Repositories.Get(ctx, r.Owner, r.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get github repo info for %s/%s: %w", r.Owner, r.Name, err)
	}

	return &RepoInfo{
		Repo: r,
		info: info,
	}, nil
}

// RepoInfo holds information about a repository.
// It is a wrapper around the github Repository type
type RepoInfo struct {
	*Repo
	info *githubapi.Repository
}

// DefaultBranch returns the default branch of the repository (or "main" if not set).
// This is typically "main" or "master".
func (r *RepoInfo) DefaultBranch() string {
	if r.info.GetDefaultBranch() != "" {
		return r.info.GetDefaultBranch()
	}
	return "main"
}
