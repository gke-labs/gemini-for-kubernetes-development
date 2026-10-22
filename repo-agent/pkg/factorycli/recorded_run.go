package factorycli

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
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
	// AnnotationReviewRun is a review's: the recipe has no task type, so
	// it runs as recipe-review, the main task of a sandbox of its own.
	AnnotationReviewRun = "sandbox.gemini.google.com/recipe-review-run"
	// AnnotationFixRun is a fix's.
	AnnotationFixRun = "sandbox.gemini.google.com/fix-run"
)

// RecordedRun is the run factory recorded on a sandbox.
type RecordedRun struct {
	// Key is the annotation it is recorded under; not part of the record.
	Key  string `json:"-"`
	Name string `json:"name,omitempty"`
	Task string `json:"task"`
	// Session is the task whose agent session the run continued (a
	// recipe of a session tag's); empty for a task that ran in its own.
	Session   string    `json:"session,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// Recipe is the recipe the run ran, and Kind the kind of task output
	// it writes, if it declares one.
	Recipe string `json:"recipe,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Source is where the recipe came from: builtin, or the repository's
	// .agents/recipes/<name>.yaml@<commit>. Empty on runs recorded before
	// factory said.
	Source string `json:"source,omitempty"`
	// State is the run's own: Running, Completed or Failed. EndedAt is
	// when it stopped running.
	State   string     `json:"state,omitempty"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
}

// SessionOf is the agent session the run is in: the one it continued,
// else its task's own. A session is named after the task that started
// it.
func (r RecordedRun) SessionOf() string {
	return cmp.Or(r.Session, r.Task)
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

// RecordedRunAt is the run recorded under key, false when there is none.
func RecordedRunAt(annotations map[string]string, key string) (RecordedRun, bool) {
	var run RecordedRun
	if err := json.Unmarshal([]byte(annotations[key]), &run); err != nil || run.Task == "" {
		return RecordedRun{}, false
	}
	run.Key = key
	return run, true
}

// RecordedRunSession is the agent session of the run recorded under key,
// empty when there is none: the run's task's, or the session it
// continued.
func RecordedRunSession(annotations map[string]string, key string) string {
	run, _ := RecordedRunAt(annotations, key)
	return run.SessionOf()
}

// runAnnotation is the prefix and suffix of the annotations factory
// records runs under: sandbox.gemini.google.com/<task type>-run.
const (
	runAnnotationPrefix = "sandbox.gemini.google.com/"
	runAnnotationSuffix = "-run"
)

// Runs are the runs recorded on a sandbox, whichever recipes they are,
// newest first.
func Runs(annotations map[string]string) []RecordedRun {
	var runs []RecordedRun
	for key := range annotations {
		if !strings.HasPrefix(key, runAnnotationPrefix) || !strings.HasSuffix(key, runAnnotationSuffix) {
			continue
		}
		if run, ok := RecordedRunAt(annotations, key); ok {
			runs = append(runs, run)
		}
	}
	slices.SortFunc(runs, func(a, b RecordedRun) int {
		return cmp.Or(b.StartedAt.Compare(a.StartedAt), strings.Compare(a.Key, b.Key))
	})
	return runs
}

// RunStatus is where a run stands for whoever it is for: "running",
// "failed", "ready" (a draft waiting on someone: an output nothing but an
// edit has been applied to) or "done". A ready or failed run needs them.
func RunStatus(state string, hasOutput bool, applied map[string]string) string {
	switch state {
	case "Running":
		return "running"
	case "Failed":
		return "failed"
	}
	if !hasOutput {
		return "done"
	}
	for action := range applied {
		if action != "edit" {
			return "done"
		}
	}
	return "ready"
}

// Status is the run's RunStatus on the sandbox annotated annotations.
func (r RecordedRun) Status(annotations map[string]string) string {
	return RunStatus(r.State, annotations[OutputAnnotation(r.Key)] != "", Applied(annotations, AppliedAnnotation(r.Key)))
}

// SessionRun is the recorded run whose agent session is session: what a
// session view offers comes from it, whichever recipe it is.
func SessionRun(annotations map[string]string, session string) (RecordedRun, bool) {
	if session == "" {
		return RecordedRun{}, false
	}
	for _, run := range Runs(annotations) {
		if run.SessionOf() == session {
			return run, true
		}
	}
	return RecordedRun{}, false
}
