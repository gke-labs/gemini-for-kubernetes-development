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
	if err != nil {
		return "github.com"
	}
	return u.Host
}
