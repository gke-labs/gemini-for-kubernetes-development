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
	"encoding/json"
	"fmt"
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

// ResearchReadyMarker prefixes the one machine-readable line `factory
// research start` prints. Everything before it is progress narration.
const ResearchReadyMarker = "RESEARCH_SANDBOX_READY "

// ResearchSandbox is the marker line's payload: where the conversation
// server for one session is listening, and what it is looking at.
type ResearchSandbox struct {
	Sandbox   string `json:"sandbox"`
	Namespace string `json:"namespace"`
	SessionID string `json:"sessionId"`
	Repo      string `json:"repo"`
	// PodIP is dialed directly. acpd's port is not on the sandbox's -lb
	// Service, so there is no stable address to use instead; a pod
	// restart invalidates this, which is the same event as losing the
	// session.
	PodIP string `json:"podIP"`
	Port  int    `json:"port"`
	// CWD is the checkout the conversation is about.
	CWD string `json:"cwd"`
}

// Addr is the host:port to reach acpd on.
func (s ResearchSandbox) Addr() string {
	return fmt.Sprintf("%s:%d", s.PodIP, s.Port)
}

// ParseResearchReady recovers the sandbox details from a finished
// `factory research start` invocation's output.
//
// The last marker line wins. One invocation prints exactly one, but
// taking the last means an output that somehow accumulated two (a
// reattach, a wrapper that retried) yields the live one rather than a
// stale address.
func ParseResearchReady(output string) (ResearchSandbox, error) {
	var payload string
	found := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), "\r")
		if rest, ok := strings.CutPrefix(line, ResearchReadyMarker); ok {
			payload, found = rest, true
		}
	}
	if !found {
		return ResearchSandbox{}, fmt.Errorf("no %sline in factory output", ResearchReadyMarker)
	}

	var sb ResearchSandbox
	if err := json.Unmarshal([]byte(payload), &sb); err != nil {
		return ResearchSandbox{}, fmt.Errorf("decoding the %sline: %w", ResearchReadyMarker, err)
	}
	// A blank address would be dialed as ":49984", which resolves to
	// this process rather than failing — the kind of wrong that looks
	// like a hang. Refuse it here, where the cause is still visible.
	if sb.PodIP == "" || sb.Port == 0 {
		return ResearchSandbox{}, fmt.Errorf("factory reported sandbox %q with no address (podIP %q, port %d)", sb.Sandbox, sb.PodIP, sb.Port)
	}
	if sb.Sandbox == "" {
		return ResearchSandbox{}, fmt.Errorf("factory reported an address with no sandbox name")
	}
	return sb, nil
}

// ResearchOptions are the inputs for a `factory research start`
// invocation: the sandbox one deep-research conversation runs in.
type ResearchOptions struct {
	Namespace string
	RepoURL   string
	// SessionID determines the sandbox name, so re-invoking with the
	// same id finds the existing sandbox instead of making a second.
	SessionID string
	// GithubToken is used for the checkout only. The engine credential
	// is not passed here and must not be: it travels in the
	// X-Engine-Api-Key header at session create, so that it never lands
	// on the sandbox's disk.
	GithubToken       string
	Image             string
	WorkspaceDiskSize string
	Timeout           time.Duration
}

// StartResearch launches `factory research start` for key unless one is
// already running. The sandbox details are recovered from the finished
// invocation's output with ParseResearchReady, via LastResult.
//
// Asynchronous like every other verb here, though a caller wants the
// address: creation is a pull, a PVC and a clone, which is minutes. A
// synchronous call would hold an HTTP request open for all of it.
//
// No preflight probe. The probe asks whether an in-sandbox factory task
// is in flight, and research starts none — the sandbox exists to host a
// conversation, not to run a task.
func (r *Runner) StartResearch(key string, opts ResearchOptions) bool {
	timeout := researchTimeout(opts)
	return r.start(key, researchArgs(opts, timeout), opts.GithubToken, timeout)
}

// researchTimeout is long enough for a cold image pull, a fresh PVC and
// a large clone, and short enough that a wedged create eventually
// becomes a failure the caller can report rather than a permanent
// "starting".
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
		"research", "start",
		"--url", opts.RepoURL,
		"--session", opts.SessionID,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	// Deliberately no --secret: mounting the member secret would put the
	// engine key on the sandbox's disk, where the agent running in it
	// could read it. The key reaches the engine through acpd's header
	// instead.
	return args
}
