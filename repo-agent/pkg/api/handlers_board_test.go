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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

type boardMockRT struct {
	responses map[string]string
}

func (m *boardMockRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body, ok := m.responses[req.URL.String()]
	status := http.StatusOK
	if !ok {
		body, status = `{}`, http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func boardTestServer(t *testing.T, ghResponses map[string]string, objs ...runtime.Object) (*Server, *gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	// repoPermCache is package-global; drop verdicts from earlier tests.
	repoPermCache.Lock()
	repoPermCache.entries = map[string]repoPermEntry{}
	repoPermCache.Unlock()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrSandbox:   "SandboxList",
		repoBoardGVR: "RepoBoardList",
	})
	// Seed via explicit Create: the fake's kind->resource guesser pluralizes
	// Sandbox as "sandboxs", so initial-object seeding lands in the wrong
	// resource.
	for _, o := range objs {
		u := o.(*unstructured.Unstructured)
		gvr := gvrSandbox
		if u.GetKind() == "RepoBoard" {
			gvr = repoBoardGVR
		}
		if _, err := dynamicClient.Resource(gvr).Namespace(u.GetNamespace()).Create(context.Background(), u, v1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", u.GetName(), err)
		}
	}
	k8sClient := kubernetesfake.NewClientset(&corev1.Secret{
		ObjectMeta: v1.ObjectMeta{Name: "github-pat", Namespace: "alice"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_alice")},
	})

	prev := githubClientForToken
	githubClientForToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &boardMockRT{responses: ghResponses}})
	}
	t.Cleanup(func() { githubClientForToken = prev })

	manager := &k8s.Manager{Client: dynamicClient, Clientset: k8sClient}
	server := &Server{K8sManager: manager, Auth: &auth.Authenticator{K8sManager: manager}}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "alice")
		c.Next()
	})
	r.GET("/board/:board/work", server.getBoardWork)
	r.POST("/board/:board/issues/:id/fix", server.kickoffFix)
	r.POST("/board/:board/prs/:id/review", server.kickoffReview)
	r.POST("/board/:board/issues/:id/rerun", server.rerunBoardIssue)
	r.GET("/boards", server.getBoards)
	r.POST("/boards", server.createBoard)
	r.DELETE("/board/:board", server.deleteBoard)
	return server, r, dynamicClient
}

func boardCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "board.gemini.google.com/v1alpha1",
		"kind":       "RepoBoard",
		"metadata":   map[string]interface{}{"name": "myboard", "namespace": "alice"},
		"spec": map[string]interface{}{
			"repoURL":  "https://github.com/test/repo",
			"triggers": map[string]interface{}{"label": "agent"},
		},
	}}
}

func sandboxCR(name string, labels, annotations map[string]interface{}, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "alice",
			"labels": labels, "annotations": annotations,
		},
		"spec": map[string]interface{}{"replicas": replicas},
	}}
}

func TestGetBoardWork(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&per_page=100&state=open": `[
			{"number": 10, "title": "fixing", "html_url": "https://github.com/test/repo/issues/10", "updated_at": "2026-09-16T10:00:00Z",
			 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[
			{"number": 42, "title": "review me", "html_url": "https://github.com/test/repo/pull/42", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	fixSandbox := sandboxCR("fix-repo-10",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"sandbox.gemini.google.com/last-task-state": "Running", "htmlURL": "https://github.com/test/repo/issues/10"}, 1)
	reviewSandbox := sandboxCR("factory-pr-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "42"},
		map[string]interface{}{"reviewState": "pending", "htmlURL": "https://github.com/test/repo/pull/42"}, 1)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), fixSandbox, reviewSandbox)

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(work) != 2 {
		t.Fatalf("expected 2 rows, got %d: %s", len(work), w.Body.String())
	}

	byKey := map[string]models.WorkItem{}
	for _, item := range work {
		byKey[item.Type+"-"+itoa(item.Number)] = item
	}

	if row := byKey["issue-10"]; row.Stage != "fixing" || row.Attention != "working" || row.Sandbox == nil || row.Sandbox.Name != "fix-repo-10" {
		t.Errorf("issue-10 row wrong: %+v", row)
	}
	if row := byKey["pr-42"]; row.Stage != "review-pending" || row.Attention != "needs-you" {
		t.Errorf("pr-42 row wrong: %+v", row)
	}

	// needs-you rows sort before working rows.
	if work[len(work)-1].Attention != "working" {
		t.Errorf("expected the working row last, got %+v", work)
	}
}

