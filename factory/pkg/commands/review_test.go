package commands

import "testing"

func strPtr(s string) *string { return &s }

// The exact production failure: the agent emitted side "RIGHT\n" and GitHub
// 422-rejected the whole review.
func TestSanitizedSide(t *testing.T) {
	cases := []struct {
		in   *string
		want *string
	}{
		{strPtr("RIGHT\n"), strPtr("RIGHT")},
		{strPtr(" left "), strPtr("LEFT")},
		{strPtr("RIGHT"), strPtr("RIGHT")},
		{strPtr("banana"), nil},
		{strPtr("  "), nil},
		{nil, nil},
	}
	for _, c := range cases {
		got := sanitizedSide(c.in)
		switch {
		case got == nil && c.want != nil, got != nil && c.want == nil:
			t.Errorf("sanitizedSide(%v): got %v want %v", c.in, got, c.want)
		case got != nil && *got != *c.want:
			t.Errorf("sanitizedSide(%q): got %q want %q", *c.in, *got, *c.want)
		}
	}
}

func TestTrimmedString(t *testing.T) {
	if got := trimmedString(strPtr("  path/to/file.go\n")); got == nil || *got != "path/to/file.go" {
		t.Errorf("trimmedString: got %v", got)
	}
	if got := trimmedString(strPtr("   ")); got != nil {
		t.Errorf("trimmedString(blank): got %v", got)
	}
	if got := trimmedString(nil); got != nil {
		t.Errorf("trimmedString(nil): got %v", got)
	}
}
