package factorycli

import (
	"slices"
	"testing"
)

func TestSessionRun(t *testing.T) {
	annotations := map[string]string{
		AnnotationPlanRun:     `{"name":"p2","task":"recipe-plan-2","session":"recipe-plan-1","startedAt":"2026-10-05T12:00:00Z","revises":[{"id":"plan","label":"Update plan"}]}`,
		ResearchRunAnnotation: `{"name":"r1","task":"research-1","startedAt":"2026-10-05T12:00:00Z","revises":[{"id":"notes","label":"Save notes"}]}`,
		AnnotationTriageRun:   `not json`,
	}
	for _, tc := range []struct {
		session, kind string
		revise        string
	}{
		// A revise's run answers for the session it revised in.
		{"recipe-plan-1", "Plan", "plan"},
		{"research-1", "Notes", "notes"},
		// The revise's own task is not a session.
		{"recipe-plan-2", "", ""},
		{"", "", ""},
	} {
		run, kind, ok := SessionRun(annotations, tc.session)
		if ok != (tc.kind != "") || kind != tc.kind {
			t.Errorf("SessionRun(%q) = %q, %v; want %q", tc.session, kind, ok, tc.kind)
			continue
		}
		if ok && !slices.ContainsFunc(run.Revises, func(r RecordedRevise) bool { return r.ID == tc.revise }) {
			t.Errorf("SessionRun(%q).Revises = %+v, want %s", tc.session, run.Revises, tc.revise)
		}
	}
}
