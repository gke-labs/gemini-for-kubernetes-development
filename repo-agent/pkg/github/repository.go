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
	"net/url"

	githubv39 "github.com/google/go-github/v39/github"
)

type Repository struct {
	repository *githubv39.Repository
}

func NewRepository(repo *githubv39.Repository) *Repository {
	return &Repository{repository: repo}
}

func (r *Repository) CloneURL() string {
	return r.repository.GetCloneURL()
}

func (r *Repository) Name() string {
	return r.repository.GetName()
}

func (r *Repository) Owner() string {
	return r.repository.GetOwner().GetLogin()
}

func (r *Repository) Host() string {
	u, err := url.Parse(r.CloneURL())
	if err != nil || u.Hostname() == "" {
		return "github.com"
	}
	return u.Hostname()
}
