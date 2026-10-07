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

// changeOutput is a Change task output, whose draft is its spec as YAML.
const changeOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Change
source:
  task: recipe-fix-1
spec:
  title: Fix it
  body: Fixes #7.
`

// Any kind's task output is found after its banner, the runner's words
// after the closer left out.
func TestHarvestedTaskOutput(t *testing.T) {
	for _, banner := range []string{changeBanner, reviewBanner, planBanner} {
		out := "Running recipe...\n" + banner + "\n" + changeOutput + bannerCloser + "\nopened #9\n"
		if got := HarvestedTaskOutput(out); got != changeOutput {
			t.Errorf("after %q: %q, want the Change", banner, got)
		}
	}
	if got := HarvestedTaskOutput(triageBanner + "\n" + triageTaskOutput + "\n" + bannerCloser); TaskOutputKind(got) != "Triage" {
		t.Errorf("triage: %q", got)
	}
	for _, out := range []string{"", "posted\n", planBanner + "\njust words\n" + bannerCloser} {
		if got := HarvestedTaskOutput(out); got != "" {
			t.Errorf("%q: %q, want none", out, got)
		}
	}
}

// A new output replaces what was applied of the one before; the same
// again keeps it.
func TestKeepOutput(t *testing.T) {
	a := map[string]string{}
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	if KeepOutput(a, AnnotationFixRun, "", at, "open-pr") || len(a) != 0 {
		t.Fatalf("an empty output kept: %v", a)
	}
	KeepOutput(a, AnnotationFixRun, changeOutput, at, "open-pr")
	MarkApplied(a, AppliedAnnotation(AnnotationFixRun), "post-replies", at)
	KeepOutput(a, AnnotationFixRun, changeOutput, at.Add(time.Hour))
	if got := Applied(a, AppliedAnnotation(AnnotationFixRun)); len(got) != 2 || got["open-pr"] != "2026-10-06T00:00:00Z" {
		t.Errorf("the same output again: applied %v, want both kept", got)
	}
	KeepOutput(a, AnnotationFixRun, strings.Replace(changeOutput, "Fix it", "Fixed", 1), at)
	if a[OutputAnnotation(AnnotationFixRun)] == changeOutput || len(Applied(a, AppliedAnnotation(AnnotationFixRun))) != 0 {
		t.Errorf("a new output: %v, want it stored, nothing applied", a)
	}
}

// A kind without markdown is drafted and edited as its spec's YAML; one
// with markdown as its markdown, its other fields kept.
func TestAnyKindsDraft(t *testing.T) {
	if DraftIsMarkdown("Change", changeOutput) {
		t.Error("a Change's draft is not markdown")
	}
	if got := Draft("Change", changeOutput); got != "title: Fix it\nbody: Fixes #7." {
		t.Errorf("Draft = %q, want the spec", got)
	}
	edited, err := WithDraft("Change", changeOutput, "title: Fixed\nbody: Fixes #7.\nreplies: []\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edited, "title: Fixed") || !strings.Contains(edited, "replies: []") || !strings.Contains(edited, "task: recipe-fix-1") {
		t.Errorf("edited = %q", edited)
	}
	if _, err := WithDraft("Change", changeOutput, "- not\n- a mapping\n"); err == nil {
		t.Error("a spec that is no mapping was taken")
	}
	report := "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Report\nspec:\n  markdown: '# Old'\n  file: r.md\n"
	if !DraftIsMarkdown("Report", report) || Draft("Report", report) != "# Old" {
		t.Errorf("Report draft = %q, want its markdown", Draft("Report", report))
	}
	edited, err = WithDraft("Report", report, "# New\n")
	if err != nil || !strings.Contains(edited, "# New") || !strings.Contains(edited, "file: r.md") {
		t.Errorf("edited = %q, %v", edited, err)
	}
	if Draft("Change", "") != "" || Draft("Plan", changeOutput) != "" {
		t.Error("a draft from no output, or another kind's")
	}
}
