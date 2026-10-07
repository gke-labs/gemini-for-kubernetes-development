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
	"time"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
		// Triage, not push: labelling an issue needs no more.
		"https://api.github.com/repos/test/repo": `{"permissions": {"triage": true, "pull": true}}`,
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
	// run is a revise's id, too.
	body, _ := json.Marshal(map[string]string{"kind": kind, "run": run, "revise": run, "text": text})
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

// requestOf is the one Request of verb filed.
func (h actionHarness) requestOf(verb string) boardv1alpha1.Request {
	h.t.Helper()
	var found []boardv1alpha1.Request
	for _, req := range filedRequests(h.t, h.dyn, "alice") {
		if req.Spec.Verb == verb {
			found = append(found, req)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("filed %d %s requests, want one: %+v", len(found), verb, found)
	}
	return found[0]
}

// settle does what the controller does once the write has run: the
// Request's verdict, and on success action stamped applied under
// appliedKey.
func (h actionHarness) settle(req boardv1alpha1.Request, phase, message, appliedKey, action string) {
	h.t.Helper()
	ctx := context.Background()
	obj, err := h.dyn.Resource(requestGVR).Namespace("alice").Get(ctx, req.Name, v1.GetOptions{})
	if err != nil {
		h.t.Fatalf("get request: %v", err)
	}
	_ = unstructured.SetNestedField(obj.Object, phase, "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, message, "status", "message")
	if _, err := h.dyn.Resource(requestGVR).Namespace("alice").Update(ctx, obj, v1.UpdateOptions{}); err != nil {
		h.t.Fatalf("update request: %v", err)
	}
	if appliedKey != "" {
		sb, err := h.dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(ctx, "fix-repo-"+itoa(h.number), v1.GetOptions{})
		if err != nil {
			h.t.Fatalf("get sandbox: %v", err)
		}
		a := sb.GetAnnotations()
		factorycli.MarkApplied(a, appliedKey, action, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
		sb.SetAnnotations(a)
		if _, err := h.dyn.Resource(k8s.SandboxGVR).Namespace("alice").Update(ctx, sb, v1.UpdateOptions{}); err != nil {
			h.t.Fatalf("update sandbox: %v", err)
		}
	}
	invalidateWorkFeed("alice", "myboard")
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
	planSandbox := sandboxCR("fix-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"htmlURL": "https://github.com/test/repo/issues/42",
			"sandbox.gemini.google.com/last-task-type":  "plan",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"board.gemini.google.com/planned-at":        "2026-09-17T00:00:00Z",
			factorycli.AnnotationPlanOutput: `apiVersion: factory.gemini.google.com/v1alpha1
kind: Plan
source:
  task: recipe-plan-1
spec:
  markdown: |-
    ## Summary
    Do the thing.
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

	// Posting is the controller's: the click files the write, and the
	// board writes nothing to GitHub itself.
	if w := h.act("Plan", "comment", "", ""); w.Code != http.StatusAccepted {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	if len(rt.writes) != 0 {
		t.Errorf("comment wrote to GitHub: %q", rt.writes)
	}
	filed := h.requestOf(boardv1alpha1.VerbApply)
	if filed.Spec.Number != 42 || filed.Spec.Member != "alice" || filed.Spec.Apply == nil ||
		*filed.Spec.Apply != (boardv1alpha1.ApplyRequest{Run: "plan", Action: "comment"}) {
		t.Errorf("comment filed %+v", filed.Spec)
	}
	if a := h.row().PlanActions[0]; a.Enabled || a.Reason != "posting" {
		t.Errorf("comment while posting = %+v", a)
	}
	// A second click lands on the standing write.
	if w := h.act("Plan", "comment", "", ""); w.Code != http.StatusAccepted {
		t.Errorf("comment while posting: got %d", w.Code)
	}
	h.requestOf(boardv1alpha1.VerbApply)

	// A failed write says why, and can be clicked again.
	h.settle(filed, boardv1alpha1.RequestFailed, "403 Resource not accessible", "", "")
	if a := h.row().PlanActions[0]; !a.Enabled || a.Error != "403 Resource not accessible" {
		t.Errorf("comment after a failure = %+v", a)
	}
	h.settle(filed, boardv1alpha1.RequestSucceeded, "", factorycli.AnnotationPlanApplied, "comment")
	if a := h.row().PlanActions[0]; a.Enabled || a.Reason != "plan posted" || a.Error != "" {
		t.Errorf("comment after posting = %+v", a)
	}
	if w := h.act("Plan", "comment", "", ""); w.Code != http.StatusConflict {
		t.Errorf("second comment: got %d", w.Code)
	}

	if w := h.act("Plan", "run", "fix", ""); w.Code != http.StatusOK {
		t.Fatalf("run fix: %d %s", w.Code, w.Body.String())
	}
	if !factorycli.IsApplied(h.annotations(), factorycli.AnnotationPlanApplied, "run") {
		t.Error("run fix did not approve the plan")
	}
	if filed := h.requestOf(boardv1alpha1.VerbRecipe); filed.Spec.Number != 42 {
		t.Errorf("run fix filed %+v", filed.Spec)
	}
	// Approved: nothing left to do with the draft.
	if acts := h.row().PlanActions; len(acts) != 0 {
		t.Errorf("approved plan offers %+v", acts)
	}
}

// A plan's revises are offered by id; a click files a revise for the
// controller, and the draft's other actions wait for the plan it writes.
func TestPlanRevise(t *testing.T) {
	gh := issueFeed(42)
	planSandbox := sandboxCR("fix-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"htmlURL": "https://github.com/test/repo/issues/42",
			"sandbox.gemini.google.com/last-task-type":  "plan",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"board.gemini.google.com/planned-at":        "2026-09-17T00:00:00Z",
			factorycli.AnnotationPlanOutput: `apiVersion: factory.gemini.google.com/v1alpha1
kind: Plan
source:
  task: recipe-plan-1
spec:
  markdown: |-
    ## Summary
    Do the thing.
actions:
  - verb: comment
    label: Post plan
  - verb: reject
  - verb: revise
    revise: plan
    label: Update plan
`,
		}, 1)
	_, r, dyn, rt := boardTestServerWithRT(t, gh, boardCR(), planSandbox)
	h := actionHarness{t: t, r: r, dyn: dyn, rt: rt, number: 42}

	acts := h.row().PlanActions
	if len(acts) != 3 || acts[2].Verb != "revise" || acts[2].Revise != "plan" || acts[2].Label != "Update plan" || !acts[2].Enabled {
		t.Fatalf("plan actions = %+v", acts)
	}
	if w := h.act("Plan", "revise", "other", ""); w.Code != http.StatusBadRequest {
		t.Errorf("a revise not offered: got %d %s", w.Code, w.Body.String())
	}
	if w := h.act("Plan", "revise", "plan", ""); w.Code != http.StatusAccepted {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	if len(rt.writes) != 0 {
		t.Errorf("revise wrote to GitHub: %q", rt.writes)
	}
	filed := h.requestOf(boardv1alpha1.VerbRevise)
	if filed.Spec.Number != 42 || filed.Spec.Member != "alice" || filed.Spec.Revise != "plan" {
		t.Errorf("revise filed %+v", filed.Spec)
	}
	acts = h.row().PlanActions
	if a := acts[2]; a.Enabled || a.Reason != "revising" {
		t.Errorf("revise while revising = %+v", a)
	}
	if a := acts[0]; a.Enabled || a.Reason != "the plan is being revised" {
		t.Errorf("comment while revising = %+v", a)
	}
	if a := acts[1]; !a.Enabled {
		t.Errorf("reject while revising = %+v", a)
	}

	// A failed revise says why, and can be clicked again.
	h.settle(filed, boardv1alpha1.RequestFailed, "a turn is in flight", "", "")
	acts = h.row().PlanActions
	if a := acts[2]; !a.Enabled || a.Error != "a turn is in flight" {
		t.Errorf("revise after a failure = %+v", a)
	}
	if a := acts[0]; !a.Enabled {
		t.Errorf("comment after a failed revise = %+v", a)
	}
}

// A triage without a task output (stored before they were kept) offers the
// defaults; labeling and commenting are separate, and posting ends editing.
func TestTriageActions(t *testing.T) {
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			factorycli.AnnotationTriageOutput:                    storedOutput("Triage", "triage:\n  labels: [bug]\n  assessment: A crash."),
			"board.gemini.google.com/triaged-at":                 "2026-09-17T00:00:00Z",
			"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
			"htmlURL": "https://github.com/test/repo/issues/20",
		}, 1)
	gh := issueFeed(20)
	_, r, dyn, rt := boardTestServerWithRT(t, gh, boardCR(), triageSandbox)
	h := actionHarness{t: t, r: r, dyn: dyn, rt: rt, number: 20}

	verbs, _ := verbsOf(h.row().TriageActions)
	if !reflect.DeepEqual(verbs, []string{"edit", "label", "comment", "reject"}) {
		t.Fatalf("triage actions = %+v", h.row().TriageActions)
	}

	if w := h.act("Triage", "label", "", ""); w.Code != http.StatusAccepted {
		t.Fatalf("label: %d %s", w.Code, w.Body.String())
	}
	label := h.requestOf(boardv1alpha1.VerbApply)
	if *label.Spec.Apply != (boardv1alpha1.ApplyRequest{Run: "recipe-triage", Action: "label"}) {
		t.Errorf("label filed %+v", label.Spec)
	}
	// Labeling and commenting are separate writes, so both can stand.
	if w := h.act("Triage", "comment", "", ""); w.Code != http.StatusAccepted {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	if n := len(filedRequests(t, dyn, "alice")); n != 2 {
		t.Errorf("filed %d requests, want label and comment", n)
	}
	if len(rt.writes) != 0 {
		t.Errorf("triage wrote to GitHub: %q", rt.writes)
	}
	h.settle(label, boardv1alpha1.RequestSucceeded, "", factorycli.AnnotationTriageApplied, "label")
	for _, req := range filedRequests(t, dyn, "alice") {
		if req.Spec.Apply.Action == "comment" {
			h.settle(req, boardv1alpha1.RequestSucceeded, "", factorycli.AnnotationTriageApplied, "comment")
		}
	}
	if w := h.act("Triage", "edit", "", "triage:\n  labels: [x]"); w.Code != http.StatusConflict {
		t.Errorf("edit after posting: got %d", w.Code)
	}
	if w := h.act("Triage", "reject", "", ""); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if a := h.annotations(); a[factorycli.AnnotationTriageOutput] != "" || a[factorycli.AnnotationTriageApplied] != "" {
		t.Errorf("reject left %v", a)
	}
}

// Without triage access the viewer may still post a triage's assessment —
// anyone may comment on a public issue — but not add its labels, which
// the feed says and the endpoint holds to.
func TestTriageActionsWithoutTriageAccess(t *testing.T) {
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			factorycli.AnnotationTriageOutput:    storedOutput("Triage", "triage:\n  labels: [bug]\n  assessment: A crash."),
			"board.gemini.google.com/triaged-at": "2026-09-17T00:00:00Z",
			"htmlURL":                            "https://github.com/test/repo/issues/20",
		}, 1)
	gh := issueFeed(20)
	gh["https://api.github.com/repos/test/repo"] = `{"permissions": {"pull": true}}`
	_, r, dyn, rt := boardTestServerWithRT(t, gh, boardCR(), triageSandbox)
	h := actionHarness{t: t, r: r, dyn: dyn, rt: rt, number: 20}

	actions := h.row().TriageActions
	label, _ := findWorkAction(actions, "label", "")
	if label.Enabled || label.Reason != needsTriageAccess {
		t.Errorf("label = %+v, want disabled for want of triage access", label)
	}
	if comment, _ := findWorkAction(actions, "comment", ""); !comment.Enabled {
		t.Errorf("comment = %+v, want enabled", comment)
	}

	if w := h.act("Triage", "label", "", ""); w.Code != http.StatusConflict {
		t.Errorf("label: got %d %s, want 409", w.Code, w.Body.String())
	}
	if w := h.act("Triage", "comment", "", ""); w.Code != http.StatusAccepted {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	reqs := filedRequests(t, dyn, "alice")
	if len(reqs) != 1 || reqs[0].Spec.Apply.Action != "comment" {
		t.Errorf("filed %+v, want the comment only", reqs)
	}
}

// storedOutput is a task output of kind with draft as its spec, as the
// board stores one, naming no task.
func storedOutput(kind, draft string) string {
	doc, err := factorycli.ComposeTaskOutput(kind, "", draft, "", "")
	if err != nil {
		panic(err)
	}
	return doc
}

// Who may click a write is the verb's: label needs triage, a verb the
// board does not know needs push, and the rest anyone with the board.
func TestActionsFor(t *testing.T) {
	actions := []models.WorkAction{
		{Verb: "comment", Enabled: true}, {Verb: "label", Enabled: true},
		{Verb: "frobnicate", Enabled: true}, {Verb: "reject", Enabled: true},
	}
	enabled := func(perms repoPerms) []bool {
		var got []bool
		for _, a := range actionsFor(actions, perms) {
			got = append(got, a.Enabled)
		}
		return got
	}
	for _, tc := range []struct {
		perms repoPerms
		want  []bool
	}{
		{repoPerms{}, []bool{true, false, false, true}},
		{repoPerms{triage: true}, []bool{true, true, false, true}},
		{repoPerms{triage: true, push: true}, []bool{true, true, true, true}},
	} {
		if got := enabled(tc.perms); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("actionsFor(%+v) enabled = %v, want %v", tc.perms, got, tc.want)
		}
	}
	if !actions[1].Enabled {
		t.Error("actionsFor changed its input")
	}
}
