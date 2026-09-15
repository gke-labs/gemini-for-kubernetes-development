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

package tokens

import (
	"os"
	"testing"
)

func TestGetGitHubToken(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		expected string
	}{
		{
			name: "GITHUB_USER_TOKEN priority",
			env: map[string]string{
				"GITHUB_USER_TOKEN": "user",
				"MANUAL_PAT":        "manual",
				"OAUTH_PAT":         "oauth",
				"GITHUB_TOKEN":      "token",
			},
			expected: "user",
		},
		{
			name: "Manual PAT priority",
			env: map[string]string{
				"MANUAL_PAT":   "manual",
				"OAUTH_PAT":    "oauth",
				"GITHUB_TOKEN": "token",
			},
			expected: "manual",
		},
		{
			name: "OAuth PAT priority over GITHUB_TOKEN",
			env: map[string]string{
				"MANUAL_PAT":   "",
				"OAUTH_PAT":    "oauth",
				"GITHUB_TOKEN": "token",
			},
			expected: "oauth",
		},
		{
			name: "Fallback to GITHUB_TOKEN",
			env: map[string]string{
				"MANUAL_PAT":   "",
				"OAUTH_PAT":    "",
				"GITHUB_TOKEN": "token",
			},
			expected: "token",
		},
		{
			name: "All empty",
			env: map[string]string{
				"MANUAL_PAT":   "",
				"OAUTH_PAT":    "",
				"GITHUB_TOKEN": "",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env
			os.Unsetenv("GITHUB_USER_TOKEN")
			os.Unsetenv("MANUAL_PAT")
			os.Unsetenv("OAUTH_PAT")
			os.Unsetenv("GITHUB_TOKEN")

			// Set env for test
			for k, v := range tt.env {
				if v != "" {
					os.Setenv(k, v)
				}
			}

			got := GetGitHubToken()
			if got != tt.expected {
				t.Errorf("GetGitHubToken() = %v, want %v", got, tt.expected)
			}
		})
	}
}
