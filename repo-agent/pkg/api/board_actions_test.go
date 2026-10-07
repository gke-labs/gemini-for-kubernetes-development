package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	number int
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

// A row says a write filed on its runs stands (Posting), so the feed is
// kept fresh and the UI polls fast until the controller has done it.
func TestRowPostingWhileAWriteStands(t *testing.T) {
	r, dyn := planSessionServer(t, false)
	h := actionHarness{t: t, r: r, dyn: dyn, number: 42}
	if h.row().Posting {
		t.Fatal("posting before any write")
	}
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/draft/comment", `{}`); w.Code != http.StatusAccepted {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	invalidateWorkFeed("alice", "myboard")
	if !h.row().Posting {
		t.Error("not posting while the comment stands")
	}
	h.settle(h.requestOf(boardv1alpha1.VerbApply), boardv1alpha1.RequestSucceeded, "", factorycli.AnnotationPlanApplied, "comment")
	if h.row().Posting {
		t.Error("posting once the comment is done")
	}
}

// Without triage access the viewer may still post a triage's assessment —
// anyone may comment on a public issue — but not add its labels.
func TestATriageSessionWithoutTriageAccess(t *testing.T) {
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "triage/myboard/20/1", Task: "recipe-triage-1", StartedAt: time.Unix(1_000_000, 0), Kind: "Triage",
	})
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"repo":                            "repo",
			annoBoard:                         "myboard",
			factorycli.AnnotationTriageRun:    string(run),
			factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]\n  assessment: A crash."),
			"htmlURL":                         "https://github.com/test/repo/issues/20",
		}, 1)
	gh := issueFeed(20)
	gh["https://api.github.com/repos/test/repo"] = `{"permissions": {"pull": true}}`
	server, r, dyn, _ := boardTestServerWithRT(t, gh, boardCR(), triageSandbox)
	r.POST("/api/task-sessions/:sandbox/:task/draft/:verb", server.taskSessionDraftAction)
	at := "/api/task-sessions/fix-repo-20/recipe-triage-1/draft/"

	if w := doJSON(t, r, http.MethodPost, at+"label", `{}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), needsTriageAccess) {
		t.Errorf("label: got %d %s, want 409 for want of triage access", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, at+"comment", `{}`); w.Code != http.StatusAccepted {
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
