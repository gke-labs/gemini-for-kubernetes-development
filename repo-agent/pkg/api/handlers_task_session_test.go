package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
)

const (
	planTask      = "recipe-plan-20261004-120000-ab12"
	issueSandbox  = "fix-granule-42"
	taskSessionAt = "/api/task-sessions/" + issueSandbox + "/" + planTask
)

// issueSandboxCR is an issue's sandbox, with a plan run recorded on it.
func issueSandboxCR(namespace string, replicas int64) *unstructured.Unstructured {
	run, _ := json.Marshal(factorycli.RecordedRun{Name: "plan/b/42/1", Task: planTask, StartedAt: time.Unix(1_000_000, 0)})
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      issueSandbox,
			"namespace": namespace,
			"annotations": map[string]interface{}{
				"repo":                           "granule",
				"htmlURL":                        "https://github.com/o/granule/issues/42",
				"board.gemini.google.com/engine": "antigravity",
				factorycli.AnnotationPlanRun:     string(run),
			},
		},
		"spec": map[string]interface{}{"replicas": replicas},
	}}
}

// taskSessionTestServer is researchTestServer, whose task session seam reaches acp when hosts is true and finds no sessions when not.
func taskSessionTestServer(t *testing.T, acp *fakeACPD, hosts bool, sandboxes []*unstructured.Unstructured, pods ...*corev1.Pod) *gin.Engine {
	t.Helper()
	r, _ := taskSessionTestServerDyn(t, acp, hosts, sandboxes, pods...)
	return r
}

// taskSessionTestServerDyn is taskSessionTestServer with its cluster, for
// a test that files Requests.
func taskSessionTestServerDyn(t *testing.T, acp *fakeACPD, hosts bool, sandboxes []*unstructured.Unstructured, pods ...*corev1.Pod) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	var objs []runtime.Object
	for _, p := range pods {
		objs = append(objs, p)
	}
	if acp == nil {
		acp = &fakeACPD{}
	}
	r, dyn := researchTestServer(t, acp, sandboxes, objs...)
	srv := httptest.NewServer(acp.handler())
	t.Cleanup(srv.Close)
	prev := taskSessionClientForPod
	taskSessionClientForPod = func(context.Context, *podacpd.Dialer, *corev1.Pod) (*acpd.Client, bool) {
		if !hosts {
			return nil, false
		}
		return acpd.New(srv.URL), true
	}
	t.Cleanup(func() { taskSessionClientForPod = prev })
	return r, dyn
}

