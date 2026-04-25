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
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestPullRequest_TruncatedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "short body",
			body: "hello",
			want: "hello",
		},
		{
			name: "long body",
			body: strings.Repeat("a", 2005),
			want: strings.Repeat("a", 2000) + "... (truncated)",
		},
		{
			name: "utf8 body",
			body: strings.Repeat("😊", 2005),
			want: strings.Repeat("😊", 2000) + "... (truncated)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PullRequest{
				pr: &githubv39.PullRequest{
					Body: &tt.body,
				},
			}
			if got := p.TruncatedBody(); got != tt.want {
				t.Logf("len(got): %d, len(want): %d", len(got), len(tt.want))
				t.Errorf("PullRequest.TruncatedBody() failed for %s", tt.name)
			}
		})
	}
}

func TestParsePullRequestURL(t *testing.T) {
	tests := []struct {
		url    string
		host   string
		owner  string
		repo   string
		number int
		err    bool
	}{
		{"https://github.com/owner/repo/pull/123", "github.com", "owner", "repo", 123, false},
		{"https://github.com/owner/repo/pull/123/changes", "github.com", "owner", "repo", 123, false},
		{"https://github.mycompany.com/owner/repo/pull/456", "github.mycompany.com", "owner", "repo", 456, false},
		{"https://github.com/owner/repo/pull/invalid", "", "", "", 0, true},
		{"https://github.com/owner/repo/issues/123", "", "", "", 0, true},
	}

	for _, tt := range tests {
		ref, err := ParsePullRequestURL(tt.url)
		if (err != nil) != tt.err {
			t.Errorf("ParsePullRequestURL(%q) error = %v, wantErr %v", tt.url, err, tt.err)
			continue
		}
		if err == nil {
			if ref.Repo.Host != tt.host {
				t.Errorf("ParsePullRequestURL(%q) host = %v, want %v", tt.url, ref.Repo.Host, tt.host)
			}
			if ref.Repo.Owner != tt.owner {
				t.Errorf("ParsePullRequestURL(%q) owner = %v, want %v", tt.url, ref.Repo.Owner, tt.owner)
			}
			if ref.Repo.Name != tt.repo {
				t.Errorf("ParsePullRequestURL(%q) repo = %v, want %v", tt.url, ref.Repo.Name, tt.repo)
			}
			if ref.PullRequestNumber != tt.number {
				t.Errorf("ParsePullRequestURL(%q) number = %v, want %v", tt.url, ref.PullRequestNumber, tt.number)
			}
		}
	}
}
