/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// A real UUID: the short id is a digest of these exact bytes, so the
// label the handler searches on has to be derived, never typed.
const researchSession = "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00"

const researchRepo = "kubernetes"

// fakeModes is what a mode-aware engine advertises, trimmed to the two
// ends of the range.
var fakeModes = []acpd.SessionMode{
	{ID: acpd.ModeDefault, Name: "Default", Description: "Prompts for approval"},
	{ID: acpd.ModeYolo, Name: "YOLO", Description: "Auto-approves all tools"},
}

// fakeACPD stands in for the conversation server in the sandbox. It
// records what arrived so the tests can assert on the wire, not on the
// handler's intentions.
type fakeACPD struct {
	mu sync.Mutex
	// calls is "METHOD /path", in order.
	calls []string
	// headers is the key header seen on the last session create.
	createKey  string
	createBody string
	promptBody string
	modeBody   string
	// mode is the approval mode the session is in, set by the create and
	// by POST /mode — so a test can see what actually stuck rather than
	// only what was asked for.
	mode string
	// modeError is acpd's account of why mode is not the one the session
	// was created with, which it reports instead of refusing to start.
	modeError string

	// sessionExists controls whether GET /sessions/<id> answers or 404s,
	// which is the difference between reusing a session and creating one.
	sessionExists bool
	// live, when non-nil, answers GET by session id and is authoritative:
	// an id that is not in it 404s. For the list, where several sessions
	// are asked about in one request and the whole point is that they can
	// be in different states. nil leaves the single-session behaviour.
	live map[string]acpd.Session
	// promptStatus overrides the prompt reply, for the busy case.
	promptStatus int
	// transcript is what the event stream serves.
	transcript string
}

func (f *fakeACPD) record(method, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+path)
}

func (f *fakeACPD) sawCall(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == want {
			return true
		}
	}
	return false
}

