package dispatcher

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Where a task's child failed: in its run, or after it, applying the
// run's result (a reply posted, a PR opened). A run that succeeded in its
// sandbox and failed to apply is still a failed task.
const (
	FailedAtRun   = "run"
	FailedAtApply = "apply"
)

// RunError is a child factory process that exited non-zero, with what its
// log says went wrong: the last error it printed, and whether that was in
// the run or applying its result.
type RunError struct {
	Err      error
	Reason   string
	FailedAt string
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
// printed as "Error: …" (factory prints its error that way, often twice),
// and whether it failed applying its result ("Applying the result of task"
// came first) or in its run. An unreadable log, or one with no error line,
// gives no reason.
func failureFromLog(path string) (reason, failedAt string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > logTail {
		_, _ = f.Seek(info.Size()-logTail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", ""
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	errAt := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "Error: ") {
			reason = strings.TrimSpace(strings.TrimPrefix(line, "Error: "))
			errAt = i
			break
		}
	}
	if reason == "" {
		return "", ""
	}
	if len(reason) > maxReason {
		reason = reason[:maxReason] + "…"
	}
	failedAt = FailedAtRun
	for i := errAt; i >= 0; i-- {
		if strings.Contains(lines[i], "Applying the result of task") {
			failedAt = FailedAtApply
			break
		}
	}
	return reason, failedAt
}
