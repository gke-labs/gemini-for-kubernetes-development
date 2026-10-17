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
	"github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
)

// overseerSandbox is a sandbox in overseer-kcc an overseer's fix ran in as
// coder-bot, its Change stored, as the run left it; paused, it is idle.
func overseerSandbox(name string, paused bool) *unstructured.Unstructured {
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "fix-42-head-1", Task: "recipe-fix-1", StartedAt: time.Unix(1_000_000, 0), Kind: "Change", State: "Completed",
	})
	replicas := int64(1)
	if paused {
		replicas = 0
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "overseer-kcc",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				labelFactoryUser:                    "coder-bot",
				factorycli.LabelIssue:               "42",
			},
			"annotations": map[string]interface{}{
				"repo": "repo", "htmlURL": "https://github.com/test/repo/issues/42",
				"sandbox.gemini.google.com/last-task-type":  "fix",
				"sandbox.gemini.google.com/last-task-state": "Completed",
				factorycli.AnnotationFixRun:                 string(run),
				factorycli.OutputAnnotation(factorycli.AnnotationFixRun): "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\n" +
					"source:\n  task: recipe-fix-1\nspec:\n  title: Fix it\n",
			},
		},
		"spec": map[string]interface{}{"replicas": replicas},
	}}
}

// overseerSessionServer serves the overseer-scoped routes as server.go
// mounts them, behind the admin check, to the user in the X-User header.
func overseerSessionServer(t *testing.T, daemon *fakeSessions) *gin.Engine {
	t.Helper()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrSandbox: "SandboxList", repoBoardGVR: "RepoBoardList", requestGVR: "RequestList", k8s.OverseerGVR: "OverseerList",
	})
	// kcc watches test/repo; dora is on its allowlist.
	kcc := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "overseer.gemini.google.com/v1alpha1", "kind": "Overseer",
		"metadata": map[string]interface{}{"name": "kcc"},
		"spec":     map[string]interface{}{"repoURL": "https://github.com/test/repo", "allowlistedUsers": []interface{}{"Dora"}},
	}}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "overseer.gemini.google.com/v1alpha1", "kind": "Overseer",
		"metadata": map[string]interface{}{"name": "other"},
		"spec":     map[string]interface{}{"repoURL": "https://github.com/test/elsewhere"},
	}}
	for _, ov := range []*unstructured.Unstructured{kcc, other} {
		if _, err := dyn.Resource(k8s.OverseerGVR).Create(context.Background(), ov, v1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// fix-repo-44: a care-ci run recorded, with no output stored by a board.
	care := overseerSandbox("fix-repo-44", false)
	careRun, _ := json.Marshal(factorycli.RecordedRun{Name: "investigate-44-a-1", Task: "recipe-care-ci-9", Recipe: "care-ci", StartedAt: time.Unix(1_000_100, 0), Kind: "Change", State: "Completed"})
	if err := unstructured.SetNestedField(care.Object, string(careRun), "metadata", "annotations", "sandbox.gemini.google.com/recipe-care-run"); err != nil {
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(care.Object, "metadata", "annotations", factorycli.AnnotationFixRun)
	unstructured.RemoveNestedField(care.Object, "metadata", "annotations", factorycli.OutputAnnotation(factorycli.AnnotationFixRun))
	for _, sb := range []*unstructured.Unstructured{overseerSandbox("fix-repo-42", false), overseerSandbox("fix-repo-43", true), care} {
		if _, err := dyn.Resource(gvrSandbox).Namespace("overseer-kcc").Create(context.Background(), sb, v1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	clientset := kubernetesfake.NewClientset(
		researchPod("overseer-kcc", "fix-repo-42", "10.1.2.3", corev1.PodRunning),
		researchPod("overseer-kcc", "fix-repo-44", "10.1.2.4", corev1.PodRunning),
		// The namespace's factory-user is a tombstone; the bot's key is
		// the one its runs used.
		&corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: "factory-user", Namespace: "overseer-kcc"}, Data: map[string][]byte{"GEMINI_API_KEY": []byte("dead-key")}},
		&corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: "user-coder-bot", Namespace: "overseer-kcc"}, Data: map[string][]byte{"GEMINI_API_KEY": []byte("bot-key")}},
		// mia has a GitHub token, which GitHub says can push to test/repo;
		// alice has none.
		&corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: "github-pat", Namespace: "mia"}, Data: map[string][]byte{"oauth_pat": []byte("gho_mia")}},
	)
	repoPermCache.Lock()
	repoPermCache.entries = map[string]repoPermEntry{}
	repoPermCache.Unlock()
	rt := &boardMockRT{responses: map[string]string{
		"https://api.github.com/repos/test/repo": `{"permissions": {"push": true}}`,
	}}
	prevGH := githubClientForToken
	githubClientForToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: rt})
	}
	t.Cleanup(func() { githubClientForToken = prevGH })
	manager := &k8s.Manager{Client: dyn, Clientset: clientset}
	server := &Server{K8sManager: manager, Auth: &auth.Authenticator{K8sManager: manager, AdminUsers: []string{"root"}}}

	srv := httptest.NewServer(daemon.handler())
	t.Cleanup(srv.Close)
	prev := taskSessionClientForPod
	taskSessionClientForPod = func(context.Context, *podacpd.Dialer, *corev1.Pod) (*acpd.Client, bool) {
		return acpd.New(srv.URL), true
	}
	t.Cleanup(func() { taskSessionClientForPod = prev })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, c.GetHeader("X-User"))
		c.Set(auth.NamespaceKey, c.GetHeader("X-User"))
		c.Next()
	})
	r.GET("/api/overseers", server.getOverseers)
	overseer := r.Group("/api/overseers", server.overseerAccess())
	overseer.GET("/:name/work", server.getOverseerWork)
	scoped := overseer.Group("/:name", server.overseerScope())
	scoped.GET("/task-sessions/:sandbox/:task", server.getTaskSession)
	scoped.POST("/task-sessions/:sandbox/:task/prompt", server.promptTaskSession)
	scoped.GET("/sandbox-card/:sandbox", server.getSandboxCard)
	scoped.POST("/sandbox-card/:sandbox/lifecycle", server.sandboxLifecycle)
	return r
}