func (f *fakeACPD) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id"))
		f.mu.Lock()
		exists := f.sessionExists
		mode, modeError := f.mode, f.modeError
		live, byID := f.live, f.live[r.PathValue("id")]
		f.mu.Unlock()
		if live != nil {
			if byID.ID == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no such session"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(byID)
			return
		}
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such session"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(acpd.Session{
			ID: researchSession, Engine: acpd.EngineGemini, CWD: "/workspaces/" + researchRepo, Offset: 128,
			Mode: mode, AvailableModes: fakeModes, ModeError: modeError,
		})
	})
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions")
		body, _ := io.ReadAll(r.Body)
		var req acpd.CreateSessionRequest
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.createKey = r.Header.Get(acpd.APIKeyHeader)
		f.createBody = string(body)
		f.sessionExists = true
		f.mode = req.Mode
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(acpd.Session{
			ID: researchSession, Engine: acpd.EngineGemini, Mode: req.Mode, AvailableModes: fakeModes,
		})
	})
	mux.HandleFunc("POST /sessions/{id}/mode", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id")+"/mode")
		f.mu.Lock()
		exists := f.sessionExists
		f.mu.Unlock()
		// The real acpd has no session to set a mode on before one is
		// created, and answers 404. The fake has to agree, or the test
		// that this route never creates one proves nothing.
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such session"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.modeBody = string(body)
		f.mode = req.Mode
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(acpd.Session{
			ID: researchSession, Engine: acpd.EngineGemini, Mode: req.Mode, AvailableModes: fakeModes,
		})
	})
	mux.HandleFunc("POST /sessions/{id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id")+"/prompt")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.promptBody = string(body)
		status := f.promptStatus
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"a turn is already in flight"}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"offset":512}`))
	})
	mux.HandleFunc("POST /sessions/{id}/permission", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id")+"/permission")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /sessions/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id")+"/cancel")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, "/sessions/"+r.PathValue("id")+"/events?"+r.URL.RawQuery)
		f.mu.Lock()
		body := f.transcript
		f.mu.Unlock()
		_, _ = w.Write([]byte(body))
	})
	return mux
}

func researchSandboxCR(namespace, sessionID, repo string, paused bool) *unstructured.Unstructured {
	name := factorycli.ResearchSandboxName(repo, sessionID)
	spec := map[string]interface{}{"replicas": int64(1)}
	if paused {
		spec["replicas"] = int64(0)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": "2026-09-26T10:00:00Z",
			"labels": map[string]interface{}{
				researchTypeLabel:    researchType,
				researchSessionLabel: factorycli.ResearchShortID(sessionID),
			},
			"annotations": map[string]interface{}{
				researchSessionIDAnnotation: sessionID,
				"repo":                      repo,
				"htmlURL":                   "https://github.com/kubernetes/" + repo,
			},
		},
		"spec": spec,
	}}
}

func researchPod(namespace, sandboxName, ip string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name:      sandboxName + "-0",
			Namespace: namespace,
			Labels:    map[string]string{"sandbox": sandboxName},
		},
		Status: corev1.PodStatus{Phase: phase, PodIP: ip},
	}
}

// researchTestServer wires the handlers to fakes and to a stand-in acpd.
// Pass a nil fake to leave acpd unreachable.
func researchTestServer(t *testing.T, acp *fakeACPD, sandboxes []*unstructured.Unstructured, kubeObjs ...runtime.Object) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrSandbox:   "SandboxList",
		repoBoardGVR: "RepoBoardList",
	})
	for _, sb := range sandboxes {
		if _, err := dynamicClient.Resource(gvrSandbox).Namespace(sb.GetNamespace()).Create(context.Background(), sb, v1.CreateOptions{}); err != nil {
			t.Fatalf("seed sandbox %s: %v", sb.GetName(), err)
		}
	}
	// The member's engine credential, unless the caller supplied its own
	// factory-user secret — which is how the no-key case is set up.
	objs := kubeObjs
	if !hasFactoryUserSecret(kubeObjs) {
		objs = append([]runtime.Object{&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{Name: "factory-user", Namespace: "alice"},
			Data:       map[string][]byte{"GEMINI_API_KEY": []byte("engine-key-value")},
		}}, kubeObjs...)
	}
	clientset := kubernetesfake.NewClientset(objs...)

	manager := &k8s.Manager{Client: dynamicClient, Clientset: clientset}
	server := &Server{K8sManager: manager, Auth: &auth.Authenticator{K8sManager: manager}}

	// Always stood up, even for a test that passes no fake: the list now
	// asks every running pod what its engine is doing, so leaving the seam
	// pointed at the real dialer sends a unit test at whatever 10.0.0.9
	// happens to be and waits out the timeout when nothing answers. An
	// empty fake 404s every session, which is the truthful stand-in — a
	// pod that is up with no engine in it.
	if acp == nil {
		acp = &fakeACPD{}
	}
	srv := httptest.NewServer(acp.handler())
	t.Cleanup(srv.Close)
	prev := acpdClientForPodIP
	acpdClientForPodIP = func(string) *acpd.Client { return acpd.New(srv.URL) }
	t.Cleanup(func() { acpdClientForPodIP = prev })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "alice")
		c.Next()
	})
	r.GET("/api/research", server.getResearchSessions)
	r.GET("/api/research/:session", server.getResearchSession)
	r.PATCH("/api/research/:session", server.renameResearchSession)
	r.DELETE("/api/research/:session", server.deleteResearchSession)
	r.POST("/api/research/:session/prompt", server.promptResearchSession)
	r.POST("/api/research/:session/permission", server.resolveResearchPermission)
	r.POST("/api/research/:session/cancel", server.cancelResearchSession)
	r.POST("/api/research/:session/mode", server.setResearchSessionMode)
	r.GET("/api/research-events/:session", server.streamResearchEvents)
	return r, dynamicClient
}

func hasFactoryUserSecret(objs []runtime.Object) bool {
	for _, o := range objs {
		if secret, ok := o.(*corev1.Secret); ok && secret.Name == "factory-user" {
			return true
		}
	}
	return false
}

func doJSON(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The list has to render before anything is reachable — a member whose
// sandbox is still pulling an image still needs to see the row.
func TestResearchSessionListDoesNotNeedAReachablePod(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{sb})

	w := doJSON(t, r, http.MethodGet, "/api/research", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Sessions []researchSandboxView `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1: %s", len(got.Sessions), w.Body.String())
	}
	if got.Sessions[0].SessionID != researchSession {
		t.Errorf("sessionId = %q", got.Sessions[0].SessionID)
	}
	if got.Sessions[0].Repo != researchRepo {
		t.Errorf("repo = %q", got.Sessions[0].Repo)
	}
}

