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
