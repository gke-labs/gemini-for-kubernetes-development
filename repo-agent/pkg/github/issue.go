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
	githubv39 "github.com/google/go-github/v39/github"
)

type Issue struct {
	issue *githubv39.Issue

	IssueComments []IssueComment
	//Upstream:  repo.GitCloneURL(),
}

func (i *Issue) String() string {
	return i.HTMLURL()
}

func (i *Issue) HTMLURL() string {
	return i.issue.GetHTMLURL()
}

func (i *Issue) Number() int {
	return i.issue.GetNumber()
}

func (i *Issue) Title() string {
	return i.issue.GetTitle()
}

func (i *Issue) Body() string {
	return i.issue.GetBody()
}