// A sandbox object exists minutes before its pod does. Reporting that
// as up sends people into a conversation that cannot answer, so the row
// says starting until there is something to dial.
func TestResearchSessionListSaysStartingUntilThePodRuns(t *testing.T) {
	booting := researchSandboxCR("alice", researchSession, researchRepo, false)
	other := "3c2f9a71-0000-4000-8000-000000000002"
	up := researchSandboxCR("alice", other, researchRepo, false)
	sleeping := researchSandboxCR("alice", "3c2f9a71-0000-4000-8000-000000000003", researchRepo, true)

	r, _ := researchTestServer(t, nil,
		[]*unstructured.Unstructured{booting, up, sleeping},
		researchPod("alice", up.GetName(), "10.0.0.9", corev1.PodRunning),
		// Pending, so it does not count: an IP-less pod cannot be dialed.
		researchPod("alice", booting.GetName(), "", corev1.PodPending))

	w := doJSON(t, r, http.MethodGet, "/api/research", "")
	var got struct {
		Sessions []researchSandboxView `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	starting := map[string]bool{}
	for _, s := range got.Sessions {
		starting[s.SessionID] = s.Starting
	}
	if !starting[researchSession] {
		t.Errorf("a sandbox with no running pod should be starting: %s", w.Body.String())
	}
	if starting[other] {
		t.Errorf("a sandbox with a running pod should not be starting: %s", w.Body.String())
	}
	// Paused is its own state and its own answer — resuming is a click,
	// not a wait, and calling it starting would promise it is coming back.
	if starting["3c2f9a71-0000-4000-8000-000000000003"] {
		t.Errorf("a paused sandbox should not be starting: %s", w.Body.String())
	}
}

// The rail's whole job is telling you which conversation to open. A list
// that says "up" for all of them says the pods are running, which is not
// a question anyone has — so each row carries what its engine is actually
// doing, and the three states have to be told apart in one request.
func TestResearchListReportsWhatEachEngineIsDoing(t *testing.T) {
	const (
		thinking = "3c2f9a71-0000-4000-8000-00000000000a"
		asking   = "3c2f9a71-0000-4000-8000-00000000000b"
	)
	idle := researchSandboxCR("alice", researchSession, researchRepo, false)
	working := researchSandboxCR("alice", thinking, researchRepo, false)
	blocked := researchSandboxCR("alice", asking, researchRepo, false)

	acp := &fakeACPD{live: map[string]acpd.Session{
		researchSession: {ID: researchSession},
		thinking:        {ID: thinking, Busy: true},
		// Waiting comes with Busy, because it is a turn in flight that has
		// stopped — not a third thing instead of being busy.
		asking: {ID: asking, Busy: true, Waiting: true},
	}}
	r, _ := researchTestServer(t, acp,
		[]*unstructured.Unstructured{idle, working, blocked},
		researchPod("alice", idle.GetName(), "10.0.0.1", corev1.PodRunning),
		researchPod("alice", working.GetName(), "10.0.0.2", corev1.PodRunning),
		researchPod("alice", blocked.GetName(), "10.0.0.3", corev1.PodRunning))

	byID := map[string]researchSandboxView{}
	for _, s := range listResearch(t, r) {
		byID[s.SessionID] = s
	}
	if len(byID) != 3 {
		t.Fatalf("got %d sessions, want 3", len(byID))
	}

	for id, view := range byID {
		if !view.Live {
			t.Errorf("session %s has an engine but the row does not say live", id)
		}
		if view.Unreachable != "" {
			t.Errorf("session %s reported unreachable: %s", id, view.Unreachable)
		}
	}
	if byID[researchSession].Busy || byID[researchSession].Waiting {
		t.Error("a session sitting idle was reported as doing something")
	}
	if !byID[thinking].Busy {
		t.Error("a session with a turn in flight was not reported busy")
	}
	if byID[thinking].Waiting {
		t.Error("a session that is merely thinking was reported as waiting on a human")
	}
	if !byID[asking].Waiting {
		t.Error("a session blocked on a permission request was not reported waiting")
	}
	if !byID[asking].Busy {
		t.Error("a waiting session stopped being busy, which would hide it from a busy check")
	}
}

// A pod with no engine in it is the resting state of every session
// nobody has opened, and of every one whose acpd restarted. It is not an
// error and must not read as one.
func TestResearchListSeparatesNoEngineFromNoAnswer(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, &fakeACPD{sessionExists: false},
		[]*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.1", corev1.PodRunning))

	got := listResearch(t, r)
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].Live {
		t.Error("a pod whose acpd has no session for this id was reported live")
	}
	if got[0].Unreachable != "" {
		t.Errorf("acpd answered, so the row should not claim it is unreachable: %q", got[0].Unreachable)
	}
	if got[0].Starting {
		t.Error("the pod is running; only the engine is missing")
	}
}

// A pod that cannot be reached must not be reported as an idle session.
// The row still renders — the sandbox is the durable thing — but the
// live fields are absent and the reason is on the row, because "quiet"
// and "broken" are not the same answer.
func TestResearchListSaysWhenAPodCouldNotBeAsked(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, &fakeACPD{},
		[]*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.1", corev1.PodRunning))

	// Port 1 refuses immediately, which is what a pod with no acpd
	// listening does — and unlike an unroutable address it does not make
	// the test wait out the timeout to prove it.
	prev := acpdClientForPodIP
	acpdClientForPodIP = func(string) *acpd.Client { return acpd.New("http://127.0.0.1:1") }
	t.Cleanup(func() { acpdClientForPodIP = prev })

	got := listResearch(t, r)
	if len(got) != 1 {
		t.Fatalf("a pod that cannot be reached should not cost the row: got %d", len(got))
	}
	if got[0].Unreachable == "" {
		t.Error("the row did not say why its engine could not be asked")
	}
	if got[0].Live || got[0].Busy || got[0].Waiting {
		t.Error("a row we failed to reach reported live state anyway")
	}
	// The name and the sandbox still come from Kubernetes, which is the
	// half of the answer that never depended on the pod.
	if got[0].Sandbox != sb.GetName() {
		t.Errorf("sandbox = %q, want %q", got[0].Sandbox, sb.GetName())
	}
}

// Nothing without a running pod gets dialled. A paused session has no
// engine by definition and a requested one has no sandbox yet; asking
// about them would be one doomed connection per row, every ten seconds.
func TestResearchListAsksNothingOfSessionsWithNoPod(t *testing.T) {
	paused := researchSandboxCR("alice", researchSession, researchRepo, true)
	acp := &fakeACPD{live: map[string]acpd.Session{}}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{paused})

	got := listResearch(t, r)
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].Live || got[0].Unreachable != "" {
		t.Errorf("a paused row reported live state: live=%t unreachable=%q", got[0].Live, got[0].Unreachable)
	}
	if acp.sawCall("GET /sessions/" + researchSession) {
		t.Error("the list dialled a paused session's pod, which does not exist")
	}
}

// Every route here starts from a session id, so a research sandbox
// without one cannot be opened, prompted or followed. Listing it would
// put a row in the UI that does nothing when clicked.
func TestResearchSessionListSkipsSandboxesWithNoSessionID(t *testing.T) {
	good := researchSandboxCR("alice", researchSession, researchRepo, false)
	orphan := researchSandboxCR("alice", "3c2f9a71-0000-4000-8000-000000000001", "other-repo", false)
	annotations := orphan.GetAnnotations()
	delete(annotations, researchSessionIDAnnotation)
	orphan.SetAnnotations(annotations)

	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{good, orphan})
	w := doJSON(t, r, http.MethodGet, "/api/research", "")
	var got struct {
		Sessions []researchSandboxView `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != researchSession {
		t.Errorf("got %+v, want only the addressable session", got.Sessions)
	}
}

// Only the member's own namespace. The routes carry no namespace, so
// this scoping is the whole of the access control on a conversation.
func TestResearchSessionInAnotherNamespaceIsInvisible(t *testing.T) {
	sb := researchSandboxCR("bob", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, &fakeACPD{}, []*unstructured.Unstructured{sb},
		researchPod("bob", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodGet, "/api/research", ""); !strings.Contains(w.Body.String(), `"sessions":[]`) {
		t.Errorf("bob's session appeared in alice's list: %s", w.Body.String())
	}
	if w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for another namespace's session", w.Code)
	}
}

// The label is eight hex characters of a digest, so it narrows the
// search but cannot prove identity. Without the annotation check, a
// colliding short id would hand one member another's conversation.
func TestResearchShortIDCollisionIsRejected(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	// Same label, different session: exactly what a digest collision
	// looks like from the handler's side.
	annotations := sb.GetAnnotations()
	annotations[researchSessionIDAnnotation] = "a-different-session"
	sb.SetAnnotations(annotations)

	r, _ := researchTestServer(t, &fakeACPD{}, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — the label matched but the session did not", w.Code)
	}
}

// 409 rather than 404 or 500: the session exists and will come up, so
// the UI should keep waiting rather than declare it gone.
//
// The last two cases are the ones worth having. A pod keeps its IP after
// it stops, and a terminating pod keeps it until it is gone — so an
// address is not evidence that anything is listening on it, and dialling
// one of those looks like acpd being broken rather than the sandbox
// being down.
func TestResearchSessionStillStartingIsConflict(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	terminating := researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning)
	now := v1.Now()
	terminating.DeletionTimestamp = &now
	terminating.Finalizers = []string{"test/hold"}
	cases := map[string]*corev1.Pod{
		"no pod at all":      nil,
		"pod is pending":     researchPod("alice", sb.GetName(), "", corev1.PodPending),
		"no ip yet":          researchPod("alice", sb.GetName(), "", corev1.PodRunning),
		"pod has finished":   researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodFailed),
		"pod is terminating": terminating,
	}
	for name, pod := range cases {
		t.Run(name, func(t *testing.T) {
			var objs []runtime.Object
			if pod != nil {
				objs = append(objs, pod)
			}
			r, _ := researchTestServer(t, &fakeACPD{}, []*unstructured.Unstructured{sb}, objs...)
			w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestResearchPausedSessionIsConflict(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, true)
	r, _ := researchTestServer(t, &fakeACPD{}, []*unstructured.Unstructured{sb})

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"paused":true`) {
		t.Errorf("paused not reported: %s", w.Body.String())
	}
}

// Status is polled. If it spawned an engine, opening the list would
// start every conversation in it.
func TestResearchStatusDoesNotCreateASession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"live":false`) {
		t.Errorf("want live:false for a sandbox with no engine: %s", w.Body.String())
	}
	// The checkout is in the status so the UI can say what the
	// conversation is about before an engine exists to ask.
	if !strings.Contains(w.Body.String(), `"cwd":"/workspaces/`+researchRepo+`"`) {
		t.Errorf("cwd missing from the status: %s", w.Body.String())
	}
	// And where that repo is, so the conversation header can link the
	// name it has always shown. A conversation reached by a link rather
	// than through the list never sees the list's copy of this.
	if !strings.Contains(w.Body.String(), `"htmlUrl":"https://github.com/kubernetes/`+researchRepo+`"`) {
		t.Errorf("htmlUrl missing from the status: %s", w.Body.String())
	}
	if acp.sawCall("POST /sessions") {
		t.Error("a status call spawned an engine")
	}
}

// The engine credential must reach acpd in a header and never in a body:
// acpd writes conversation bodies to the on-disk transcript, and this
// API's own request logger prints them.
func TestResearchPromptCreatesSessionWithKeyInHeader(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"what does this repo do?"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"offset":512`) {
		t.Errorf("prompt offset not returned: %s", w.Body.String())
	}
	if !acp.sawCall("POST /sessions") {
		t.Fatalf("no session was created; calls: %v", acp.calls)
	}
	if acp.createKey != "engine-key-value" {
		t.Errorf("%s = %q, want the member's key", acpd.APIKeyHeader, acp.createKey)
	}
	if strings.Contains(acp.createBody, "engine-key-value") {
		t.Errorf("key leaked into the create body: %s", acp.createBody)
	}
	if !strings.Contains(acp.createBody, `"cwd":"/workspaces/`+researchRepo+`"`) {
		t.Errorf("session was not pointed at the checkout: %s", acp.createBody)
	}
	if !strings.Contains(acp.promptBody, "what does this repo do?") {
		t.Errorf("the prompt text did not reach the engine: %s", acp.promptBody)
	}
}

// An engine already running is reused. Creating a second one for a
// conversation that has one would start a duplicate agent against the
// same transcript.
func TestResearchPromptReusesALiveSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"hello"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if acp.sawCall("POST /sessions") {
		t.Errorf("created a second session for a live conversation; calls: %v", acp.calls)
	}
}

// acpd answers 409 when a turn is already running. The UI disables the
// composer on that, so it has to survive the proxy unchanged rather than
// becoming a generic 502.
func TestResearchPromptPassesBusyThrough(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true, promptStatus: http.StatusConflict}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"hello"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// Without a key there is no engine to spawn. Failing here, before the
// create, keeps the member's problem ("onboard first") distinguishable
// from a broken sandbox.
func TestResearchPromptWithoutAnEngineKeyFails(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning),
		// A factory-user secret with no engine key: an onboarded member
		// who never supplied one.
		&corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: "factory-user", Namespace: "alice"}})

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"hello"}`)
	if w.Code == http.StatusAccepted {
		t.Fatalf("prompt succeeded without an engine key: %s", w.Body.String())
	}
	if acp.sawCall("POST /sessions") {
		t.Errorf("tried to spawn an engine with no key; calls: %v", acp.calls)
	}
	if !strings.Contains(w.Body.String(), "GEMINI_API_KEY") {
		t.Errorf("the reason is not diagnosable: %s", w.Body.String())
	}
}

