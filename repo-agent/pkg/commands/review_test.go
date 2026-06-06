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

func TestMatchesAnyPattern(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		{
			name:     "exact match",
			path:     "go.sum",
			patterns: []string{"go.sum"},
			want:     true,
		},
		{
			name:     "wildcard match",
			path:     "vendor/foo/bar.go",
			patterns: []string{"vendor/*/*.go"},
			want:     true,
		},
		{
			name:     "extension match",
			path:     "foo.generated.go",
			patterns: []string{"*.generated.go"},
			want:     true,
		},
		{
			name:     "no match",
			path:     "main.go",
			patterns: []string{"go.sum", "vendor/*/*.go"},
			want:     false,
		},
		{
			name:     "empty pattern list",
			path:     "go.sum",
			patterns: []string{},
			want:     false,
		},
		{
			name:     "match in multiple patterns",
			path:     "vendor/foo.generated.go",
			patterns: []string{"vendor/*/*.go", "*.generated.go"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesAnyPattern(tt.path, tt.patterns); got != tt.want {
				t.Errorf("matchesAnyPattern() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPathMatches(t *testing.T) {
	tests := []struct {
		p1, p2 string
		want   bool
	}{
		{"file.go", "file.go", true},
		{"a/file.go", "file.go", true},
		{"b/file.go", "file.go", true},
		{"file.go", "a/file.go", true},
		{"file.go", "b/file.go", true},
		{"a/a/file.go", "a/file.go", true},
		{"a/a/file.go", "a/a/file.go", true},
		{"pkg/foo.go", "pkg/foo.go", true},
		{"a/pkg/foo.go", "pkg/foo.go", true},
		{"abc/foo.go", "abc/foo.go", true},
		{"a/foo.go", "b/foo.go", true}, // Mixed prefixes should match
	}
	for _, tt := range tests {
		if got := pathMatches(tt.p1, tt.p2); got != tt.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", tt.p1, tt.p2, got, tt.want)
		}
	}
}

func TestGetActualPath(t *testing.T) {
	diffFiles := []*gitdiff.File{
		{NewName: "pkg/foo.go"},
		{NewName: "a/bar.go"},
		{NewName: "b/baz.go"},
	}
	tests := []struct {
		path string
		want string
	}{
		{"pkg/foo.go", "pkg/foo.go"},
		{"b/pkg/foo.go", "pkg/foo.go"},
		{"a/bar.go", "a/bar.go"},
		{"b/a/bar.go", "a/bar.go"}, // Strips prefix b/ to find a/bar.go
		{"b/baz.go", "b/baz.go"},
		{"a/b/baz.go", "b/baz.go"}, // Strips prefix a/ to find b/baz.go
		{"other.go", "other.go"},   // Not found, returns original
		{"a/pkg/foo.go", "pkg/foo.go"},
	}
	for _, tt := range tests {
		if got := getActualPath(tt.path, diffFiles); got != tt.want {
			t.Errorf("getActualPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestParseOverrides(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		wantMaxFiles     *int
		wantIgnoreFiles  []string
		wantIncludeFiles []string
	}{
		{
			name:         "no overrides",
			body:         "This is a PR description without any commands.",
			wantMaxFiles: nil,
		},
		{
			name:         "max files override",
			body:         "Some text.\n/max-review-files 50\nMore text.",
			wantMaxFiles: intPtr(50),
		},
		{
			name:         "max files zero override",
			body:         "/max-review-files 0",
			wantMaxFiles: intPtr(0),
		},
		{
			name:            "ignore files override",
			body:            "/ignore-files pkg/generated/**, vendor/**",
			wantMaxFiles:    nil,
			wantIgnoreFiles: []string{"pkg/generated/**", "vendor/**"},
		},
		{
			name:             "include files override",
			body:             "/include-files main.go, pkg/api/*.go",
			wantMaxFiles:     nil,
			wantIncludeFiles: []string{"main.go", "pkg/api/*.go"},
		},
		{
			name:             "multiple overrides",
			body:             "/max-review-files: 10\n/ignore-files=foo.go\n/include-files bar.go",
			wantMaxFiles:     intPtr(10),
			wantIgnoreFiles:  []string{"foo.go"},
			wantIncludeFiles: []string{"bar.go"},
		},
		{
			name:             "flexible separators",
			body:             "/max-review-files=100\n/ignore-files:a.go,b.go\n/include-files c.go",
			wantMaxFiles:     intPtr(100),
			wantIgnoreFiles:  []string{"a.go", "b.go"},
			wantIncludeFiles: []string{"c.go"},
		},
		{
			name:            "empty and whitespace",
			body:            "/ignore-files  , foo.go,  , bar.go , ",
			wantIgnoreFiles: []string{"foo.go", "bar.go"},
		},
		{
			name:             "trailing commas",
			body:             "/include-files a.go,b.go,",
			wantIncludeFiles: []string{"a.go", "b.go"},
		},
		{
			name:            "indented and extra whitespace",
			body:            "  /max-review-files   20  \n\t/ignore-files:  foo.go  ,  bar.go  ",
			wantMaxFiles:    intPtr(20),
			wantIgnoreFiles: []string{"foo.go", "bar.go"},
		},
		{
			name:            "case insensitivity",
			body:            "/MAX-REVIEW-FILES 50\n/Ignore-Files foo.go",
			wantMaxFiles:    intPtr(50),
			wantIgnoreFiles: []string{"foo.go"},
		},
		{
			name:            "skip code blocks",
			body:            "```\n/ignore-files hidden.go\n```\n/ignore-files visible.go",
			wantIgnoreFiles: []string{"visible.go"},
		},
		{
			name:            "unintended command suffixes",
			body:            "/max-review-files-extra 100\n/max-review-files 50\n/ignore-files-extra foo.go\n/include-files-extra bar.go",
			wantMaxFiles:    intPtr(50),
			wantIgnoreFiles: nil,
		},
		{
			name:            "backtick wrapped commands",
			body:            "`/ignore-files foo.go`",
			wantIgnoreFiles: []string{"foo.go"},
		},
		{
			name:             "backtick wrapped patterns",
			body:             "/include-files `main.go`, `pkg/*.go` ",
			wantIncludeFiles: []string{"main.go", "pkg/*.go"},
		},
		{
			name:            "quote wrapped patterns",
			body:            "/ignore-files \"file1.go\", 'file2.go'",
			wantIgnoreFiles: []string{"file1.go", "file2.go"},
		},
		{
			name:            "semicolon and tab separators",
			body:            "/ignore-files\ta.go;b.go",
			wantIgnoreFiles: []string{"a.go", "b.go"},
		},
		{
			name:         "mixed separators in command",
			body:         "/max-review-files:= 100",
			wantMaxFiles: intPtr(100),
		},
		{
			name:            "filenames with spaces and explicit separators",
			body:            "/ignore-files: \"my file.go\", other file.go; third file.go",
			wantMaxFiles:    nil,
			wantIgnoreFiles: []string{"my file.go", "other file.go", "third file.go"},
		},
		{
			name:            "space-separated fallback",
			body:            "/ignore-files: file1.go file2.go",
			wantMaxFiles:    nil,
			wantIgnoreFiles: []string{"file1.go", "file2.go"},
		},
		{
			name:            "filenames with spaces NO explicit separators",
			body:            "/ignore-files \"my file.go\" \"other file.go\"",
			wantMaxFiles:    nil,
			wantIgnoreFiles: []string{"my file.go", "other file.go"},
		},
		{
			name:            "filenames with commas in quotes",
			body:            "/ignore-files \"file, with comma.go\", next.go",
			wantMaxFiles:    nil,
			wantIgnoreFiles: []string{"file, with comma.go", "next.go"},
		},
		{
			name:            "leading special characters in filenames preserved",
			body:            "/ignore-files =config.yaml, :data.json",
			wantIgnoreFiles: []string{"=config.yaml", ":data.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMax, gotIgnore, gotInclude := parseOverrides(tt.body)
			if (gotMax == nil) != (tt.wantMaxFiles == nil) {
				t.Errorf("parseOverrides() gotMax = %v, want %v", gotMax, tt.wantMaxFiles)
			} else if gotMax != nil && *gotMax != *tt.wantMaxFiles {
				t.Errorf("parseOverrides() gotMax = %v, want %v", *gotMax, *tt.wantMaxFiles)
			}
			if !equalSlices(gotIgnore, tt.wantIgnoreFiles) {
				t.Errorf("parseOverrides() gotIgnore = %v, want %v", gotIgnore, tt.wantIgnoreFiles)
			}
			if !equalSlices(gotInclude, tt.wantIncludeFiles) {
				t.Errorf("parseOverrides() gotInclude = %v, want %v", gotInclude, tt.wantIncludeFiles)
			}
		})
	}
}

func intPtr(i int) *int {
	return &i
}

func TestParseCommaSeparatedEnv(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     []string
	}{
		{
			name:     "simple list",
			envValue: "a.go,b.go",
			want:     []string{"a.go", "b.go"},
		},
		{
			name:     "with whitespace",
			envValue: " a.go ,  b.go  ",
			want:     []string{"a.go", "b.go"},
		},
		{
			name:     "empty values",
			envValue: "a.go,,b.go,",
			want:     []string{"a.go", "b.go"},
		},
		{
			name:     "empty env",
			envValue: "",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_ENV", tt.envValue)
			got := parseCommaSeparatedEnv("TEST_ENV")
			if !equalSlices(got, tt.want) {
				t.Errorf("parseCommaSeparatedEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
