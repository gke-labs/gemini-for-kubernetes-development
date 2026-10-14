package factorycli

import (
	"testing"
)

func TestSessionRun(t *testing.T) {
	annotations := map[string]string{
		AnnotationPlanRun:     `{"name":"p2","task":"recipe-plan-2","session":"recipe-plan-1","startedAt":"2026-10-05T12:00:00Z","kind":"Plan"}`,
		ResearchRunAnnotation: `{"name":"r1","task":"research-1","startedAt":"2026-10-05T12:00:00Z","kind":"Notes"}`,
		// A recipe the board has never heard of is found all the same.
		"sandbox.gemini.google.com/recipe-lint-run": `{"task":"recipe-lint-1","startedAt":"2026-10-05T13:00:00Z","kind":"Lint"}`,
		AnnotationTriageRun:                         `not json`,
		"sandbox.gemini.google.com/other":           `{"task":"x"}`,
	}
	for _, tc := range []struct {
		session, kind string
	}{
		// A run that continued a session answers for it.
		{"recipe-plan-1", "Plan"},
		{"research-1", "Notes"},
		{"recipe-lint-1", "Lint"},
		// Its own task is not a session.
		{"recipe-plan-2", ""},
		{"x", ""},
		{"", ""},
	} {
		run, ok := SessionRun(annotations, tc.session)
		if ok != (tc.kind != "") || run.Kind != tc.kind {
			t.Errorf("SessionRun(%q) = %q, %v; want %q", tc.session, run.Kind, ok, tc.kind)
		}
	}
	if runs := Runs(annotations); len(runs) != 3 || runs[0].Kind != "Lint" {
		t.Errorf("Runs = %+v, want 3, newest (Lint) first", runs)
	}
}