func TestKickoffFixWritesMailbox(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{
		// Best-effort assignment + label calls; served happily.
		"https://api.github.com/repos/test/repo/issues/77/assignees": `{}`,
		"https://api.github.com/repos/test/repo/issues/77/labels":    `[]`,
	}, boardCR())

	req, _ := http.NewRequest("POST", "/board/myboard/issues/77/fix", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := board.GetAnnotations()[annoBoardRequests]
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &requests); err != nil {
		t.Fatalf("bad mailbox: %q", raw)
	}
	if requests["fix-77"] != "alice" {
		t.Errorf("expected fix-77=alice in mailbox, got %v", requests)
	}
}

func TestKickoffForbiddenForNonMember(t *testing.T) {
	// Personal model: a board outside the session namespace does not resolve.
	board := boardCR()
	board.SetNamespace("board-kcc")
	_, r, _ := boardTestServer(t, map[string]string{}, board)

	req, _ := http.NewRequest("POST", "/board/myboard/issues/77/fix", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRerunBoardIssue(t *testing.T) {
	fixSandbox := sandboxCR("fix-repo-10",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"sandbox.gemini.google.com/last-task-state": "Completed", "htmlURL": "https://github.com/test/repo/issues/10"}, 0)
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR(), fixSandbox)

	req, _ := http.NewRequest("POST", "/board/myboard/issues/10/rerun", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	sb, err := dyn.Resource(gvrSandbox).Namespace("alice").Get(context.Background(), "fix-repo-10", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetAnnotations()["review.gemini.google.com/refix-requested-at"] == "" {
		t.Errorf("expected refix annotation, got %v", sb.GetAnnotations())
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestCreateAndDeleteBoard(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{})

	req, _ := http.NewRequest("POST", "/boards", strings.NewReader(`{"repoURL": "https://github.com/test/repo"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "repo", v1.GetOptions{})
	if err != nil {
		t.Fatalf("board not created: %v", err)
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	if repoURL != "https://github.com/test/repo" {
		t.Errorf("unexpected repoURL %q", repoURL)
	}

	req, _ = http.NewRequest("DELETE", "/board/repo", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "repo", v1.GetOptions{}); err == nil {
		t.Errorf("board still exists after delete")
	}
}

// Shared boards (mode github) are visible only when the viewer's own token
// proves push permission on the repo.
func TestOtherNamespaceBoardInvisible(t *testing.T) {
	// Boards are personal: a board in another namespace is not resolvable.
	other := boardCR()
	other.SetNamespace("board-kcc")
	_, r, _ := boardTestServer(t, map[string]string{}, other)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for another namespace's board, got %d: %s", w.Code, w.Body.String())
	}
}

// Settings roundtrip: the opt-in lands in the session member's own
// namespace keyed by the board.
func TestBoardSettings(t *testing.T) {
	server, r, _ := boardTestServer(t, map[string]string{}, boardCR())
	r.GET("/board/:board/settings", server.getBoardSettings)
	r.PUT("/board/:board/settings", server.putBoardSettings)

	req, _ := http.NewRequest("PUT", "/board/myboard/settings", strings.NewReader(`{"autoFix": true}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cm, err := server.K8sManager.Clientset.CoreV1().ConfigMaps("alice").Get(context.Background(), "agent-preferences", v1.GetOptions{})
	if err != nil {
		t.Fatalf("prefs configmap missing: %v", err)
	}
	if cm.Data["autofix.alice.myboard"] != "true" {
		t.Errorf("expected opt-in recorded, got %v", cm.Data)
	}

	req, _ = http.NewRequest("GET", "/board/myboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"autoFix":true`) {
		t.Errorf("get: expected autoFix true, got %d %s", w.Code, w.Body.String())
	}

	req, _ = http.NewRequest("PUT", "/board/myboard/settings", strings.NewReader(`{"autoFix": false}`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put off: expected 200, got %d", w.Code)
	}
	cm, _ = server.K8sManager.Clientset.CoreV1().ConfigMaps("alice").Get(context.Background(), "agent-preferences", v1.GetOptions{})
	if _, ok := cm.Data["autofix.alice.myboard"]; ok {
		t.Errorf("expected opt-in removed, got %v", cm.Data)
	}
}

// Publish posts the stored draft as a pending review under the member's
// token and marks the sandbox submitted.
// Promote no-ops on a non-draft PR and calls the GraphQL mutation for a
// draft one.
func TestPromoteBoardPR(t *testing.T) {
	called := false
	prev := markPRReadyForReview
	markPRReadyForReview = func(_ context.Context, _, nodeID string) error {
		called = true
		if nodeID != "NODE42" {
			t.Errorf("unexpected node id %q", nodeID)
		}
		return nil
	}
	t.Cleanup(func() { markPRReadyForReview = prev })

	server, r, _ := boardTestServer(t, map[string]string{
		"https://api.github.com/repos/test/repo/pulls/42": `{"number": 42, "draft": true, "node_id": "NODE42"}`,
	}, boardCR())
	r.POST("/board/:board/prs/:id/promote", server.promoteBoardPR)

	req, _ := http.NewRequest("POST", "/board/myboard/prs/42/promote", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Errorf("expected GraphQL promote to be called")
	}
}

// Grouping and issue→PR folding: an issue with an open fix PR disappears in
// favor of the PR row (which records the linkage); authored PRs land in
// mine-pr, incoming reviews in review, self-filed issues in mine-issue.
func TestGetBoardWorkGroupsAndFolding(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&per_page=100&state=open": `[
			{"number": 10, "title": "assigned, being fixed", "html_url": "https://github.com/test/repo/issues/10", "updated_at": "2026-09-16T10:00:00Z",
			 "assignees": [{"login": "alice"}]},
			{"number": 13, "title": "assigned, bot PR open", "html_url": "https://github.com/test/repo/issues/13", "updated_at": "2026-09-16T08:00:00Z",
			 "assignees": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&per_page=100&state=open": `[
			{"number": 12, "title": "my filed issue", "html_url": "https://github.com/test/repo/issues/12", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[
			{"number": 50, "title": "my fix", "html_url": "https://github.com/test/repo/pull/50", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "alice"}, "draft": true, "body": "This change...\n\nFixes #10"},
			{"number": 42, "title": "review me", "html_url": "https://github.com/test/repo/pull/42", "updated_at": "2026-09-16T11:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]},
			{"number": 60, "title": "bot fix for 13", "html_url": "https://github.com/test/repo/pull/60", "updated_at": "2026-09-16T07:00:00Z",
			 "user": {"login": "hopper-coder-bot"}, "body": "This PR resolves issue #13.\n\nFixes #13"}
		]`,
	}

	_, r, _ := boardTestServer(t, ghResponses, boardCR())

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	byKey := map[string]models.WorkItem{}
	for _, item := range work {
		byKey[item.Type+"-"+itoa(item.Number)] = item
	}

	if _, ok := byKey["issue-10"]; ok {
		t.Errorf("issue-10 should be folded into PR 50: %s", w.Body.String())
	}
	pr50 := byKey["pr-50"]
	if pr50.Group != "mine-pr" || !pr50.DraftPR || len(pr50.Fixes) != 1 || pr50.Fixes[0] != 10 {
		t.Errorf("pr-50 row wrong: %+v", pr50)
	}
	if row := byKey["pr-42"]; row.Group != "review" {
		t.Errorf("pr-42 should be group review: %+v", row)
	}
	if row := byKey["issue-12"]; row.Group != "issues" {
		t.Errorf("issue-12 should be group issues: %+v", row)
	}

	// A bot-authored PR with no involvement signals still surfaces (and
	// swallows the issue) because it addresses board work.
	if _, ok := byKey["issue-13"]; ok {
		t.Errorf("issue-13 should be folded into bot PR 60: %s", w.Body.String())
	}
	pr60 := byKey["pr-60"]
	if pr60.Group != "review" || len(pr60.Fixes) != 1 || pr60.Fixes[0] != 13 {
		t.Errorf("pr-60 row wrong: %+v", pr60)
	}

	if len(work) != 4 {
		t.Errorf("expected 4 rows after folding, got %d: %s", len(work), w.Body.String())
	}
}

// The triage group: with intake.triageIssues on, every open issue (minus
// excluded and trigger-labeled) gets a row — triage-ready when a draft
// exists, untriaged otherwise. Rows claimed by fix/mine keep their group.
func TestGetBoardWorkTriageGroup(t *testing.T) {
	board := boardCR()
	_ = unstructured.SetNestedField(board.Object, true, "spec", "intake", "triageIssues")
	_ = unstructured.SetNestedStringSlice(board.Object, []string{"wontfix"}, "spec", "intake", "filters", "excludeLabels")

	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&per_page=100&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open":   `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&per_page=100&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": `[
			{"number": 20, "title": "draft ready", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T10:00:00Z"},
			{"number": 21, "title": "plain", "html_url": "https://github.com/test/repo/issues/21", "updated_at": "2026-09-16T09:00:00Z"},
			{"number": 22, "title": "excluded", "html_url": "https://github.com/test/repo/issues/22", "updated_at": "2026-09-16T08:00:00Z",
			 "labels": [{"name": "wontfix"}]},
			{"number": 23, "title": "labeled routes to fix", "html_url": "https://github.com/test/repo/issues/23", "updated_at": "2026-09-16T07:00:00Z",
			 "labels": [{"name": "agent"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[]`,
	}
	triageSandbox := sandboxCR("triage-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"agentDraft": "triage:\n  labels: [bug]", "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

	_, r, _ := boardTestServer(t, ghResponses, board, triageSandbox)

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	byKey := map[string]models.WorkItem{}
	for _, item := range work {
		byKey[item.Type+"-"+itoa(item.Number)] = item
	}

	if row := byKey["issue-20"]; row.Group != "issues" || row.Stage != "triage-ready" || row.Draft == "" {
		t.Errorf("issue-20 row wrong: %+v", row)
	}
	if row := byKey["issue-21"]; row.Group != "issues" || row.Stage != "untriaged" {
		t.Errorf("issue-21 row wrong: %+v", row)
	}
	if _, ok := byKey["issue-22"]; ok {
		t.Errorf("excluded issue-22 should not appear: %s", w.Body.String())
	}
	// The trigger label is automation-only: a labeled, unassigned issue
	// still lists in the triage view.
	if row, ok := byKey["issue-23"]; !ok || row.Group != "issues" {
		t.Errorf("issue-23 (labeled, unassigned) should list in triage: %s", w.Body.String())
	}
}

// A bare GitHub review request is "review-requested" (not "queued" — nothing
// launches without a click), is nobody's claim, and only fresh requests are
// needs-you; fossils stay out of UP NEXT.
func TestGetBoardWorkReviewRequested(t *testing.T) {
	fresh := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&per_page=100&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open":   `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&per_page=100&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[
			{"number": 70, "title": "fresh request", "html_url": "https://github.com/test/repo/pull/70", "updated_at": "` + fresh + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]},
			{"number": 71, "title": "fossil request", "html_url": "https://github.com/test/repo/pull/71", "updated_at": "2026-01-01T00:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}

	_, r, _ := boardTestServer(t, ghResponses, boardCR())

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	byKey := map[string]models.WorkItem{}
	for _, item := range work {
		byKey[item.Type+"-"+itoa(item.Number)] = item
	}

	if row := byKey["pr-70"]; row.Stage != "review-requested" || row.Attention != "needs-you" || row.Assignee != "" {
		t.Errorf("fresh request row wrong: %+v", row)
	}
	if row := byKey["pr-71"]; row.Stage != "review-requested" || row.Attention != "waiting" || row.Assignee != "" {
		t.Errorf("fossil request row wrong: %+v", row)
	}
}

// getBoards reports the viewer's repo role so the UI can gate fix flows:
// push permission => maintainer, anything else => read-only.
func TestGetBoardsRole(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo": `{"permissions": {"push": true}}`,
		// second board's repo: 404 -> read-only
	}
	roBoard := boardCR()
	roBoard.SetName("otherboard")
	_ = unstructured.SetNestedField(roBoard.Object, "https://github.com/test/otherrepo", "spec", "repoURL")

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), roBoard)

	req, _ := http.NewRequest("GET", "/boards", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var boards []models.Board
	if err := json.Unmarshal(w.Body.Bytes(), &boards); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	roles := map[string]string{}
	for _, b := range boards {
		roles[b.Name] = b.Role
	}
	if roles["myboard"] != "maintainer" {
		t.Errorf("myboard role: want maintainer, got %q", roles["myboard"])
	}
	if roles["otherboard"] != "read-only" {
		t.Errorf("otherboard role: want read-only, got %q", roles["otherboard"])
	}
}

// The trigger label is automation-only: a labeled PR with no involvement
// signal (not authored, no review request, no sandbox, no issue link) does
// not appear in the view at all.
func TestLabeledPRNotAView(t *testing.T) {
	prsJSON := `[
		{"number": 80, "title": "labeled", "html_url": "https://github.com/test/repo/pull/80", "updated_at": "2026-09-17T10:00:00Z",
		 "user": {"login": "carol"}, "labels": [{"name": "agent"}]}
	]`
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": prsJSON,
	}

	_, r, _ := boardTestServer(t, ghResponses, boardCR())
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 80 {
			t.Errorf("labeled-only PR must not appear in the view: %+v", item)
		}
	}
}

// GitHub's native "review again": submitting clears you from
// requested_reviewers, a re-request re-adds you — a submitted row with a
// fresh request returns to review-requested instead of staying quiet.
func TestSubmittedThenReRequested(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[
			{"number": 90, "title": "re-requested", "html_url": "https://github.com/test/repo/pull/90", "updated_at": "` + fresh + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	submittedSandbox := sandboxCR("factory-pr-90",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "90"},
		map[string]interface{}{"reviewState": "submitted", "htmlURL": "https://github.com/test/repo/pull/90"}, 0)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), submittedSandbox)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 90 {
			if item.Stage != "review-requested" || item.Attention != "needs-you" {
				t.Errorf("re-requested after submit: want review-requested/needs-you, got %q/%q", item.Stage, item.Attention)
			}
			return
		}
	}
	t.Fatal("pr-90 row missing")
}

// Between a click and the run: a mailbox entry renders the row as
// Starting… (no needs-you, no second kickoff invited), and a sandbox
// without a task state (provisioning) does the same.
func TestKickoffFeedbackStages(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": `[
			{"number": 5, "title": "clicked", "html_url": "https://github.com/test/repo/pull/5", "updated_at": "` + fresh + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]},
			{"number": 6, "title": "provisioning", "html_url": "https://github.com/test/repo/pull/6", "updated_at": "` + fresh + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	board := boardCR()
	board.SetAnnotations(map[string]string{"board.gemini.google.com/requests": `{"review-5": "alice"}`})
	provisioning := sandboxCR("factory-pr-6",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "6"},
		map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/6"}, 1)

	_, r, _ := boardTestServer(t, ghResponses, board, provisioning)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	stages := map[int][2]string{}
	for _, item := range work {
		stages[item.Number] = [2]string{item.Stage, item.Attention}
	}
	if s := stages[5]; s[0] != "review-starting" || s[1] != "working" {
		t.Errorf("mailboxed PR: want review-starting/working, got %v", s)
	}
	if s := stages[6]; s[0] != "review-starting" || s[1] != "working" {
		t.Errorf("provisioning PR: want review-starting/working, got %v", s)
	}
}

// Maintainers see the whole review queue; uninvolved PRs list for them
// (view only) while non-maintainers keep involvement-only.
func TestMaintainerSeesFullReviewQueue(t *testing.T) {
	prsJSON := `[
		{"number": 300, "title": "someone elses PR", "html_url": "https://github.com/test/repo/pull/300", "updated_at": "2026-09-17T10:00:00Z",
		 "user": {"login": "carol"}, "requested_reviewers": [{"login": "dave"}]}
	]`
	// Maintainer: repo permissions grant push.
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": prsJSON,
		"https://api.github.com/repos/test/repo":                               `{"permissions": {"push": true}}`,
	}
	_, r, _ := boardTestServer(t, ghResponses, boardCR())
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	found := false
	for _, item := range work {
		if item.Number == 300 && item.Group == "review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("maintainer should see uninvolved PR 300 in review: %s", w.Body.String())
	}

	// Non-maintainer (permission check 404s): involvement-only.
	ghNo := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": prsJSON,
	}
	_, r2, _ := boardTestServer(t, ghNo, boardCR())
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, req)
	var work2 []models.WorkItem
	_ = json.Unmarshal(w2.Body.Bytes(), &work2)
	for _, item := range work2 {
		if item.Number == 300 {
			t.Fatalf("non-maintainer should not see uninvolved PR 300: %s", w2.Body.String())
		}
	}
}
