package imagebuilder

import (
	"testing"
)

func TestIsSHA(t *testing.T) {
	tests := []struct {
		s        string
		expected bool
	}{
		{"abc", false},
		{"1234567", true},
		{"1234567890", true},
		{"1234567890123456789012345678901234567890", true},
		{"1234567890123456789012345678901234567890123456789012345678901234", true},
		{"G234567890123456789012345678901234567890", false},
		{"", false},
		{"deadbeef", true},
		{"short", false},
	}

	for _, tt := range tests {
		if got := isSHA(tt.s); got != tt.expected {
			t.Errorf("isSHA(%q) = %v, want %v", tt.s, got, tt.expected)
		}
	}
}
