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
