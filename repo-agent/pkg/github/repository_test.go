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

	githubv39 "github.com/google/go-github/v39/github"
)

func TestRepository_Host(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		want     string
	}{
		{
			name:     "standard github",
			cloneURL: "https://github.com/owner/repo.git",
			want:     "github.com",
		},
		{
			name:     "github with port",
			cloneURL: "https://github.com:443/owner/repo.git",
			want:     "github.com",
		},
		{
			name:     "enterprise github",
			cloneURL: "https://github.mycompany.com/owner/repo.git",
			want:     "github.mycompany.com",
		},
		{
			name:     "invalid url",
			cloneURL: "not-a-url",
			want:     "github.com",
		},
		{
			name:     "empty url",
			cloneURL: "",
			want:     "github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Repository{
				repository: &githubv39.Repository{
					CloneURL: &tt.cloneURL,
				},
			}
			if got := r.Host(); got != tt.want {
				t.Errorf("Repository.Host() = %v, want %v", got, tt.want)
			}
		})
	}
}
