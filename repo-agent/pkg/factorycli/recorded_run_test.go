package factorycli

import (
	"slices"
	"testing"
)

func TestSessionRun(t *testing.T) {
	annotations := map[string]string{
		AnnotationPlanRun:     `{"name":"p2","task":"recipe-plan-2","session":"recipe-plan-1","startedAt":"2026-10-05T12:00:00Z","kind":"Plan","revises":[{"id":"plan","label":"Update plan"}]}`,
		ResearchRunAnnotation: `{"name":"r1","task":"research-1","startedAt":"2026-10-05T12:00:00Z","kind":"Notes","revises":[{"id":"notes","label":"Save notes"}]}`,
		// A recipe the board has never heard of is found all the same.
		"sandbox.gemini.google.com/recipe-lint-run": `{"task":"recipe-lint-1","startedAt":"2026-10-05T13:00:00Z","kind":"Lint","revises":[{"id":"again","inputs":["instruction"]}]}`,
		AnnotationTriageRun:                         `not json`,
		"sandbox.gemini.google.com/other":           `{"task":"x"}`,
	}
	for _, tc := range []struct {
		session, kind string
		revise        string
	}{
		// A revise's run answers for the session it revised in.
		{"recipe-plan-1", "Plan", "plan"},
		{"research-1", "Notes", "notes"},
		{"recipe-lint-1", "Lint", "again"},
		// The revise's own task is not a session.
		{"recipe-plan-2", "", ""},
		{"x", "", ""},
		{"", "", ""},
	} {
		run, ok := SessionRun(annotations, tc.session)
		if ok != (tc.kind != "") || run.Kind != tc.kind {
			t.Errorf("SessionRun(%q) = %q, %v; want %q", tc.session, run.Kind, ok, tc.kind)
			continue
		}
		if ok && !slices.ContainsFunc(run.Revises, func(r RecordedRevise) bool { return r.ID == tc.revise }) {
			t.Errorf("SessionRun(%q).Revises = %+v, want %s", tc.session, run.Revises, tc.revise)
		}
	}
	if runs := Runs(annotations); len(runs) != 3 || runs[0].Kind != "Lint" {
		t.Errorf("Runs = %+v, want 3, newest (Lint) first", runs)
	}
	run, ok := ReviseRun(annotations, "again")
	if !ok || run.Key != "sandbox.gemini.google.com/recipe-lint-run" || run.SessionOf() != "recipe-lint-1" {
		t.Errorf("ReviseRun(again) = %+v, %v", run, ok)
	}
	if rv, _ := run.Offers("again"); !slices.Equal(rv.Inputs, []string{"instruction"}) {
		t.Errorf("again's inputs = %v", rv.Inputs)
	}
	if _, ok := ReviseRun(annotations, "nope"); ok {
		t.Error("ReviseRun(nope) found a run")
	}
}
