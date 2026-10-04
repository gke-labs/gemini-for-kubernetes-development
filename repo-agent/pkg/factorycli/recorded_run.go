package factorycli

import (
	"encoding/json"
	"time"
)

// factory recipe records the run it starts on the sandbox, under
// sandbox.gemini.google.com/<task type>-run, in the same update that marks
// it Running. The sandbox's task server keeps the run's state; this says
// which run to ask it about. A controller that restarted mid-run invokes
// factory again with the recorded name, which follows the run (or reads
// its result) instead of starting another.
const (
	AnnotationTriageRun = "sandbox.gemini.google.com/recipe-triage-run"
	AnnotationPlanRun   = "sandbox.gemini.google.com/plan-run"
)

// RecordedRun is the run factory recorded on a sandbox.
type RecordedRun struct {
	Name      string    `json:"name,omitempty"`
	Task      string    `json:"task"`
	StartedAt time.Time `json:"startedAt"`
}

// RecordedRunName is the name of the run recorded under key, when it
// started after since: one started before a member's reject or feedback
// is not the run the board waits on. The start is compared to the second,
// as since is stamped, rounding down, so a run in the same second as since
// is never taken for a later one.
func RecordedRunName(annotations map[string]string, key string, since time.Time) (string, bool) {
	var run RecordedRun
	if err := json.Unmarshal([]byte(annotations[key]), &run); err != nil || run.Name == "" || run.StartedAt.IsZero() {
		return "", false
	}
	if !run.StartedAt.Truncate(time.Second).After(since) {
		return "", false
	}
	return run.Name, true
}
