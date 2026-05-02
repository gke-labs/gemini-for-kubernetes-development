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
)

func TestFilterDiffFiles(t *testing.T) {
	tests := []struct {
		name        string
		diffFiles   []*gitdiff.File
		ignoreFiles []string
		want        int // number of files expected after filtering
	}{
		{
			name: "no filtering",
			diffFiles: []*gitdiff.File{
				{NewName: "b/file1.go"},
				{NewName: "b/file2.go"},
			},
			ignoreFiles: nil,
			want:        2,
		},
		{
			name: "ignore by pattern",
			diffFiles: []*gitdiff.File{
				{NewName: "b/file1.go"},
				{NewName: "b/ignore_me.go"},
			},
			ignoreFiles: []string{"ignore_me.go"},
			want:        1,
		},
		{
			name: "ignore generated file by name",
			diffFiles: []*gitdiff.File{
				{NewName: "b/file1.go"},
				{NewName: "b/zz_generated.deepcopy.go"},
			},
			ignoreFiles: nil,
			want:        1,
		},
		{
			name: "ignore generated file by extension",
			diffFiles: []*gitdiff.File{
				{NewName: "b/file1.go"},
				{NewName: "b/file.pb.go"},
			},
			ignoreFiles: nil,
			want:        1,
		},
		{
			name: "ignore all",
			diffFiles: []*gitdiff.File{
				{NewName: "b/ignore_me.go"},
				{NewName: "b/zz_generated.go"},
			},
			ignoreFiles: []string{"ignore_me.go"},
			want:        0,
		},
		{
			name: "ignore directory wildcard recursive",
			diffFiles: []*gitdiff.File{
				{NewName: "b/pkg/clients/generated/subdir/foo.go"},
				{NewName: "b/pkg/other/foo.go"},
			},
			ignoreFiles: []string{"pkg/clients/generated/**"},
			want:        1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We pass an empty repoDir because we rely on name-based checks for generated files in this test
			got := filterDiffFiles("", tt.diffFiles, tt.ignoreFiles)
			if len(got) != tt.want {
				t.Errorf("filterDiffFiles() returned %d files, want %d", len(got), tt.want)
			}
		})
	}
}
