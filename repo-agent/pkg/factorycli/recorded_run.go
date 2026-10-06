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
	// AnnotationReviewRun is a review's: the recipe has no task type, so
	// it runs as recipe-review, the main task of a sandbox of its own.
	AnnotationReviewRun = "sandbox.gemini.google.com/recipe-review-run"
	// AnnotationFixRun is a fix's, and its follow-ups' (the revises of
	// its session).
	AnnotationFixRun = "sandbox.gemini.google.com/fix-run"
)

// RecordedRun is the run factory recorded on a sandbox.
type RecordedRun struct {
	Name string `json:"name,omitempty"`
	Task string `json:"task"`
	// Session is the task whose agent session a revise ran in; empty for
	// a task that ran in its own.
	Session   string    `json:"session,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// Revises are the revises the run's recipe offers into its session.
	Revises []RecordedRevise `json:"revises,omitempty"`
}

// RecordedRevise is one of a recipe's revises: its id and its button's
// label.
type RecordedRevise struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
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

// RecordedRunSession is the agent session of the run recorded under key,
// empty when there is none: the run's task's, or for a revise, the
// session it revised in, so every revise of a plan is one conversation.
// A session is named after the task that started it.
func RecordedRunSession(annotations map[string]string, key string) string {
	var run RecordedRun
	if err := json.Unmarshal([]byte(annotations[key]), &run); err != nil {
		return ""
	}
	if run.Session != "" {
		return run.Session
	}
	return run.Task
}

// RecordedRunRevises are the revises of the run recorded under key, none
// when there is no run.
func RecordedRunRevises(annotations map[string]string, key string) []RecordedRevise {
	var run RecordedRun
	if err := json.Unmarshal([]byte(annotations[key]), &run); err != nil {
		return nil
	}
	return run.Revises
}

// recordedRunKinds are the runs a sandbox records, with the kind of task
// output each writes.
var recordedRunKinds = []struct{ key, kind string }{
	{AnnotationPlanRun, "Plan"},
	{AnnotationTriageRun, "Triage"},
	{ResearchRunAnnotation, "Notes"},
	{AnnotationReviewRun, "Review"},
	{AnnotationFixRun, "Change"},
}

// SessionRun is the recorded run whose agent session is session, with the
// kind of task output its recipe writes: what a session view offers comes
// from it, whichever recipe it is.
func SessionRun(annotations map[string]string, session string) (RecordedRun, string, bool) {
	if session == "" {
		return RecordedRun{}, "", false
	}
	for _, k := range recordedRunKinds {
		var run RecordedRun
		if err := json.Unmarshal([]byte(annotations[k.key]), &run); err != nil {
			continue
		}
		if run.Session == session || (run.Session == "" && run.Task == session) {
			return run, k.kind, true
		}
	}
	return RecordedRun{}, "", false
}
