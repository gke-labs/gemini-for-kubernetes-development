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
