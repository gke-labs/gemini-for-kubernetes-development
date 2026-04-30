/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k8s

import (
	"strings"
	"testing"
)

func TestTruncateLabel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "short", input: "short", expected: "short"},
		{name: "exactly 63", input: strings.Repeat("a", 63), expected: strings.Repeat("a", 63)},
		{name: "long triggers hash", input: strings.Repeat("a", 70), expected: strings.Repeat("a", 56) + "-6bd5e5"},
		{name: "unicode start", input: "👋hello-world", expected: "hello-world"},
		{name: "unicode middle", input: "hello👋world", expected: "hello-world"},
		{name: "invalid middle", input: "abc!@#def", expected: "abc-def"},
		{name: "trim dash", input: "-abc-", expected: "abc"},
		{name: "trim dot", input: ".abc.", expected: "abc"},
		{name: "trim mixed", input: ".-_abc_-.", expected: "abc"},
		{name: "empty input", input: "", expected: "fallback-e3b0c442"},
		{name: "only non-alphanumeric triggers fallback hash", input: ".-_", expected: "fallback-3f77d544"},
		{name: "dash dash triggers fallback hash", input: "--", expected: "fallback-d8156bae"},
		{name: "allow dots and underscores", input: "my.label_value", expected: "my.label_value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateLabel(tt.input); got != tt.expected {
				t.Errorf("TruncateLabel(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncateName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "short", input: "short", expected: "short"},
		{name: "exactly 63", input: strings.Repeat("a", 63), expected: strings.Repeat("a", 63)},
		{name: "long triggers hash", input: strings.Repeat("a", 70), expected: strings.Repeat("a", 56) + "-6bd5e5"},
		{name: "unicode start", input: "👋hello-world", expected: "hello-world"},
		{name: "unicode middle", input: "hello👋world", expected: "hello-world"},
		{name: "invalid middle", input: "abc!@#def", expected: "abc-def"},
		{name: "trim dash", input: "-abc-", expected: "abc"},
		{name: "empty input", input: "", expected: "fallback-e3b0c442"},
		{name: "only non-alphanumeric triggers fallback hash", input: ".-_", expected: "fallback-3f77d544"},
		{name: "disallow dots and underscores", input: "my.name_value", expected: "my-name-value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateName(tt.input); got != tt.expected {
				t.Errorf("TruncateName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"My Chore", "my-chore"},
		{"My Chore!", "my-chore"},
		{"  My Chore  ", "my-chore"},
		{"My-Chore-", "my-chore"},
		{"My -- Chore", "my-chore"},
		{"My ! Chore", "my-chore"},
		{"", "fallback-e3b0c442"},
		{"My_(Chore)_Test", "my-chore-test"},
		{"👋_My_Chore", "my-chore"},
		{"👋👋👋", "fallback-3656d98a"}, // Only non-alphanumeric
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Slugify(tt.input); got != tt.expected {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
