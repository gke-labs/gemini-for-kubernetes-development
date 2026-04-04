package api

import (
	"testing"
)

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{
			name:  "No truncation needed",
			s:     "hello",
			limit: 10,
			want:  "hello",
		},
		{
			name:  "Exact limit",
			s:     "hello",
			limit: 5,
			want:  "hello",
		},
		{
			name:  "Basic truncation",
			s:     "hello world",
			limit: 5,
			want:  "hello",
		},
		{
			name:  "UTF-8 truncation - safe",
			s:     "世界", // 6 bytes
			limit: 3,
			want:  "世",
		},
		{
			name:  "UTF-8 truncation - middle of rune",
			s:     "世界", // 6 bytes
			limit: 4,
			want:  "世", // "界" is 3 bytes, so 4th byte is invalid if taken alone
		},
		{
			name:  "UTF-8 truncation - middle of rune 2",
			s:     "世界",
			limit: 5,
			want:  "世",
		},
		{
			name:  "UTF-8 truncation - empty",
			s:     "世界",
			limit: 2,
			want:  "",
		},
		{
			name:  "Empty string",
			s:     "",
			limit: 5,
			want:  "",
		},
		{
			name:  "Zero limit",
			s:     "hello",
			limit: 0,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.s, tt.limit); got != tt.want {
				t.Errorf("truncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}