func doAs(t *testing.T, r *gin.Engine, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const overseerSessionAt = "/api/overseers/kcc/task-sessions/fix-repo-42/recipe-fix-1"

// An overseer is an admin's, a maintainer's of its repository, and an
// allowlisted login's, all of it: sessions, cards, and driving them.
// Anyone else is refused, as for an overseer that does not exist.
func TestOverseerAccess(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	paths := []string{overseerSessionAt, "/api/overseers/kcc/sandbox-card/fix-repo-43", "/api/overseers/kcc/work"}
	for _, user := range []string{"root", "dora", "mia"} {
		for _, path := range paths {
			if w := doAs(t, r, user, http.MethodGet, path, ""); w.Code != http.StatusOK {
				t.Errorf("%s GET %s: %d, want 200: %s", user, path, w.Code, w.Body.String())
			}
		}
	}
	if w := doAs(t, r, "mia", http.MethodPost, overseerSessionAt+"/prompt", `{"text":"hi"}`); w.Code != http.StatusAccepted {
		t.Errorf("a maintainer drives the session too: %d %s", w.Code, w.Body.String())
	}
	for _, path := range append(paths, "/api/overseers/nope/work") {
		if w := doAs(t, r, "alice", http.MethodGet, path, ""); w.Code != http.StatusForbidden {
			t.Errorf("alice GET %s: %d, want 403", path, w.Code)
		}
	}
	if w := doAs(t, r, "root", http.MethodGet, "/api/overseers/Bad%20Name/sandbox-card/x", ""); w.Code != http.StatusBadRequest {
		t.Errorf("a bad overseer name: %d, want 400", w.Code)
	}
}

// The list is the overseers the member may see.
func TestOverseerListIsTheOnesYouMaySee(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	names := func(user string) []string {
		w := doAs(t, r, user, http.MethodGet, "/api/overseers", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", user, w.Code, w.Body.String())
		}
		var items []struct {
			Metadata struct{ Name string } `json:"metadata"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, it := range items {
			out = append(out, it.Metadata.Name)
		}
		return out
	}
	for user, want := range map[string]string{"root": "kcc,other", "dora": "kcc", "mia": "kcc", "alice": ""} {
		if got := strings.Join(names(user), ","); got != want {
			t.Errorf("%s sees %q, want %q", user, got, want)
		}
	}
}

// An overseer's run opens in the slide-over from overseer-kcc: no
// recipes to run, and its output to read, without actions (its CLI applied it;
// it is on no board to file one on).
func TestAnOverseerSessionIsReadOnly(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	w := doAs(t, r, "root", http.MethodGet, overseerSessionAt, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Namespace string            `json:"namespace"`
		Recipes   []json.RawMessage `json:"recipes"`
		Draft     *struct {
			Kind    string            `json:"kind"`
			Actions []json.RawMessage `json:"actions"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "overseer-kcc" || len(got.Recipes) != 0 || got.Draft == nil || got.Draft.Kind != "Change" || got.Draft.Actions == nil || len(got.Draft.Actions) != 0 {
		t.Errorf("session = %s", w.Body.String())
	}
}

// Continuing an overseer's run opens its conversation with the key of the
// bot it ran as, not the namespace's factory-user.
func TestAnOverseerSessionOpensWithItsBotsKey(t *testing.T) {
	daemon := &fakeSessions{}
	r := overseerSessionServer(t, daemon)
	if w := doAs(t, r, "root", http.MethodPost, overseerSessionAt+"/prompt", `{"text":"why did the push fail?"}`); w.Code != http.StatusAccepted {
		t.Fatalf("prompt: %d %s", w.Code, w.Body.String())
	}
	if daemon.createKey != "bot-key" {
		t.Errorf("the conversation opened with key %q, want the bot's", daemon.createKey)
	}
}

// The card is the overseer's sandbox's, named by :sandbox (:name is the
// overseer).
func TestAnOverseerSandboxCard(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	w := doAs(t, r, "root", http.MethodGet, "/api/overseers/kcc/sandbox-card/fix-repo-43", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"fix-repo-43"`) || !strings.Contains(w.Body.String(), `"paused":true`) {
		t.Errorf("card: %d %s", w.Code, w.Body.String())
	}
}

// The routes as RegisterRoutes mounts them: the overseer's sessions and
// cards under /api/overseers (the admin group), and no terminal that
// names its namespace in the URL any more.
func TestOverseerSessionRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewServer(&k8s.Manager{}, &auth.Authenticator{}).RegisterRoutes(r)
	have := map[string]bool{}
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	for _, want := range []string{
		"GET /api/overseers/:name/work",
		"GET /api/overseers/:name/task-sessions/:sandbox/:task",
		"POST /api/overseers/:name/task-sessions/:sandbox/:task/prompt",
		"GET /api/overseers/:name/task-session-events/:sandbox/:task",
		"GET /api/overseers/:name/sandbox-card/:sandbox",
		"GET /api/overseers/:name/sandbox-card/:sandbox/terminal",
		"POST /api/overseers/:name/sandbox-card/:sandbox/lifecycle",
	} {
		if !have[want] {
			t.Errorf("no route %s", want)
		}
	}
	for _, gone := range []string{
		"GET /api/terminal/:namespace/:name",
		// The sandbox table's and the chores', which the page no longer has.
		"GET /api/overseers/:name/sandboxes",
		"GET /api/overseers/:name/sandboxes/:sandboxName/tasks",
		"DELETE /api/overseers/:name/sandboxes/:sandboxName",
		"POST /api/overseers/:name/sandboxes/:sandboxName/pause",
		"GET /api/overseers/:name/chores",
		"POST /api/overseers/:name/task-sessions/:sandbox/:task/recipe",
		"POST /api/overseers/:name/task-sessions/:sandbox/:task/draft/:verb",
	} {
		if have[gone] {
			t.Errorf("route %s is registered", gone)
		}
	}
}

// overseerRun is a run in overseer-kcc whose result the board's
// controller never stored: the session reads it from the run's task
// directory, with when the overseer's CLI applied it.
func TestAnOverseerSessionReadsItsRunsResult(t *testing.T) {
	daemon := &fakeSessions{files: map[string]string{
		"recipe-care-ci-9/task-output.yaml": "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\n" +
			"source:\n  task: recipe-care-ci-9\nspec:\n  title: keep settings.tier\n  report: unit-tests failed because the mapper dropped settings.tier\n",
		"recipe-care-ci-9/task-output.applied": "2026-10-12T12:30:00Z\n",
	}}
	r := overseerSessionServer(t, daemon)
	// The care-ci run, recorded with no stored output.
	runAt := "/api/overseers/kcc/task-sessions/fix-repo-44/recipe-care-ci-9"
	w := doAs(t, r, "root", http.MethodGet, runAt, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Draft *struct {
			Kind      string            `json:"kind"`
			Spec      string            `json:"spec"`
			AppliedAt string            `json:"appliedAt"`
			Actions   []json.RawMessage `json:"actions"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Draft == nil || got.Draft.Kind != "Change" || !strings.Contains(got.Draft.Spec, "mapper dropped settings.tier") ||
		got.Draft.AppliedAt != "2026-10-12T12:30:00Z" || got.Draft.Actions == nil || len(got.Draft.Actions) != 0 {
		t.Errorf("session = %s", w.Body.String())
	}
	if !daemon.sawCall("GET /tasks/recipe-care-ci-9/files/task-output.yaml") {
		t.Errorf("the result was not read from the task: %v", daemon.calls)
	}

	// Its apply failed: why, beside the draft; emptied by a later apply
	// that succeeds, it is gone.
	daemon.files["recipe-care-ci-9/task-output.apply-failed"] = `{"action":"post-replies","at":"2026-10-12T12:40:00Z","error":"replying to comment 7: neither a review comment nor a comment"}`
	w = doAs(t, r, "root", http.MethodGet, runAt, "")
	var failed struct {
		Draft *struct {
			ApplyFailed *struct {
				Action string `json:"action"`
				At     string `json:"at"`
				Error  string `json:"error"`
			} `json:"applyFailed"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if f := failed.Draft; f == nil || f.ApplyFailed == nil || f.ApplyFailed.Action != "post-replies" ||
		f.ApplyFailed.At != "2026-10-12T12:40:00Z" || !strings.Contains(f.ApplyFailed.Error, "neither a review comment") {
		t.Errorf("failed apply: %s", w.Body.String())
	}
	daemon.files["recipe-care-ci-9/task-output.apply-failed"] = ""
	w = doAs(t, r, "root", http.MethodGet, runAt, "")
	if strings.Contains(w.Body.String(), `"applyFailed"`) {
		t.Errorf("cleared mark still shown: %s", w.Body.String())
	}

	// Neither applied nor failed, its run long over: nobody applied it
	// (the overseer task that started it stopped waiting for it).
	if strings.Contains(w.Body.String(), `"unapplied"`) {
		t.Errorf("an applied result is unapplied: %s", w.Body.String())
	}
	delete(daemon.files, "recipe-care-ci-9/task-output.applied")
	delete(daemon.files, "recipe-care-ci-9/task-output.apply-failed")
	w = doAs(t, r, "root", http.MethodGet, runAt, "")
	if !strings.Contains(w.Body.String(), `"unapplied":true`) {
		t.Errorf("a result nobody applied is not unapplied: %s", w.Body.String())
	}

	// A run that left none: no draft.
	daemon.files = nil
	w = doAs(t, r, "root", http.MethodGet, runAt, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"draft"`) {
		t.Errorf("no result: %d %s", w.Code, w.Body.String())
	}
}

// Clear & retry on the Overseer page: the lifecycle's retry clears
// factory's requires-human mark, for whoever may use the overseer.
func TestAnOverseerSandboxRetry(t *testing.T) {
	r := overseerSessionServer(t, &fakeSessions{})
	if w := doAs(t, r, "alice", http.MethodPost, "/api/overseers/kcc/sandbox-card/fix-repo-43/lifecycle", `{"action":"retry"}`); w.Code != http.StatusForbidden {
		t.Errorf("alice: %d, want 403", w.Code)
	}
	if w := doAs(t, r, "root", http.MethodPost, "/api/overseers/kcc/sandbox-card/fix-repo-43/lifecycle", `{"action":"retry"}`); w.Code != http.StatusOK {
		t.Errorf("retry: %d %s", w.Code, w.Body.String())
	}
}

// A run's result is unapplied only once its run completed a while ago,
// with an action to apply, and neither mark beside it.
func TestUnapplied(t *testing.T) {
	now := time.Unix(10_000, 0)
	recent, old := now.Add(-time.Minute), now.Add(-time.Hour)
	change := "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\nspec:\n  title: x\n"
	draftOnly := change + "actions:\n  - verb: edit\n    field: spec\n  - verb: reject\n"
	for _, tc := range []struct {
		name  string
		run   factorycli.RecordedRun
		doc   string
		draft taskSessionDraft
		want  bool
	}{
		{"nobody applied it", factorycli.RecordedRun{State: "Completed", EndedAt: &old}, change, taskSessionDraft{}, true},
		{"just ended", factorycli.RecordedRun{State: "Completed", EndedAt: &recent}, change, taskSessionDraft{}, false},
		{"applied", factorycli.RecordedRun{State: "Completed", EndedAt: &old}, change, taskSessionDraft{AppliedAt: "x"}, false},
		{"apply failed", factorycli.RecordedRun{State: "Completed", EndedAt: &old}, change, taskSessionDraft{ApplyFailed: &taskApplyFailure{Error: "e"}}, false},
		{"run failed", factorycli.RecordedRun{State: "Failed", EndedAt: &old}, change, taskSessionDraft{}, false},
		{"running", factorycli.RecordedRun{State: "Running"}, change, taskSessionDraft{}, false},
		{"nothing to apply", factorycli.RecordedRun{State: "Completed", EndedAt: &old}, draftOnly, taskSessionDraft{}, false},
	} {
		if got := unapplied(tc.run, "Change", tc.doc, &tc.draft, now); got != tc.want {
			t.Errorf("%s: unapplied = %v, want %v", tc.name, got, tc.want)
		}
	}
}
