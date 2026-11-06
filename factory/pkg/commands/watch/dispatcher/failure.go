package dispatcher

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// RunError is a child factory process that exited non-zero, with what its
// log says went wrong: the last error it printed. Where a recipe's apply
// failed is the run's to say, in its task directory
// (task-output.apply-failed).
type RunError struct {
	Err    error
	Reason string
	// EngineUnavailable is that the log shows the engine could not serve
	// the run (recipe.EngineUnavailableMessage): its model at capacity, not the task.
	EngineUnavailable bool
	// DiskFull is that the run did not start because its sandbox's
	// workspace was full even after cleaning (diskcare.ErrFull): not the
	// task's failure either, so no attempt of its own.
	DiskFull bool
}

func (e *RunError) Error() string {
	if e.Reason == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%v: %s", e.Err, e.Reason)
}

func (e *RunError) Unwrap() error { return e.Err }

// maxReason bounds the reason recorded on a failed task: the first line of
// factory's error says what failed; the rest is in the task's log.
const maxReason = 300

// logTail is how much of a task's log failureFromLog reads: the error is
// at its end.
const logTail = 256 << 10

// failureFromLog is what a failed child's log says: the last line it
// printed as "Error: …" (factory prints its error that way, often twice).
// An unreadable log, or one with no error line, gives no reason.
func failureFromLog(path string) string {
	reason, _ := failureOfLog(path)
	return reason
}

// failureOfLog is failureFromLog, and whether the run failed because its
// engine could not serve it (recipe.EngineUnavailableMessage on an error line): the
// reason is then that line, which says so, rather than the "task failed"
// factory prints last.
func failureOfLog(path string) (string, bool) {
	reason, unavailable, _ := failureKindOfLog(path)
	return reason, unavailable
}

// diskFullPrefix starts diskcare.ErrFull's message, as factory prints it.
const diskFullPrefix = "disk full: "

// failureKindOfLog is failureOfLog, and also whether the run failed for a
// full workspace (an error line from diskcare.ErrFull): the reason is then
// that line.
func failureKindOfLog(path string) (string, bool, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > logTail {
		_, _ = f.Seek(info.Size()-logTail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false, false
	}
	reason, unavailable, diskFull := "", false, false
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "Error: ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "Error: "))
		if reason == "" {
			reason = line
		}
		if recipe.EngineUnavailableMessage(line) {
			reason, unavailable = line, true
			break
		}
		if strings.Contains(line, diskFullPrefix) {
			reason, diskFull = line, true
			break
		}
	}
	if len(reason) > maxReason {
		reason = reason[:maxReason] + "…"
	}
	return reason, unavailable, diskFull
}
