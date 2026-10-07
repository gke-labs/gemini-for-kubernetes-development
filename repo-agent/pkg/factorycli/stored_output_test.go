package factorycli

import (
	"strings"
	"testing"
	"time"
)

// A run's stored output and its applied stamp are named for its run
// annotation, so any recipe's run has one.
func TestStoredOutputAnnotations(t *testing.T) {
	for run, want := range map[string]string{
		AnnotationTriageRun:   AnnotationTriageOutput,
		AnnotationPlanRun:     AnnotationPlanOutput,
		ResearchRunAnnotation: AnnotationNotesOutput,
		AnnotationFixRun:      "board.gemini.google.com/fix-output",
	} {
		if got := OutputAnnotation(run); got != want {
			t.Errorf("OutputAnnotation(%s) = %s, want %s", run, got, want)
		}
	}
	if got := AppliedAnnotation(AnnotationPlanRun); got != AnnotationPlanApplied {
		t.Errorf("AppliedAnnotation = %s", got)
	}
	if AppliedAnnotation(AnnotationTriageRun) != AnnotationTriageApplied || AppliedAnnotation(ResearchRunAnnotation) != AnnotationNotesApplied {
		t.Error("the triage's or the notes' applied annotation is not their run's")
	}
}

func TestMarkApplied(t *testing.T) {
	a := map[string]string{}
	if IsApplied(a, AnnotationPlanApplied, "comment") {
		t.Fatal("applied before anything was")
	}
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	MarkApplied(a, AnnotationPlanApplied, "comment", at)
	MarkApplied(a, AnnotationPlanApplied, "run", at)
	if got := Applied(a, AnnotationPlanApplied); got["comment"] != "2026-10-06T12:00:00Z" || got["run"] == "" || len(got) != 2 {
		t.Errorf("applied = %v (%s)", got, a[AnnotationPlanApplied])
	}
	a[AnnotationPlanApplied] = "not json"
	if IsApplied(a, AnnotationPlanApplied, "comment") {
		t.Error("a stamp that does not parse read as applied")
	}
}

// An edit rewrites the stored output's spec and keeps the rest: its
// source, its actions, a Notes' name.
func TestWithDraft(t *testing.T) {
	doc, err := WithDraft("Plan", planTaskOutput+"actions:\n  - verb: comment\n", "## Edited\n")
	if err != nil {
		t.Fatal(err)
	}
	if Draft("Plan", doc) != "## Edited" || TaskOutputTask("Plan", doc) != "recipe-plan-20261003-120000-0001" ||
		len(OfferedActions("Plan", doc)) != 1 {
		t.Errorf("edited plan:\n%s", doc)
	}

	notes := "apiVersion: " + TaskOutputAPIVersion + "\nkind: Notes\nspec:\n  name: my-notes\n  markdown: old\n"
	doc, err = WithDraft("Notes", notes, "new")
	if err != nil {
		t.Fatal(err)
	}
	if Draft("Notes", doc) != "new" || !strings.Contains(doc, "name: my-notes") {
		t.Errorf("edited notes:\n%s", doc)
	}

	triage := "apiVersion: " + TaskOutputAPIVersion + "\nkind: Triage\nsource:\n  task: t1\nspec:\n  labels: [bug]\n  assessment: old\n"
	doc, err = WithDraft("Triage", triage, "triage:\n  labels: [feature]\n  assessment: new")
	if err != nil {
		t.Fatal(err)
	}
	if got := Draft("Triage", doc); !strings.Contains(got, "feature") || strings.Contains(got, "bug") || !strings.Contains(got, "assessment: new") ||
		TaskOutputTask("Triage", doc) != "t1" {
		t.Errorf("edited triage:\n%s", doc)
	}

	if _, err := WithDraft("Plan", "", "x"); err == nil {
		t.Error("edited a plan that is not stored")
	}
	if _, err := WithDraft("Plan", planTaskOutput, " "); err == nil {
		t.Error("emptied a plan")
	}
}
