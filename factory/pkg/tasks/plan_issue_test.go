package tasks

import (
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

func TestRenderPlanPrompt(t *testing.T) {
	issue := githubv39.Issue{Number: intp(42), Title: strp("crash on empty input"), Body: strp("details")}

	fresh, err := RenderPlanPrompt(PlanParams{Issue: issue, HTMLURL: "https://github.com/o/r/issues/42"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fresh), "PREVIOUS PLAN") || strings.Contains(string(fresh), "MAINTAINER FEEDBACK") {
		t.Errorf("fresh plan prompt must not carry refinement sections:\n%s", fresh)
	}

	refine, err := RenderPlanPrompt(PlanParams{
		Issue:     issue,
		HTMLURL:   "https://github.com/o/r/issues/42",
		PriorPlan: "## Summary\nold plan",
		Feedback:  "steps 2 and 3 should be one migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PREVIOUS PLAN", "old plan", "MAINTAINER FEEDBACK", "one migration"} {
		if !strings.Contains(string(refine), want) {
			t.Errorf("refinement prompt missing %q", want)
		}
	}
}

func TestGetPlanScript(t *testing.T) {
	script, err := GetPlanScript()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plan-output.txt", "/workspaces/plan-issue-${ISSUE_NUMBER}.md"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("plan script missing %q", want)
		}
	}
}

func TestPlanFilePath(t *testing.T) {
	if got := PlanFilePath(7); got != "/workspaces/plan-issue-7.md" {
		t.Errorf("PlanFilePath(7) = %q", got)
	}
}
