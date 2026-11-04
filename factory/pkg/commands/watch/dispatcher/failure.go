package dispatcher

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// engineUnavailableRE is an engine's answer that it cannot serve now -
// its model at capacity or the caller rate limited - as gemini prints it
// ("prompt failed: RPC error [503]: This model is currently experiencing
// high demand…") and as the APIs name it: a moment's state of the
// service, which a later run gets past.
var engineUnavailableRE = regexp.MustCompile(`(?i)RPC error \[(429|503)\]|experiencing high demand|RESOURCE_EXHAUSTED|model is (currently )?overloaded|\b(429 Too Many Requests|503 Service Unavailable)\b`)

// RunError is a child factory process that exited non-zero, with what its
// log says went wrong: the last error it printed. Where a recipe's apply
// failed is the run's to say, in its task directory
// (task-output.apply-failed).
type RunError struct {
	Err    error
	Reason string
	// EngineUnavailable is that the log shows the engine could not serve
	// the run (engineUnavailableRE): its model at capacity, not the task.
	EngineUnavailable bool
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
// engine could not serve it (engineUnavailableRE on an error line): the
// reason is then that line, which says so, rather than the "task failed"
// factory prints last.
func failureOfLog(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > logTail {
		_, _ = f.Seek(info.Size()-logTail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	reason, unavailable := "", false
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
		if engineUnavailableRE.MatchString(line) {
			reason, unavailable = line, true
			break
		}
	}
	if len(reason) > maxReason {
		reason = reason[:maxReason] + "…"
	}
	return reason, unavailable
}