func TestResearchPermissionAndCancelReachTheEngine(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/permission", `{"requestId":"r1","optionId":"allow"}`); w.Code != http.StatusNoContent {
		t.Fatalf("permission status = %d: %s", w.Code, w.Body.String())
	}
	if !acp.sawCall("POST /sessions/" + researchSession + "/permission") {
		t.Errorf("permission did not reach acpd; calls: %v", acp.calls)
	}
	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/cancel", ""); w.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d: %s", w.Code, w.Body.String())
	}
	if !acp.sawCall("POST /sessions/" + researchSession + "/cancel") {
		t.Errorf("cancel did not reach acpd; calls: %v", acp.calls)
	}
}

// Answering a permission prompt must not be what starts the engine:
// there is nothing blocked on an engine that does not exist, and
// spawning one here would answer a question nobody asked.
func TestResearchPermissionDoesNotCreateASession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/permission", `{"requestId":"r1","optionId":"allow"}`)
	if acp.sawCall("POST /sessions") {
		t.Errorf("a permission answer spawned an engine; calls: %v", acp.calls)
	}
}

// Deleting is how a conversation ends, and a wedged one is the case that
// most needs it — so it must not go through the reachability checks that
// refuse a paused or still-booting sandbox.
func TestResearchDeleteWorksOnAPausedSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, true)
	r, dyn := researchTestServer(t, nil, []*unstructured.Unstructured{sb})

	if w := doJSON(t, r, http.MethodDelete, "/api/research/"+researchSession, ""); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	if _, err := dyn.Resource(gvrSandbox).Namespace("alice").Get(context.Background(), sb.GetName(), v1.GetOptions{}); err == nil {
		t.Error("the sandbox survived the delete")
	}
}

