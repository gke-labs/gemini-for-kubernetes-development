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
	"testing"
)

func TestParseHTMLUrl(t *testing.T) {
	tests := []struct {
		url   string
		owner string
		repo  string
		err   bool
	}{
		{"https://github.com/owner/repo", "owner", "repo", false},
		{"https://github.com/owner/repo.git", "owner", "repo", false},
		{"https://github.com/owner/repo/issues/123", "owner", "repo", false},
		{"https://github.com/owner/repo/pull/123", "owner", "repo", false},
		{"https://github.com/owner/repo/tree/branch", "owner", "repo", false},
		{"https://github.com/owner/repo/blob/branch/file", "owner", "repo", false},
		{"https://github.com/owner", "", "", true},
		{"https://google.com/owner/repo", "", "", true},
	}

	for _, tt := range tests {
		owner, repo, err := parseHTMLUrl(tt.url)
		if (err != nil) != tt.err {
			t.Errorf("parseHTMLUrl(%q) error = %v, wantErr %v", tt.url, err, tt.err)
			continue
		}
		if owner != tt.owner {
			t.Errorf("parseHTMLUrl(%q) owner = %v, want %v", tt.url, owner, tt.owner)
		}
		if repo != tt.repo {
			t.Errorf("parseHTMLUrl(%q) repo = %v, want %v", tt.url, repo, tt.repo)
		}
	}
}
