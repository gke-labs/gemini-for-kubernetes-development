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

// The labels and annotations factory stamps on every repository sandbox - a
// recipe on a repository's own, named by its prefix and a key (factory's
// sandbox.EnsureRepoSandbox): research's, a run's, a repository recipe's
// alike.
const (
	// LabelSandboxType is the sandbox's prefix: ResearchPrefix, RunPrefix,
	// or a repository recipe's.
	LabelSandboxType = "sandbox.gemini.google.com/type"
	// LabelSandboxKey is the key's short hash (ShortID): a filter, not
	// proof - AnnotationKeyID is the key itself.
	LabelSandboxKey = "sandbox.gemini.google.com/key"
	// AnnotationKeyID is the key in full: a research session's id, a
	// run's instance.
	AnnotationKeyID = "sandbox.gemini.google.com/key-id"
	// AnnotationReady is the receipt for a finished setup, written by the
	// launch once its task is accepted: absent means the setup did not
	// finish.
	AnnotationReady = "sandbox.gemini.google.com/ready"
	// AnnotationEngine is the engine the sandbox was set up for, written
	// with the ready receipt.
	AnnotationEngine = "sandbox.gemini.google.com/engine"
)

// The built-in recipes' sandbox prefixes: research.yaml's, and the run
// recipes' (run-plan, run-deploy, run-teardown).
const (
	ResearchPrefix = "rsch"
	RunPrefix      = "run"
)

// shortIDLen mirrors factory's: eight hex characters of the key's digest
// go in the sandbox name.
const shortIDLen = 8

// ShortID mirrors factory's sandbox.ShortID — the first eight hex
// characters of sha256(key).
//
// Mirrored rather than imported, like every other name in this package:
// factory is reached by process invocation, never by Go import. The
// cost is that a change to factory's derivation has to be made here
// too, which the tests are written to catch as a value mismatch rather
// than a compile error.
func ShortID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:shortIDLen]
}

// RepoSandboxName mirrors factory's sandbox.RepoSandboxName:
// <prefix>-<repo slug>-<short hash of the key>.
func RepoSandboxName(prefix, repo, key string) string {
	short := ShortID(key)
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
	if budget := 60 - len(prefix) - 1 - len(short) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return prefix + "-" + slug + "-" + short
}