// The id names a label value and reaches acpd inside a URL path.
func TestResearchRejectsMalformedSessionIDs(t *testing.T) {
	r, _ := researchTestServer(t, nil, nil)
	for _, id := range []string{"-leading-dash", "has space", "semi;colon", "back`tick", strings.Repeat("a", 200)} {
		t.Run(id, func(t *testing.T) {
			// Escaped on the way in; gin hands the handler the decoded
			// value, which is the form the check has to cope with.
			w := doJSON(t, r, http.MethodGet, "/api/research/"+url.PathEscape(id), "")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

// The resumption contract: every event frame carries the offset AFTER
// it, so a browser that reconnects with the last one it stored sees each
// event exactly once.
func TestResearchEventStreamCarriesResumeOffsets(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	line1 := `{"seq":1,"kind":"user_prompt","data":{"text":"hi"}}` + "\n"
	line2 := `{"seq":2,"kind":"turn_end","data":{"stopReason":"end_turn"}}` + "\n"
	acp := &fakeACPD{sessionExists: true, transcript: line1 + line2}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/research-events/" + researchSession + "?offset=100"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var frames []researchFrame
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var frame researchFrame
		if err := conn.ReadJSON(&frame); err != nil {
			break
		}
		frames = append(frames, frame)
		if frame.Type == researchFrameClosed {
			break
		}
	}

	if len(frames) != 4 {
		t.Fatalf("got %d frames, want open + 2 events + closed: %+v", len(frames), frames)
	}
	if frames[0].Type != researchFrameOpen || frames[0].Session == nil {
		t.Errorf("first frame = %+v, want an open frame carrying the session", frames[0])
	}
	if frames[1].Offset != 100+int64(len(line1)) {
		t.Errorf("first event offset = %d, want %d — it must resume past the event, from the requested offset",
			frames[1].Offset, 100+int64(len(line1)))
	}
	if frames[2].Offset != 100+int64(len(line1)+len(line2)) {
		t.Errorf("second event offset = %d, want %d", frames[2].Offset, 100+int64(len(line1)+len(line2)))
	}
	if frames[2].Event == nil || frames[2].Event.Kind != acpd.KindTurnEnd {
		t.Errorf("third frame = %+v, want the turn_end event", frames[2])
	}
	if frames[3].Type != researchFrameClosed {
		t.Errorf("last frame = %+v, want closed", frames[3])
	}
	if !acp.sawCall("GET /sessions/" + researchSession + "/events?offset=100") {
		t.Errorf("the requested offset did not reach acpd; calls: %v", acp.calls)
	}
}

// Opening the stream is the deliberate act that starts the engine — it
// is what a member clicking into a conversation does, and it is also the
// recovery path after an acpd restart drops the session.
func TestResearchEventStreamCreatesTheSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false, transcript: ""}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/research-events/" + researchSession
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame researchFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read: %v", err)
	}
	if frame.Type != researchFrameOpen {
		t.Fatalf("first frame = %+v, want open", frame)
	}
	if !acp.sawCall("POST /sessions") {
		t.Errorf("the stream did not start an engine; calls: %v", acp.calls)
	}
}

