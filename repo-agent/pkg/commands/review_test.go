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

package commands

import (
	"testing"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

func TestIsCommentValid(t *testing.T) {
	tests := []struct {
		name      string
		comment   *models.DraftReviewComment
		diffFiles []*gitdiff.File
		want      bool
	}{
		{
			name: "valid comment with side RIGHT",
			comment: func() *models.DraftReviewComment {
				path := "file.go"
				line := 10
				side := "RIGHT"
				return &models.DraftReviewComment{Path: &path, Line: &line, Side: &side}
			}(),
			diffFiles: []*gitdiff.File{
				{
					NewName: "file.go",
					TextFragments: []*gitdiff.TextFragment{
						{NewPosition: 1, NewLines: 20},
					},
				},
			},
			want: true,
		},
		{
			name: "valid comment with nil side (defaults to RIGHT)",
			comment: func() *models.DraftReviewComment {
				path := "file.go"
				line := 10
				return &models.DraftReviewComment{Path: &path, Line: &line, Side: nil}
			}(),
			diffFiles: []*gitdiff.File{
				{
					NewName: "file.go",
					TextFragments: []*gitdiff.TextFragment{
						{NewPosition: 1, NewLines: 20},
					},
				},
			},
			want: true,
		},
		{
			name: "valid comment with side LEFT",
			comment: func() *models.DraftReviewComment {
				path := "file.go"
				line := 5
				side := "LEFT"
				return &models.DraftReviewComment{Path: &path, Line: &line, Side: &side}
			}(),
			diffFiles: []*gitdiff.File{
				{
					NewName: "file.go",
					TextFragments: []*gitdiff.TextFragment{
						{OldPosition: 1, OldLines: 10, NewPosition: 1, NewLines: 20},
					},
				},
			},
			want: true,
		},
		{
			name: "invalid comment (path missing)",
			comment: func() *models.DraftReviewComment {
				line := 10
				return &models.DraftReviewComment{Path: nil, Line: &line}
			}(),
			diffFiles: []*gitdiff.File{
				{
					NewName: "file.go",
					TextFragments: []*gitdiff.TextFragment{
						{NewPosition: 1, NewLines: 20},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCommentValid(tt.comment, tt.diffFiles); got != tt.want {
				t.Errorf("isCommentValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldIgnoreFile(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		ignorePatterns []string
		want           bool
	}{
		{
			name:           "exact match",
			path:           "go.sum",
			ignorePatterns: []string{"go.sum"},
			want:           true,
		},
		{
			name:           "wildcard match",
			path:           "vendor/foo/bar.go",
			ignorePatterns: []string{"vendor/*/*.go"},
			want:           true,
		},
		{
			name:           "extension match",
			path:           "foo.generated.go",
			ignorePatterns: []string{"*.generated.go"},
			want:           true,
		},
		{
			name:           "no match",
			path:           "main.go",
			ignorePatterns: []string{"go.sum", "vendor/*/*.go"},
			want:           false,
		},
		{
			name:           "empty ignore list",
			path:           "go.sum",
			ignorePatterns: []string{},
			want:           false,
		},
		{
			name:           "match in multiple patterns",
			path:           "vendor/foo.generated.go",
			ignorePatterns: []string{"vendor/*/*.go", "*.generated.go"},
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIgnoreFile(tt.path, tt.ignorePatterns); got != tt.want {
				t.Errorf("shouldIgnoreFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
