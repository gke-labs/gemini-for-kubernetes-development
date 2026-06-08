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

package blocked

import (
	"os"
	"testing"
)

func TestIsBlocked(t *testing.T) {
	tests := []struct {
		name           string
		blockedActions string
		inputText      string
		expected       bool
	}{
		{
			name:           "no blocked actions",
			blockedActions: "",
			inputText:      "/hold cancel",
			expected:       false,
		},
		{
			name:           "simple command blocked",
			blockedActions: "/hold cancel",
			inputText:      "please do /hold cancel to merge",
			expected:       true,
		},
		{
			name:           "comma separated multiple commands blocked",
			blockedActions: "/hold,/lgtm",
			inputText:      "sending /lgtm",
			expected:       true,
		},
		{
			name:           "json array multiple commands blocked",
			blockedActions: `["/hold", "/lgtm"]`,
			inputText:      "sending /lgtm",
			expected:       true,
		},
		{
			name:           "semantic mapping unhold",
			blockedActions: "unhold",
			inputText:      "/hold cancel",
			expected:       true,
		},
		{
			name:           "semantic mapping unhold case insensitive",
			blockedActions: "UNHOLD",
			inputText:      "some text with hold cancel in it",
			expected:       true,
		},
		{
			name:           "semantic mapping approve blocked",
			blockedActions: "approve",
			inputText:      "I will put /lgtm",
			expected:       true,
		},
		{
			name:           "unrelated input not blocked",
			blockedActions: "unhold",
			inputText:      "just some unrelated comment",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.blockedActions != "" {
				os.Setenv("BLOCKED_ACTIONS", tt.blockedActions)
				defer os.Unsetenv("BLOCKED_ACTIONS")
			} else {
				os.Unsetenv("BLOCKED_ACTIONS")
			}

			result := IsBlocked(tt.inputText)
			if result != tt.expected {
				t.Errorf("expected IsBlocked(%q) to be %v, got %v", tt.inputText, tt.expected, result)
			}
		})
	}
}
