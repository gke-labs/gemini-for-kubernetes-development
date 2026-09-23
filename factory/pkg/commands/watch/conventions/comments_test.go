package conventions

import "testing"

func TestHasIgnorePrefix(t *testing.T) {
	tests := []struct {
		body         string
		triggerLabel string
		expected     bool
	}{
		{
			body:         "/overseer-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /OVERSEER-IGNORE: some message  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/factory-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /FACTORY-IGNORE: custom prefix  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/other-ignore",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "overseer",
			expected:     true,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "",
			expected:     true,
		},
		{
			body:         "just a regular comment",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "line 1\n/overseer-ignore\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  /FACTORY-IGNORE: some message\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  some comment containing /overseer-ignore but not at start",
			triggerLabel: "factory",
			expected:     false,
		},
	}

	for _, tc := range tests {
		got := HasIgnorePrefix(tc.body, tc.triggerLabel)
		if got != tc.expected {
			t.Errorf("HasIgnorePrefix(%q, %q) = %v; expected %v", tc.body, tc.triggerLabel, got, tc.expected)
		}
	}
}
