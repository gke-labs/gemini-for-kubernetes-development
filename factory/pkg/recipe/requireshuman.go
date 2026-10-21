package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// requiresHuman is why err, a step's failure, would happen again if the
// task ran again, or "" when it might not: a failure for a person to look
// at before anything retries it.
func requiresHuman(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "input token count exceeds"):
		// gemini's ACP mode hands the model a tool's whole output: one
		// command printing megabytes is more than the model takes, and a
		// new run runs the same command (google-gemini/gemini-cli#29777).
		return "the agent's input went over the model's token limit in one turn, most likely a command that printed a very large output (the task log has it); running it again would do the same"
	}
	return ""
}

// markRequiresHuman records reason, for the step labelled label, in the
// task's spool.RequiresHumanFile.
func (r *Runner) markRequiresHuman(label, reason string) {
	fmt.Fprintf(r.Log, "::requires-human %s: %s\n", label, reason)
	if r.TaskDir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(r.TaskDir, spool.RequiresHumanFile), []byte("step "+label+": "+reason+"\n"), 0o644); err != nil {
		fmt.Fprintf(r.Log, "::warning recording that the task needs a person: %v\n", err)
	}
}

// markStopReason records, for the step labelled label, the ACP stop
// reason its turn ended on (spool.StopReasonFile): max_turn_requests says
// the agent ran out of turns, which the task's status and the CLI's
// failure then name.
func (r *Runner) markStopReason(label, reason string) {
	if r.TaskDir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(r.TaskDir, spool.StopReasonFile), []byte("step "+label+": "+reason+"\n"), 0o644); err != nil {
		fmt.Fprintf(r.Log, "::warning recording the turn's stop reason: %v\n", err)
	}
}
