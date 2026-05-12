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

import "os"

// GetGitHubToken returns the effective GitHub token from environment variables.
// It follows the hierarchy: GITHUB_USER_TOKEN > MANUAL_PAT > OAUTH_PAT > GITHUB_TOKEN.
func GetGitHubToken() string {
	if token := os.Getenv("GITHUB_USER_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("MANUAL_PAT"); token != "" {
		return token
	}
	if token := os.Getenv("OAUTH_PAT"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}
