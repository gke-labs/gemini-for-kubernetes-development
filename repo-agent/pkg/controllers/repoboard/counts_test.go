package repoboard

import (
	"encoding/json"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// The board's needs-you count is its runs waiting on someone, whichever
// recipe: per sandbox, the newest run of each group, when it is a draft
// to judge or failed.
func TestNeedsHumanCountsReadyAndFailedRuns(t *testing.T) {
	now := time.Now()
	sandbox := func(runs map[string]factorycli.RecordedRun, extra map[string]string) *unstructured.Unstructured {
		a := map[string]string{}
		for taskType, run := range runs {
			b, _ := json.Marshal(run)
			a[factorycli.RunAnnotation(taskType)] = string(b)
		}
		for k, v := range extra {
			a[k] = v
		}
		sb := &unstructured.Unstructured{}
		sb.SetAnnotations(a)
		return sb
	}
	ended := now.Add(-time.Minute)
	run := func(recipe, state string, ago time.Duration) factorycli.RecordedRun {
		return factorycli.RecordedRun{Recipe: recipe, Task: recipe + "-1", State: state, StartedAt: now.Add(-ago), EndedAt: &ended}
	}
	catalog := []boardv1alpha1.BoardRecipe{{Name: "care", Session: "care"}, {Name: "care-ci", Session: "care"}}
	doc := "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\nspec:\n  markdown: x\n"
	sandboxes := []*unstructured.Unstructured{
		// A plan draft to judge: 1.
		sandbox(map[string]factorycli.RecordedRun{"plan": run("plan", "Completed", time.Hour)},
			map[string]string{factorycli.OutputAnnotation(factorycli.RunAnnotation("plan")): doc}),
		// A triage draft already posted, a fix that failed: 1.
		sandbox(map[string]factorycli.RecordedRun{
			"recipe-triage": run("triage", "Completed", time.Hour),
			"fix":           run("fix", "Failed", time.Hour),
		}, map[string]string{
			factorycli.OutputAnnotation(factorycli.RunAnnotation("recipe-triage")):  doc,
			factorycli.AppliedAnnotation(factorycli.RunAnnotation("recipe-triage")): `{"comment":"2026-10-14T00:00:00Z"}`,
		}),
		// care-ci failed, but care, of the same session, ran since: 0.
		sandbox(map[string]factorycli.RecordedRun{
			"recipe-care-ci": run("care-ci", "Failed", 2*time.Hour),
			"recipe-care":    run("care", "Running", time.Hour),
		}, nil),
	}
	if got := needsHuman(sandboxes, catalog); got != 2 {
		t.Errorf("needsHuman = %d, want 2", got)
	}
}
