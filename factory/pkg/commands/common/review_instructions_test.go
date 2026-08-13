package common

import (
	"reflect"
	"testing"
)

func TestExtractReviewInstructions(t *testing.T) {
	tests := []struct {
		name     string
		bodies   []string
		expected []string
	}{
		{
			name:     "empty bodies",
			bodies:   []string{"", "   "},
			expected: nil,
		},
		{
			name: "single PR body with review instructions and terminating equal heading",
			bodies: []string{`# Overview
Some description of the PR.

## Review Instructions
- '.gemini/skills/generate-sh-checker/SKILL.md'
- Ensure all exported functions have GoDoc comments.
- '.gemini/skills/kcc-direct-controller-implementer/SKILL.md'

### Notes for Reviewer
Pay special attention to the conversion logic in _types.go.

## Testing Done
- Ran make test
`},
			expected: []string{
				"'.gemini/skills/generate-sh-checker/SKILL.md'",
				"Ensure all exported functions have GoDoc comments.",
				"'.gemini/skills/kcc-direct-controller-implementer/SKILL.md'",
				"### Notes for Reviewer",
				"Pay special attention to the conversion logic in _types.go.",
			},
		},
		{
			name: "backtick stripping around file path",
			bodies: []string{`## Review Instructions
- ` + "`" + `.gemini/skills/generate-sh-checker/SKILL.md` + "`" + `
- Normal instruction line
`},
			expected: []string{
				".gemini/skills/generate-sh-checker/SKILL.md",
				"Normal instruction line",
			},
		},
		{
			name: "fallback to parent issue body when PR body has no section",
			bodies: []string{
				"PR body without section\nFixes #123",
				"Parent issue body\n## Review Instructions\n- .gemini/skills/foo/SKILL.md",
			},
			expected: []string{
				".gemini/skills/foo/SKILL.md",
			},
		},
		{
			name: "stops at higher level heading",
			bodies: []string{`## Review Instructions
- Rule 1
# Major Section
- Not included
`},
			expected: []string{
				"Rule 1",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := ExtractReviewInstructions(tc.bodies...)
			if !reflect.DeepEqual(actual, tc.expected) {
				t.Fatalf("ExtractReviewInstructions() = %#v, expected %#v", actual, tc.expected)
			}
		})
	}
}
