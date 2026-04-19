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
	"strconv"
	"strings"
	"unicode/utf8"

	githubv39 "github.com/google/go-github/v39/github"
)

// TODO (barney-s): Deprecate. refactor autopoll to use PullReq instead of this
type PullRequestRef struct {
	Repo Repo

	PullRequestNumber int
}

func ParsePullRequestURL(s string) (*PullRequestRef, error) {
	u := strings.TrimPrefix(s, "https://")

	if prefix, suffix, found := strings.Cut(u, "#"); found {
		u = prefix
		_ = suffix // ignore fragment
	}

	// Conveneience: handle URLs that end with /changes (when copy and paste from file view)
	u = strings.TrimSuffix(u, "/changes")

	tokens := strings.Split(u, "/")

	// e.g. https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/6010
	if len(tokens) == 5 && tokens[0] == "github.com" && tokens[3] == "pull" {
		pr := &PullRequestRef{
			Repo: Repo{
				Host:  "github.com",
				Owner: tokens[1],
				Name:  tokens[2],
			},
		}
		// Parse pull request number
		n, err := strconv.Atoi(tokens[4])
		if err != nil {
			return nil, fmt.Errorf("invalid pull request number %q: %w", tokens[4], err)
		}
		pr.PullRequestNumber = n
		return pr, nil
	}

	return nil, fmt.Errorf("pull-request format %q not recognized", s)
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
	byteOffset := 0
	runeCount := 0
	for runeCount < 2000 && byteOffset < len(body) {
		_, size := utf8.DecodeRuneInString(body[byteOffset:])
		byteOffset += size
		runeCount++
	}
	if byteOffset < len(body) {
		return body[:byteOffset] + "... (truncated)"
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
