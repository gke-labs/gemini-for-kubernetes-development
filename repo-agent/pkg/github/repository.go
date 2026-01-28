package github

import (
	githubv39 "github.com/google/go-github/v39/github"
)

type Repository struct {
	repository *githubv39.Repository
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