// --- titles and pending rows ------------------------------------------

func researchBoardCR(requests map[string]string) *unstructured.Unstructured {
	raw, _ := json.Marshal(requests)
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "board.gemini.google.com/v1alpha1",
		"kind":       "RepoBoard",
		"metadata": map[string]interface{}{
			"name": "myboard", "namespace": "alice",
			"annotations": map[string]interface{}{annoBoardRequests: string(raw)},
		},
		"spec": map[string]interface{}{"repoURL": "https://github.com/kubernetes/" + researchRepo},
	}}
}

func listResearch(t *testing.T, r *gin.Engine) []researchSandboxView {
	t.Helper()
	w := doJSON(t, r, http.MethodGet, "/api/research", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Sessions []researchSandboxView `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Sessions
}

// The title is what the list is read by. It comes off the sandbox, so
// it survives everything except deleting the session.
func TestResearchListCarriesTheTitle(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.TitleAnnotation] = "overview"
	annotations[research.KickoffAnnotation] = research.Kickoff{Kind: research.KindOnboard}.Encode()
	sb.SetAnnotations(annotations)
	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{sb})

	sessions := listResearch(t, r)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "overview" {
		t.Errorf("title = %q, want overview", sessions[0].Title)
	}
	if !sessions[0].Opening {
		t.Error("a session whose opening turn is still owed must say so")
	}
}

// A click is minutes away from being a sandbox. Without a row for it
// the member sees nothing happen and clicks again — which is a second
// sandbox and a second engine.
func TestResearchListShowsRequestedSessions(t *testing.T) {
	pending := "9f1c2d3e-4a5b-4c6d-8e9f-0a1b2c3d4e5f"
	claim := research.Claim{
		Member:  "alice",
		At:      time.Now().UTC(),
		Kickoff: research.Kickoff{Kind: research.KindActivity, Since: "1 month"},
	}
	r, dyn := researchTestServer(t, nil, nil)
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(),
		researchBoardCR(map[string]string{"research-" + pending: claim.Encode()}), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	sessions := listResearch(t, r)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want the requested one: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.SessionID != pending || !got.Requested {
		t.Errorf("row = %+v, want the pending session marked requested", got)
	}
	if got.Title != "what happened · 1 month" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Repo != researchRepo {
		t.Errorf("repo = %q, want %q — taken from the board that holds the claim", got.Repo, researchRepo)
	}
}

// The controller trims a claim as soon as the sandbox exists, but the
// two states overlap for one reconcile. Showing both would double the
// row under the member's cursor.
func TestResearchListDoesNotDoubleAServedClaim(t *testing.T) {
	claim := research.Claim{Member: "alice", At: time.Now().UTC()}
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, dyn := researchTestServer(t, nil, []*unstructured.Unstructured{sb})
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(),
		researchBoardCR(map[string]string{"research-" + researchSession: claim.Encode()}), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	sessions := listResearch(t, r)
	if len(sessions) != 1 {
		t.Fatalf("got %d rows for one session: %+v", len(sessions), sessions)
	}
	if sessions[0].Requested {
		t.Error("the sandbox exists; the row must be the real one")
	}
}

// The sandbox object exists minutes before the controller stamps a
// title on it — `factory research start` creates it and then clones —
// and the claim is hidden as served for that whole window. Reading the
// row's name off the sandbox alone is what made a session flip from
// "what happened · 2 weeks" to "untitled" and back a minute later.
func TestAServedClaimStillNamesItsUntitledSandbox(t *testing.T) {
	claim := research.Claim{
		Member:  "alice",
		At:      time.Now().UTC(),
		Kickoff: research.Kickoff{Kind: research.KindActivity, Since: "2 weeks"},
	}
	// No title annotation: the controller has not run since the sandbox
	// appeared, which is the whole of the window under test.
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, dyn := researchTestServer(t, nil, []*unstructured.Unstructured{sb})
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(),
		researchBoardCR(map[string]string{"research-" + researchSession: claim.Encode()}), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	sessions := listResearch(t, r)
	if len(sessions) != 1 {
		t.Fatalf("got %d rows for one session: %+v", len(sessions), sessions)
	}
	if sessions[0].Requested {
		t.Error("the sandbox exists; the row must be the real one")
	}
	if sessions[0].Title != "what happened · 2 weeks" {
		t.Errorf("title = %q, want the claim's — the sandbox has none yet", sessions[0].Title)
	}
}

// ...and the sandbox wins once it has one. A rename writes the
// annotation and leaves the claim alone, so a claim that outlives the
// rename must not drag the old name back.
func TestASandboxTitleBeatsTheClaimItCameFrom(t *testing.T) {
	claim := research.Claim{
		Member:  "alice",
		At:      time.Now().UTC(),
		Kickoff: research.Kickoff{Kind: research.KindActivity, Since: "2 weeks"},
	}
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.TitleAnnotation] = "the retry loop"
	sb.SetAnnotations(annotations)
	r, dyn := researchTestServer(t, nil, []*unstructured.Unstructured{sb})
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(),
		researchBoardCR(map[string]string{"research-" + researchSession: claim.Encode()}), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	sessions := listResearch(t, r)
	if len(sessions) != 1 {
		t.Fatalf("got %d rows: %+v", len(sessions), sessions)
	}
	if sessions[0].Title != "the retry loop" {
		t.Errorf("title = %q, want the sandbox's", sessions[0].Title)
	}
}

// A claim filed by someone else, sitting on a board this member can
// read, is not this member's session.
func TestResearchListIgnoresAnotherMembersClaim(t *testing.T) {
	claim := research.Claim{Member: "bob", At: time.Now().UTC()}
	r, dyn := researchTestServer(t, nil, nil)
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(),
		researchBoardCR(map[string]string{"research-" + researchSession: claim.Encode()}), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if sessions := listResearch(t, r); len(sessions) != 0 {
		t.Errorf("got %+v, want nothing", sessions)
	}
}

// Renaming must work on a session that cannot be dialed: a paused one
// is exactly the session you want to label before you forget what it
// was for.
func TestResearchRenameWorksOnAPausedSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, true)
	r, dyn := researchTestServer(t, nil, []*unstructured.Unstructured{sb})

	w := doJSON(t, r, http.MethodPatch, "/api/research/"+researchSession, `{"title":"retry loop, where?"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	got, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), sb.GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if title := got.GetAnnotations()[research.TitleAnnotation]; title != "retry loop, where?" {
		t.Errorf("stored title = %q", title)
	}
}

func TestResearchRenameRejectsAnEmptyTitle(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{sb})
	w := doJSON(t, r, http.MethodPatch, "/api/research/"+researchSession, `{"title":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A conversation nobody named is named by what was asked of it — the
// alternative is a list of identical rows.
func TestResearchFirstPromptTitlesTheSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt",
		`{"text":"where does the retry loop live?\nand who calls it"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	got, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), sb.GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if title := got.GetAnnotations()[research.TitleAnnotation]; title != "where does the retry loop live?" {
		t.Errorf("title = %q, want the first line of the first prompt", title)
	}
}

// Only the first. A session that already has a name keeps it, whether
// that name came from a canned opening or from the member.
func TestResearchLaterPromptsDoNotRetitle(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.TitleAnnotation] = "overview"
	sb.SetAnnotations(annotations)
	acp := &fakeACPD{}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"and the backoff?"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	got, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), sb.GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if title := got.GetAnnotations()[research.TitleAnnotation]; title != "overview" {
		t.Errorf("title = %q, want the name it already had", title)
	}
}

