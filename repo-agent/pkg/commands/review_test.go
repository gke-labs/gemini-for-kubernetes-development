package commands

import "testing"

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
