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
	"time"
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

// ResearchOptions are the inputs for a `factory recipe research`
// invocation: the sandbox one deep-research conversation runs in, and its
// opening question, asked by the recipe's start.
type ResearchOptions struct {
	Namespace string
	RepoURL   string
	// SessionID determines the sandbox name, so re-invoking with the
	// same id finds the existing sandbox instead of making a second.
	SessionID string
	// Topic is the opening question.
	Topic string
	// GithubToken is used for the clone only: the recipe is credentials:
	// clone, so factory hands it to that one step and keeps it out of the
	// sandbox's environment and disk.
	GithubToken string
	// Engine is the engine the conversation runs on. Empty leaves
	// factory's default, gemini.
	Engine            string
	Image             string
	WorkspaceDiskSize string
	Timeout           time.Duration
}

// ResearchRunName is the run a session's start is recorded under: one per
// session, so a relaunch after a controller restart follows the start
// instead of asking the question twice. Identifiers only.
func ResearchRunName(sessionID string) string {
	return "research/" + sessionID
}

// StartResearch launches `factory recipe research` for key unless one is
// already running. Detached: factory returns once the sandbox has the
// task and the sandbox carries research-ready, and the conversation runs
// on in the sandbox, where the board follows it as a task session.
//
// No preflight probe: the sandbox is new, made for this conversation.
func (r *Runner) StartResearch(key string, opts ResearchOptions) bool {
	timeout := researchTimeout(opts)
	return r.start(key, researchArgs(opts, timeout), opts.GithubToken, timeout)
}

// researchTimeout is long enough for a cold image pull and a fresh PVC,
// and short enough that a wedged create eventually becomes a failure the
// caller can report rather than a permanent "starting".
func researchTimeout(opts ResearchOptions) time.Duration {
	if opts.Timeout > 0 {
		return opts.Timeout
	}
	return 20 * time.Minute
}

// researchArgs is the command line, split out so the contract with
// factory can be asserted without spawning anything.
func researchArgs(opts ResearchOptions, timeout time.Duration) []string {
	args := []string{
		"recipe", "research",
		"--url", opts.RepoURL,
		"--session", opts.SessionID,
		"--run-name", ResearchRunName(opts.SessionID),
		"--topic", opts.Topic,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--detached",
		"--abort-on-cancel=false",
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	// Deliberately no --secret: mounting the member secret would put the
	// engine key on the sandbox's disk, where the agent running in it
	// could read it.
	return args
}