// Research sessions are created auto-approving. The canned openings need
// shell tools — an activity digest runs git log — so a read-only mode
// would not spare them the prompt, and the controller fires them with no
// browser attached, where an unanswered prompt blocks the turn until
// acpd's permission timeout and is then cancelled.
func TestResearchSessionIsCreatedAutoApproving(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"hello"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(acp.createBody, `"mode":"`+acpd.ResearchMode+`"`) {
		t.Errorf("session was not created in the research mode: %s", acp.createBody)
	}
}

// The mode is on the status so the header can render the switcher on the
// first paint, without a second round trip to find out what it is on.
func TestResearchStatusReportsTheMode(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true, mode: acpd.ModeYolo}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"mode":"yolo"`) {
		t.Errorf("mode missing from the status: %s", w.Body.String())
	}
	// Without the set there is nothing to switch to, so the UI would
	// have to hardcode one engine's vocabulary to offer the control.
	if !strings.Contains(w.Body.String(), `"availableModes"`) {
		t.Errorf("availableModes missing from the status: %s", w.Body.String())
	}
}

// The status carries waiting as well as the list does, so the two cannot
// disagree about one session. It matters most on the first paint: the
// conversation learns this from the event stream, which only tells it
// what happens after it attached — a session that stopped to ask
// something before anybody opened it has no event left to send.
func TestResearchStatusReportsAWaitingSession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{live: map[string]acpd.Session{
		researchSession: {ID: researchSession, Busy: true, Waiting: true},
	}}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"waiting":true`) {
		t.Errorf("a session blocked on a permission request did not say so: %s", w.Body.String())
	}
}

