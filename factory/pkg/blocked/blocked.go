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
	"encoding/json"
	"os"
	"strings"
)

// IsBlocked checks if a given action or text is blocked by the BLOCKED_ACTIONS policy.
func IsBlocked(text string) bool {
	blockedActionsRaw := os.Getenv("BLOCKED_ACTIONS")
	if blockedActionsRaw == "" {
		return false
	}

	var terms []string
	if strings.HasPrefix(blockedActionsRaw, "[") {
		_ = json.Unmarshal([]byte(blockedActionsRaw), &terms)
	} else {
		for _, part := range strings.Split(blockedActionsRaw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				terms = append(terms, trimmed)
			}
		}
	}

	if len(terms) == 0 {
		return false
	}

	// Expand semantic terms
	var expanded []string
	for _, term := range terms {
		expanded = append(expanded, term)
		lterm := strings.ToLower(term)
		if lterm == "unhold" || lterm == "unhold-pr" {
			expanded = append(expanded, "/hold cancel", "hold cancel", "do-not-merge/hold")
		} else if lterm == "approve" || lterm == "lgtm" {
			expanded = append(expanded, "/approve", "/lgtm", "approved", "lgtm")
		} else if lterm == "hold" {
			expanded = append(expanded, "/hold", "do-not-merge/hold")
		}
	}

	ltext := strings.ToLower(text)
	for _, term := range expanded {
		if strings.Contains(ltext, strings.ToLower(term)) {
			return true
		}
	}

	return false
}
