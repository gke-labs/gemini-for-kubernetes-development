package commands

import (
	"testing"
)

func TestHasOverseerIgnore(t *testing.T) {
	tests := []struct {
		body     string
		expected bool
	}{
		{
			body:     "/overseer-ignore",
			expected: true,
		},
		{
			body:     "  /OVERSEER-IGNORE: some message  ",
			expected: true,
		},
		{
			body:     "just a regular comment",
			expected: false,
		},
		{
			body:     "line 1\n/overseer-ignore\nline 3",
			expected: true,
		},
		{
			body:     "line 1\n  /OVERSEER-IGNORE: some message\nline 3",
			expected: true,
		},
		{
			body:     "line 1\n  some comment containing /overseer-ignore but not at start",
			expected: false,
		},
	}

	for _, tc := range tests {
		got := hasOverseerIgnore(tc.body)
		if got != tc.expected {
			t.Errorf("hasOverseerIgnore(%q) = %v; expected %v", tc.body, got, tc.expected)
		}
	}
}
