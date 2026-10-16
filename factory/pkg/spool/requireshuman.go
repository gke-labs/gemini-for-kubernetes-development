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
