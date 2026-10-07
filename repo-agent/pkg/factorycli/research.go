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

package factorycli

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// researchShortIDLen mirrors factory's: eight hex characters of the
// session digest go in the sandbox name.
const researchShortIDLen = 8

// ResearchShortID mirrors factory's ResearchShortID — the first eight
// hex characters of sha256(sessionID).
//
// Mirrored rather than imported, like every other name in this package:
// factory is reached by process invocation, never by Go import. The
// cost is that a change to factory's derivation has to be made here
// too, which the tests are written to catch as a value mismatch rather
// than a compile error.
func ResearchShortID(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:researchShortIDLen]
}

// ResearchSandboxName mirrors factory's ResearchSandboxName: the
// per-session conversation sandbox (rsch-<slug>-<short id>),
// slug-budgeted for the -lb Service's DNS cap.
//
// Knowing the name without asking factory is what lets a caller find,
// watch or delete the sandbox for a session it has only the id of.
func ResearchSandboxName(repo, sessionID string) string {
	short := ResearchShortID(sessionID)
	slug := strings.ToLower(repo)
	var b strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug = strings.Trim(b.String(), "-")
	if budget := 60 - len("rsch-") - len(short) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "rsch-" + slug + "-" + short
}

// ResearchRunAnnotation is where factory records a research conversation's
// start (factory sandbox.RunAnnotation("research")): its task, whose agent
// session the conversation is.
const ResearchRunAnnotation = "sandbox.gemini.google.com/research-run"

// ResearchTask is the task whose session a research sandbox's conversation
// is, empty for a sandbox the recipe did not start.
func ResearchTask(annotations map[string]string) string {
	return RecordedRunSession(annotations, ResearchRunAnnotation)
}

// ResearchRunName is the run a session's start is recorded under: one per
// session, so a relaunch after a controller restart follows the start
// instead of asking the question twice. Identifiers only.
func ResearchRunName(sessionID string) string {
	return "research/" + sessionID
}
