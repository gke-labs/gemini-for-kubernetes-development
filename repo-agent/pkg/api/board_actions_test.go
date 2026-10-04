package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

func issueFeed(number int) map[string]string {
	return map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": ` + itoa(number) + `, "title": "an issue", "html_url": "https://github.com/test/repo/issues/` + itoa(number) + `", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
}

type actionHarness struct {
	t      *testing.T
	r      *gin.Engine
	dyn    *fake.FakeDynamicClient
	rt     *boardMockRT
	number int
}

func (h actionHarness) act(kind, verb, run, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"kind": kind, "run": run, "text": text})
	req, _ := http.NewRequest("POST", "/board/myboard/issues/"+itoa(h.number)+"/actions/"+verb, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, req)
	return w
}

func (h actionHarness) row() models.WorkItem {
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == h.number {
			return item
		}
	}
	h.t.Fatalf("issue %d missing: %s", h.number, w.Body.String())
	return models.WorkItem{}
}

func (h actionHarness) annotations() map[string]string {
	sb, err := h.dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), "fix-repo-"+itoa(h.number), v1.GetOptions{})
	if err != nil {
		h.t.Fatalf("get sandbox: %v", err)
	}
	return sb.GetAnnotations()
}

func verbsOf(actions []models.WorkAction) (verbs []string, enabled []bool) {
	for _, a := range actions {
		verbs = append(verbs, a.Verb)
		enabled = append(enabled, a.Enabled)
	}
	return verbs, enabled
}

// A plan's row carries the actions its task output declares, and the
// action endpoint takes exactly those: posting it once, then fixing with
// it.
func TestPlanActions(t *testing.T) {
	gh := issueFeed(42)
	gh["https://api.github.com/repos/test/repo/issues/42/comments?per_page=100"] = `[]`
	planSandbox := sandboxCR("fix-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"htmlURL": "https://github.com/test/repo/issues/42",
			"sandbox.gemini.google.com/last-task-type":  "plan",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"board.gemini.google.com/plan":              "## Summary\nDo the thing.",
			"board.gemini.google.com/planned-at":        "2026-09-17T00:00:00Z",
			factorycli.AnnotationPlanOutput: `apiVersion: factory.gemini.google.com/v1alpha1
kind: Plan
source:
  task: recipe-plan-1
actions:
  - verb: comment
    label: Post plan
  - verb: run
    run: fix
    label: Fix with this plan
  - verb: reject
`,
		}, 1)
	_, r, dyn, rt := boardTestServerWithRT(t, gh, boardCR(), planSandbox)
	h := actionHarness{t: t, r: r, dyn: dyn, rt: rt, number: 42}

	verbs, enabled := verbsOf(h.row().PlanActions)
	if !reflect.DeepEqual(verbs, []string{"comment", "run", "reject"}) || slices.Contains(enabled, false) {
		t.Fatalf("plan actions = %+v", h.row().PlanActions)
	}
	if w := h.act("Plan", "edit", "", "## new"); w.Code != http.StatusBadRequest {
		t.Errorf("edit, not offered: got %d %s", w.Code, w.Body.String())
	}

	if w := h.act("Plan", "comment", "", ""); w.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(rt.writes, "POST /repos/test/repo/issues/42/comments") || h.annotations()[factorycli.AnnotationPlanCommented] == "" {
		t.Errorf("comment: writes %q, annotations %v", rt.writes, h.annotations())
	}
	if a := h.row().PlanActions[0]; a.Enabled || a.Reason != "plan posted" {
		t.Errorf("comment after posting = %+v", a)
	}
	if w := h.act("Plan", "comment", "", ""); w.Code != http.StatusConflict {
		t.Errorf("second comment: got %d", w.Code)
	}

	if w := h.act("Plan", "run", "fix", ""); w.Code != http.StatusOK {
		t.Fatalf("run fix: %d %s", w.Code, w.Body.String())
	}
	if h.annotations()["board.gemini.google.com/plan-approved-at"] == "" {
		t.Error("run fix did not approve the plan")
	}
	if filed := theRequest(t, dyn, "alice"); filed.Spec.Verb != boardv1alpha1.VerbFix || filed.Spec.Number != 42 {
		t.Errorf("run fix filed %+v", filed.Spec)
	}
	// Approved: nothing left to do with the draft.
	if acts := h.row().PlanActions; len(acts) != 0 {
		t.Errorf("approved plan offers %+v", acts)
	}
}

// A triage without a task output (stored before they were kept) offers the
// defaults; labeling and commenting are separate, and posting ends editing.
func TestTriageActions(t *testing.T) {
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			factorycli.AnnotationTriageDraft:                     "triage:\n  labels: [bug]\n  assessment: A crash.",
			"board.gemini.google.com/triaged-at":                 "2026-09-17T00:00:00Z",
			"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
			"htmlURL": "https://github.com/test/repo/issues/20",
		}, 1)
	gh := issueFeed(20)
	gh["POST https://api.github.com/repos/test/repo/issues/20/labels"] = `[{"name": "bug"}]`
	_, r, dyn, rt := boardTestServerWithRT(t, gh, boardCR(), triageSandbox)
	h := actionHarness{t: t, r: r, dyn: dyn, rt: rt, number: 20}

	verbs, _ := verbsOf(h.row().TriageActions)
	if !reflect.DeepEqual(verbs, []string{"edit", "label", "comment", "reject"}) {
		t.Fatalf("triage actions = %+v", h.row().TriageActions)
	}

	if w := h.act("Triage", "label", "", ""); w.Code != http.StatusOK {
		t.Fatalf("label: %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(rt.writes, []string{"POST /repos/test/repo/issues/20/labels"}) {
		t.Errorf("label wrote %q", rt.writes)
	}
	if a := h.annotations(); a[factorycli.AnnotationTriageLabeled] == "" || a[annoTriagePublished] != "" {
		t.Errorf("label: annotations %v", a)
	}

	if w := h.act("Triage", "comment", "", ""); w.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(rt.writes, "POST /repos/test/repo/issues/20/comments") || h.annotations()[annoTriagePublished] == "" {
		t.Errorf("comment: writes %q", rt.writes)
	}
	if w := h.act("Triage", "edit", "", "triage:\n  labels: [x]"); w.Code != http.StatusConflict {
		t.Errorf("edit after posting: got %d", w.Code)
	}
	if w := h.act("Triage", "reject", "", ""); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if a := h.annotations(); a[factorycli.AnnotationTriageDraft] != "" || a[factorycli.AnnotationTriageLabeled] != "" {
		t.Errorf("reject left %v", a)
	}
}
