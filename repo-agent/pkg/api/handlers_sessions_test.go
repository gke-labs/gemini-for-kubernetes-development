package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// sessionsTestServer serves GET /api/sessions over sandboxes and pods,
// with the member's board myboard (boardCR) on test/repo.
func sessionsTestServer(t *testing.T, daemon *fakeSessions, sandboxes []*unstructured.Unstructured, pods ...*corev1.Pod) *gin.Engine {
	t.Helper()
	objs := []runtime.Object{}
	for _, p := range pods {
		objs = append(objs, p)
	}
	r, dyn := researchTestServer(t, daemon, sandboxes, objs...)
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(), boardCR(), v1.CreateOptions{}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	server := &Server{K8sManager: &k8s.Manager{Client: dyn, Clientset: kubernetesfake.NewClientset(objs...)}}
	server.Auth = &auth.Authenticator{K8sManager: server.K8sManager}
	r.GET("/api/sessions", server.getSessions)
	return r
}

func listSessions(t *testing.T, r *gin.Engine) []SessionRow {
	t.Helper()
	w := doJSON(t, r, http.MethodGet, "/api/sessions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Sessions []SessionRow `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Sessions
}

func sessionsByTask(rows []SessionRow) map[string]SessionRow {
	out := map[string]SessionRow{}
	for _, r := range rows {
		out[r.Task] = r
	}
	return out
}

// A PR's sandbox holds its fix and its care: two conversations, two rows,
// one sandbox, both on the PR.
func TestSessionsAreConversationsNotSandboxes(t *testing.T) {
	sb := sandboxCR("fix-repo-9",
		map[string]interface{}{factorycli.LabelIssue: "9", "factory.gemini.google.com/pr": "12"},
		map[string]interface{}{
			"htmlURL":                   "https://github.com/test/repo/pull/12",
			factorycli.AnnotationFixRun: `{"task":"recipe-fix-1","recipe":"fix","state":"Completed","startedAt":"2026-10-10T10:00:00Z","endedAt":"2026-10-10T11:00:00Z"}`,
			"sandbox.gemini.google.com/recipe-care-run": `{"task":"recipe-care-ci-2","recipe":"care-ci","state":"Running","startedAt":"2026-10-11T10:00:00Z"}`,
		}, 1)
	daemon := &fakeSessions{live: map[string]acpd.Session{"recipe-care-ci-2": {ID: "recipe-care-ci-2", Busy: true}}}
	r := sessionsTestServer(t, daemon, []*unstructured.Unstructured{sb}, researchPod("alice", "fix-repo-9", "10.0.0.1", corev1.PodRunning))

	rows := listSessions(t, r)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Task != "recipe-care-ci-2" {
		t.Errorf("the newest change should come first, got %s", rows[0].Task)
	}
	by := sessionsByTask(rows)
	care, fix := by["recipe-care-ci-2"], by["recipe-fix-1"]
	for _, row := range []SessionRow{care, fix} {
		if row.Sandbox != "fix-repo-9" || row.Item != "pr" || row.Number != 12 || row.Repo != "test/repo" || row.Board != "myboard" {
			t.Errorf("row %s: %+v", row.Task, row)
		}
	}
	if care.Label != "Fix CI" || care.Session != "care" || care.State != "working" || !care.Busy {
		t.Errorf("care: %+v", care)
	}
	if fix.Label != "Fix" || fix.State != "done" || fix.Busy {
		t.Errorf("fix: %+v", fix)
	}
}

// A run that went into another's conversation is a run of that row.
func TestRunsIntoOneConversationAreOneRow(t *testing.T) {
	sb := sandboxCR("fix-repo-5", map[string]interface{}{factorycli.LabelIssue: "5"}, map[string]interface{}{
		"htmlURL":                    "https://github.com/test/repo/issues/5",
		factorycli.AnnotationPlanRun: `{"task":"recipe-plan-1","recipe":"plan","state":"Completed","startedAt":"2026-10-10T10:00:00Z","endedAt":"2026-10-10T10:30:00Z"}`,
		"sandbox.gemini.google.com/summarize-run": `{"task":"recipe-summarize-2","session":"recipe-plan-1","recipe":"summarize","state":"Completed","startedAt":"2026-10-11T10:00:00Z","endedAt":"2026-10-11T10:05:00Z"}`,
	}, 0)
	rows := listSessions(t, sessionsTestServer(t, nil, []*unstructured.Unstructured{sb}))
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Task != "recipe-plan-1" || len(row.Runs) != 2 || row.Runs[0].Recipe != "summarize" || row.Label != "Summarize" {
		t.Errorf("row: %+v", row)
	}
	if row.Item != "issue" || row.Number != 5 || row.Pod != "paused" || row.State != "paused" {
		t.Errorf("row: %+v", row)
	}
}

// A draft nobody has acted on needs the member, paused sandbox or not; an
// engine stopped on a permission does too.
func TestSessionsNeedingTheMember(t *testing.T) {
	plan := sandboxCR("fix-repo-7", map[string]interface{}{factorycli.LabelIssue: "7"}, map[string]interface{}{
		"htmlURL":                    "https://github.com/test/repo/issues/7",
		factorycli.AnnotationPlanRun: `{"task":"recipe-plan-1","recipe":"plan","state":"Completed","kind":"Plan","startedAt":"2026-10-10T10:00:00Z","endedAt":"2026-10-10T10:30:00Z"}`,
		factorycli.OutputAnnotation(factorycli.AnnotationPlanRun): "kind: Plan\n",
	}, 0)
	asking := researchSandboxCR("alice", researchSession, researchRepo, false)
	daemon := &fakeSessions{live: map[string]acpd.Session{researchTaskOf(researchSession): {ID: researchTaskOf(researchSession), Busy: true, Waiting: true}}}
	r := sessionsTestServer(t, daemon, []*unstructured.Unstructured{plan, asking}, researchPod("alice", asking.GetName(), "10.0.0.2", corev1.PodRunning))

	by := sessionsByTask(listSessions(t, r))
	if got := by["recipe-plan-1"]; got.State != "needs-you" {
		t.Errorf("a ready plan: %+v", got)
	}
	got := by[researchTaskOf(researchSession)]
	if got.State != "needs-you" || !got.Waiting || got.Label != "Research" || got.Research != researchSession || got.Item != "" {
		t.Errorf("a research session asking: %+v", got)
	}
}

// Only the research conversation in a research sandbox carries its id: it
// names the row, so a second session there must not share it.
func TestOnlyTheResearchSessionCarriesItsID(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	a := sb.GetAnnotations()
	a["sandbox.gemini.google.com/summarize-run"] = `{"task":"recipe-summarize-1","recipe":"summarize","state":"Completed","startedAt":"2026-10-11T10:00:00Z"}`
	sb.SetAnnotations(a)
	by := sessionsByTask(listSessions(t, sessionsTestServer(t, nil, []*unstructured.Unstructured{sb})))
	if got := by[researchTaskOf(researchSession)].Research; got != researchSession {
		t.Errorf("the research row's id = %q", got)
	}
	if got := by["recipe-summarize-1"].Research; got != "" {
		t.Errorf("another session in the sandbox carries the research id %q", got)
	}
}

// A running engine nobody talks to is idle; a sandbox that recorded no
// runs has no conversation to list.
func TestIdleSessionsAndSandboxesWithoutRuns(t *testing.T) {
	idle := researchSandboxCR("alice", researchSession, researchRepo, false)
	bare := sandboxCR("runbook-x", nil, map[string]interface{}{"htmlURL": "https://github.com/test/repo"}, 1)
	daemon := &fakeSessions{live: map[string]acpd.Session{researchTaskOf(researchSession): {ID: researchTaskOf(researchSession)}}}
	rows := listSessions(t, sessionsTestServer(t, daemon, []*unstructured.Unstructured{idle, bare},
		researchPod("alice", idle.GetName(), "10.0.0.3", corev1.PodRunning)))
	if len(rows) != 1 || rows[0].State != "idle" || !rows[0].Live {
		t.Errorf("rows: %+v", rows)
	}
}
