package spool

import (
	"os"
	"path/filepath"
	"strings"
)

// RequiresHumanFile is why a task failed in a way running it again would
// repeat, relative to the task directory: the recipe runner writes it, and
// whoever settles the run marks the sandbox as needing a person, which the
// watch then dispatches nothing to.
const RequiresHumanFile = "requires-human"

// maxRequiresHuman bounds the reason read back, which ends up on an
// annotation.
const maxRequiresHuman = 500

// requiresHuman is the task's RequiresHumanFile, or "".
func requiresHuman(taskDir string) string {
	data, err := os.ReadFile(filepath.Join(taskDir, RequiresHumanFile))
	if err != nil {
		return ""
	}
	reason := strings.TrimSpace(string(data))
	if len(reason) > maxRequiresHuman {
		reason = reason[:maxRequiresHuman]
	}
	return reason
}

// StopReasonFile is, relative to the task directory, the ACP stop reason
// of the turn the task failed on when it was not the agent's answer
// ("step <label>: max_turn_requests"): the recipe runner writes it.
const StopReasonFile = "stop-reason"

// stopReason is the task's StopReasonFile, or "".
func stopReason(taskDir string) string {
	data, err := os.ReadFile(filepath.Join(taskDir, StopReasonFile))
	if err != nil {
		return ""
	}
	reason := strings.TrimSpace(string(data))
	if len(reason) > maxRequiresHuman {
		reason = reason[:maxRequiresHuman]
	}
	return reason
}
