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