func TestContinuingATaskSessionLoadsItWhereTheTaskRan(t *testing.T) {
	acp := &fakeACPD{}
	r := taskSessionTestServer(t, acp, true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, taskSessionAt+"/prompt", `{"text":"why this approach?"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var req acpd.CreateSessionRequest
	if err := json.Unmarshal([]byte(acp.createBody), &req); err != nil {
		t.Fatalf("create body %q: %v", acp.createBody, err)
	}
	if req.ID != planTask || req.Task != planTask {
		t.Errorf("created %+v, want the task's session", req)
	}
	if req.Engine != "antigravity" || req.CWD != "/workspaces/granule" {
		t.Errorf("created on %q in %q, want the engine and checkout the task ran with", req.Engine, req.CWD)
	}
	if acp.createKey != "engine-key-value" {
		t.Errorf("%s = %q, want the member's key", acpd.APIKeyHeader, acp.createKey)
	}
	if !acp.sawCall("POST /sessions/" + planTask + "/prompt") {
		t.Errorf("the prompt did not reach the task's session; calls: %v", acp.calls)
	}
}

func TestATaskSessionStatusReportsHeldAndLoadedWithoutStartingIt(t *testing.T) {
	acp := &fakeACPD{}
	r := taskSessionTestServer(t, acp, true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	w := doJSON(t, r, http.MethodGet, taskSessionAt, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"live":false`) {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if acp.sawCall("POST /sessions") {
		t.Error("a status call started an engine")
	}

	acp.sessionExists = true
	acp.session = &acpd.Session{ID: planTask, Task: planTask, Held: true, Loaded: true, Busy: true}
	w = doJSON(t, r, http.MethodGet, taskSessionAt, "")
	for _, want := range []string{`"held":true`, `"loaded":true`, `"busy":true`, `"htmlUrl":"https://github.com/o/granule/issues/42"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("status lacks %s: %s", want, w.Body.String())
		}
	}
}

func TestATaskSessionOnAnOlderImageIsLegacy(t *testing.T) {
	r := taskSessionTestServer(t, nil, false, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	w := doJSON(t, r, http.MethodGet, taskSessionAt, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"legacy":true`) {
		t.Errorf("status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestATaskSessionIsLookedUpInTheMembersNamespace(t *testing.T) {
	r := taskSessionTestServer(t, nil, true, []*unstructured.Unstructured{issueSandboxCR("bob", 1)},
		researchPod("bob", issueSandbox, "10.1.2.3", corev1.PodRunning))
	if w := doJSON(t, r, http.MethodGet, taskSessionAt, ""); w.Code != http.StatusNotFound {
		t.Errorf("another member's sandbox: %d %s", w.Code, w.Body.String())
	}
}

func TestATaskSessionNeedsAnUpSandbox(t *testing.T) {
	r := taskSessionTestServer(t, nil, true, []*unstructured.Unstructured{issueSandboxCR("alice", 0)})
	if w := doJSON(t, r, http.MethodGet, taskSessionAt, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"paused":true`) {
		t.Errorf("paused: %d %s", w.Code, w.Body.String())
	}
	r = taskSessionTestServer(t, nil, true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)})
	if w := doJSON(t, r, http.MethodGet, taskSessionAt, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"starting":true`) {
		t.Errorf("no pod: %d %s", w.Code, w.Body.String())
	}
}

func TestATaskSessionRejectsMalformedNames(t *testing.T) {
	r := taskSessionTestServer(t, nil, true, nil)
	for _, path := range []string{"/api/task-sessions/" + issueSandbox + "/..", "/api/task-sessions/Bad_Name/" + planTask} {
		if w := doJSON(t, r, http.MethodGet, path, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// A running task's session is the task's to start; the member's prompt is
// refused, and the stream closes without an error for the UI to retry.
func TestARunningTasksSessionIsNotTheMembersToStart(t *testing.T) {
	acp := &fakeACPD{createStatus: http.StatusConflict}
	r := taskSessionTestServer(t, acp, true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	if w := doJSON(t, r, http.MethodPost, taskSessionAt+"/prompt", `{"text":"hi"}`); w.Code != http.StatusConflict {
		t.Errorf("prompt: %d %s", w.Code, w.Body.String())
	}

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/task-session-events/"+issueSandbox+"/"+planTask, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame researchFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != researchFrameClosed || frame.Error != "" {
		t.Errorf("frame = %+v, want a quiet close", frame)
	}
}

func TestTheBoardNamesTheRecordedRunsSession(t *testing.T) {
	sb := issueSandboxCR("alice", 1)
	got := taskSession(sb, factorycli.AnnotationPlanRun)
	if got == nil || got.Sandbox != issueSandbox || got.Task != planTask {
		t.Errorf("plan session = %+v", got)
	}
	if got := taskSession(sb, factorycli.AnnotationTriageRun); got != nil {
		t.Errorf("triage session with no triage run = %+v", got)
	}
	if got := taskSession(nil, factorycli.AnnotationPlanRun); got != nil {
		t.Errorf("no sandbox, yet %+v", got)
	}

	// After a revise, the recorded run is the revise's; the session is
	// still the plan's, the one conversation.
	run, _ := json.Marshal(factorycli.RecordedRun{Name: "revise/b/42/plan/2", Task: "recipe-plan-2", Session: planTask, StartedAt: time.Unix(1_000_100, 0)})
	a := sb.GetAnnotations()
	a[factorycli.AnnotationPlanRun] = string(run)
	sb.SetAnnotations(a)
	if got := taskSession(sb, factorycli.AnnotationPlanRun); got == nil || got.Task != planTask {
		t.Errorf("plan session after a revise = %+v", got)
	}
}

// The session a plan draft came from offers its revises, with where to
// file them; another session, or an approved plan, offers none.
func TestATaskSessionOffersItsDraftsRevises(t *testing.T) {
	withDraft := func(approved bool) *unstructured.Unstructured {
		sb := issueSandboxCR("alice", 1)
		a := sb.GetAnnotations()
		a[annoPlanDraft] = "## Summary\nA plan."
		a[annoBoard] = "myboard"
		a[factorycli.AnnotationPlanOutput] = "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
			"source:\n  task: recipe-plan-2\n  session: " + planTask + "\n" +
			"actions:\n  - verb: comment\n  - verb: revise\n    revise: plan\n    label: Use as plan\n"
		if approved {
			a[annoPlanApproved] = "2026-10-04T00:00:00Z"
		}
		sb.SetAnnotations(a)
		return sb
	}
	r := taskSessionTestServer(t, nil, true, []*unstructured.Unstructured{withDraft(false)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	w := doJSON(t, r, http.MethodGet, taskSessionAt, "")
	for _, want := range []string{`"revises":[{"verb":"revise","revise":"plan","label":"Use as plan","enabled":true}]`, `"board":"myboard"`, `"number":42`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("status lacks %s: %s", want, w.Body.String())
		}
	}
	if w := doJSON(t, r, http.MethodGet, "/api/task-sessions/"+issueSandbox+"/recipe-triage-1", ""); strings.Contains(w.Body.String(), `"revises"`) {
		t.Errorf("another session offers the plan's revises: %s", w.Body.String())
	}

	r = taskSessionTestServer(t, nil, true, []*unstructured.Unstructured{withDraft(true)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	if w := doJSON(t, r, http.MethodGet, taskSessionAt, ""); strings.Contains(w.Body.String(), `"revises"`) {
		t.Errorf("an approved plan offers revises: %s", w.Body.String())
	}
}

// The session is where a revise is clicked, so it is where it is followed:
// standing, the button says it is revising; failed, it says why.
func TestATaskSessionFollowsItsRevise(t *testing.T) {
	sb := issueSandboxCR("alice", 1)
	a := sb.GetAnnotations()
	a[annoPlanDraft] = "## Summary\nA plan."
	a[annoBoard] = "myboard"
	a[factorycli.AnnotationPlanOutput] = "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
		"source:\n  task: " + planTask + "\n" +
		"actions:\n  - verb: comment\n  - verb: revise\n    revise: plan\n    label: Use as plan\n"
	sb.SetAnnotations(a)
	r, dyn := taskSessionTestServerDyn(t, nil, true, []*unstructured.Unstructured{sb},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	revises := func() []models.WorkAction {
		t.Helper()
		var body struct {
			Revises []models.WorkAction `json:"revises"`
		}
		w := doJSON(t, r, http.MethodGet, taskSessionAt, "")
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Revises) != 1 {
			t.Fatalf("revises in %s (%v)", w.Body.String(), err)
		}
		return body.Revises
	}
	if got := revises()[0]; !got.Enabled || got.Reason != "" || got.Error != "" {
		t.Errorf("with no revise filed: %+v", got)
	}

	ctx := context.Background()
	other := requestCR(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRevise, Number: 7, Revise: "plan"})
	if _, err := dyn.Resource(requestGVR).Namespace("alice").Create(ctx, other, v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := revises()[0]; !got.Enabled {
		t.Errorf("another issue's revise holds this one: %+v", got)
	}

	click := requestCR(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRevise, Number: 42, Revise: "plan"})
	if _, err := dyn.Resource(requestGVR).Namespace("alice").Create(ctx, click, v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := revises()[0]; got.Enabled || got.Reason != "revising" {
		t.Errorf("while the revise stands: %+v", got)
	}

	click.Object["status"] = map[string]interface{}{"phase": boardv1alpha1.RequestFailed, "message": "the session is busy"}
	if _, err := dyn.Resource(requestGVR).Namespace("alice").Update(ctx, click, v1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := revises()[0]; !got.Enabled || got.Error != "the session is busy" {
		t.Errorf("after the revise failed: %+v", got)
	}
}