// A session that did not get the mode it asked for still runs, and the
// only place the reason survives is on the session: the create that saw
// the refusal was over before anybody attached to this conversation.
func TestResearchStatusCarriesWhyTheModeDidNotStick(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{
		sessionExists: true,
		mode:          acpd.ModeDefault,
		modeError:     `session/set_mode "yolo": Cannot enable privileged approval modes in an untrusted folder.`,
	}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "untrusted folder") {
		t.Errorf("modeError missing from the status: %s", w.Body.String())
	}
}

func TestResearchModeCanBeSwitchedMidConversation(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true, mode: acpd.ModeYolo}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/mode", `{"mode":"default"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(acp.modeBody, `"mode":"default"`) {
		t.Errorf("the mode did not reach acpd: %s", acp.modeBody)
	}
	if !strings.Contains(w.Body.String(), `"mode":"default"`) {
		t.Errorf("the reply does not report the new mode: %s", w.Body.String())
	}
}

func TestResearchModeRejectsAnEmptyBody(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true, mode: acpd.ModeYolo}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/mode", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if acp.sawCall("POST /sessions/" + researchSession + "/mode") {
		t.Error("an empty mode reached acpd")
	}
}

// Like the permission route, this one must not spawn an engine: there is
// nothing to set a mode on until a session exists, and a create here
// would hand the member a conversation as a side effect of adjusting one.
func TestSettingTheModeDoesNotCreateASession(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: false}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/mode", `{"mode":"default"}`); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a session with no engine", w.Code)
	}
	if acp.sawCall("POST /sessions") {
		t.Errorf("setting the mode spawned an engine; calls: %v", acp.calls)
	}
}
