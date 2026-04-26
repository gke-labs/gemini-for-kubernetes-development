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
	"fmt"

	githubv39 "github.com/google/go-github/v39/github"
)

// TODO (barney-s): Deprecate. refactor autopoll to use PullReq instead of this
type PullRequestRef struct {
	Repo Repo

	PullRequestNumber int
}

func ParsePullRequestURL(s string) (*PullRequestRef, error) {
	host, owner, repo, number, urlType, err := parseURL(s)
	if err != nil {
		return nil, err
	}
	if number == 0 || urlType != "pull" {
		return nil, fmt.Errorf("pull-request format %q not recognized", s)
	}

	return &PullRequestRef{
		Repo: Repo{
			Host:  host,
			Owner: owner,
			Name:  repo,
		},
		PullRequestNumber: number,
	}, nil
}

type PullRequest struct {
	pr *githubv39.PullRequest
}

func (p *PullRequest) HTMLURL() string {
	return p.pr.GetHTMLURL()
}

func (p *PullRequest) URL() string {
	return p.pr.GetHTMLURL()
}

func (p *PullRequest) Number() int {
	return p.pr.GetNumber()
}

func (p *PullRequest) Title() string {
	return p.pr.GetTitle()
}

func (p *PullRequest) Body() string {
	return p.pr.GetBody()
}

func (p *PullRequest) State() string {
	return p.pr.GetState()
}

func (p *PullRequest) TruncatedBody() string {
	body := p.pr.GetBody()
	if len(body) <= 2000 {
		return body
	}
	
	// Truncate to 2000 runes (not bytes) for LLM safety, while ensuring we don't slice mid-rune.
	// We use range loop which safely iterates over runes.
	count := 0
	for i := range body {
		if count >= 2000 {
			return body[:i] + "... (truncated)"
		}
		count++
	}
	return body
}

func (p *PullRequest) HeadRef() string {
	return p.pr.GetHead().GetRef()
}

func (p *PullRequest) BaseRef() string {
	return p.pr.GetBase().GetRef()
}

func (p *PullRequest) HeadSHA() string {
	return p.pr.GetHead().GetSHA()
}

func (p *PullRequest) BaseSHA() string {
	return p.pr.GetBase().GetSHA()
}
