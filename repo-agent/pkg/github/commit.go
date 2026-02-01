package github

import (
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

type RepositoryCommit struct {
	commit *githubv39.RepositoryCommit
}

func (c *RepositoryCommit) SHA() string {
	return c.commit.GetSHA()
}

func (c *RepositoryCommit) Message() string {
	return c.commit.GetCommit().GetMessage()
}

func (c *RepositoryCommit) CommittedAt() time.Time {
	return c.commit.GetCommit().GetCommitter().GetDate()
}
