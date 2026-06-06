package commands

import (
	"testing"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

func TestFilterDiffFiles(t *testing.T) {
	tests := []struct {
		name         string
		diffFiles    []*gitdiff.File
		ignoreFiles  []string
		includeFiles []string
		want         int // number of files expected after filtering
	}{
		{
			name: "no filtering",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "file2.go"},
			},
			ignoreFiles:  nil,
			includeFiles: nil,
			want:         2,
		},
		{
			name: "ignore by pattern",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "ignore_me.go"},
			},
			ignoreFiles:  []string{"ignore_me.go"},
			includeFiles: nil,
			want:         1,
		},
		{
			name: "include by pattern",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "file2.go"},
			},
			ignoreFiles:  nil,
			includeFiles: []string{"file1.go"},
			want:         1,
		},
		{
			name: "ignore and include",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "file2.go"},
				{NewName: "ignore_me.go"},
			},
			ignoreFiles:  []string{"ignore_me.go"},
			includeFiles: []string{"file1.go", "ignore_me.go"},
			want:         1, // ignore_me.go matches both but is ignored; only file1.go remains.
		},
		{
			name: "ignore takes precedence over include",
			diffFiles: []*gitdiff.File{
				{NewName: "important.go"},
			},
			ignoreFiles:  []string{"important.go"},
			includeFiles: []string{"important.go"},
			want:         0,
		},
		{
			name: "ignore generated file by name",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "zz_generated.deepcopy.go"},
			},
			ignoreFiles:  nil,
			includeFiles: nil,
			want:         1,
		},
		{
			name: "ignore generated file by extension",
			diffFiles: []*gitdiff.File{
				{NewName: "file1.go"},
				{NewName: "file.pb.go"},
			},
			ignoreFiles:  nil,
			includeFiles: nil,
			want:         1,
		},
		{
			name: "ignore all",
			diffFiles: []*gitdiff.File{
				{NewName: "ignore_me.go"},
				{NewName: "zz_generated.go"},
			},
			ignoreFiles:  []string{"ignore_me.go"},
			includeFiles: nil,
			want:         0,
		},
		{
			name: "ignore directory wildcard recursive",
			diffFiles: []*gitdiff.File{
				{NewName: "pkg/clients/generated/subdir/foo.go"},
				{NewName: "pkg/other/foo.go"},
			},
			ignoreFiles:  []string{"pkg/clients/generated/**"},
			includeFiles: nil,
			want:         1,
		},
		{
			name: "include directory wildcard recursive",
			diffFiles: []*gitdiff.File{
				{NewName: "pkg/clients/generated/subdir/foo.go"},
				{NewName: "pkg/other/foo.go"},
			},
			ignoreFiles:  nil,
			includeFiles: []string{"pkg/clients/generated/**"},
			want:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We pass an empty repoDir because we rely on name-based checks for generated files in this test
			got := filterDiffFiles("", tt.diffFiles, tt.ignoreFiles, tt.includeFiles)
			if len(got) != tt.want {
				t.Errorf("filterDiffFiles() returned %d files, want %d", len(got), tt.want)
			}
		})
	}
}
