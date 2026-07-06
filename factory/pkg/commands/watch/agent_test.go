package watch

import (
	"testing"
)

func TestParseAgent(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		expectErr    bool
		expectName   string
		expectSched  string
		expectPrompt string
	}{
		{
			name: "Valid Agent Definition",
			content: `---
name: "test-agent"
description: "A test agent"
schedule: "0 9 * * 1"
skipPR: true
---
This is the agent prompt.
It has multiple lines.`,
			expectErr:    false,
			expectName:   "test-agent",
			expectSched:  "0 9 * * 1",
			expectPrompt: "This is the agent prompt.\nIt has multiple lines.",
		},
		{
			name: "Missing Frontmatter",
			content: `name: "no-frontmatter"
prompt here`,
			expectErr: true,
		},
		{
			name: "Invalid YAML Frontmatter",
			content: `---
name: : invalid-yaml
---
prompt`,
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def, err := ParseAgent([]byte(tc.content))
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil definition")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error parsing agent: %v", err)
			}

			if def.Name != tc.expectName {
				t.Errorf("expected name %q, got %q", tc.expectName, def.Name)
			}
			if def.Schedule != tc.expectSched {
				t.Errorf("expected schedule %q, got %q", tc.expectSched, def.Schedule)
			}
			if def.Prompt != tc.expectPrompt {
				t.Errorf("expected prompt %q, got %q", tc.expectPrompt, def.Prompt)
			}
		})
	}
}
