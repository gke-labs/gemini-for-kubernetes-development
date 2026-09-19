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
	// Package-global caches; drop verdicts from earlier tests.
	workFeedCache.Lock()
	workFeedCache.entries = map[string]workFeedEntry{}
	workFeedCache.refreshing = map[string]bool{}
	workFeedCache.Unlock()
	repoSuggestionCache.Lock()
	repoSuggestionCache.entries = map[string]repoSuggestionEntry{}
	repoSuggestionCache.Unlock()
	pendingReviewCache.Lock()
	pendingReviewCache.entries = map[string]pendingReviewEntry{}
	pendingReviewCache.Unlock()
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
	r.GET("/repo-suggestions", server.getRepoSuggestions)
	r.POST("/board/:board/issues/:id/fix", server.kickoffFix)
	r.POST("/board/:board/prs/:id/review", server.kickoffReview)
	r.POST("/board/:board/issues/:id/rerun", server.rerunBoardIssue)
	r.POST("/board/:board/issues/:id/plan", server.kickoffPlan)
	r.POST("/board/:board/issues/:id/plan-feedback", server.planBoardFeedback)
	r.POST("/board/:board/issues/:id/plan-approve", server.planBoardApprove)
	r.POST("/board/:board/issues/:id/plan-reject", server.planBoardReject)
	r.POST("/board/:board/issues/:id/triage-reject", server.rejectBoardTriage)
	r.PUT("/board/:board/issues/:id/plan-draft", server.putBoardPlanDraft)
	r.PUT("/board/:board/issues/:id/draft", server.putBoardTriageDraft)
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
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 10, "title": "fixing", "html_url": "https://github.com/test/repo/issues/10", "updated_at": "2026-09-16T10:00:00Z",
			 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
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

// The gear edits the auto block on the spec: verbs are scope enums,
// invalid values fall back to off, recency defaults sensibly.
func TestBoardSpecAuto(t *testing.T) {
	server, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	r.GET("/board/:board/spec", server.getBoardSpec)
	r.PUT("/board/:board/spec", server.putBoardSpec)

	body := `{"autoTriage": "unclaimed", "autoFix": "assigned", "autoReview": "requested",
		"autoLabels": ["bug"], "autoExcludeLabels": ["wontfix"], "recencyDays": 30, "maxActive": 5, "idleMinutes": 15}`
	req, _ := http.NewRequest("PUT", "/board/myboard/spec", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	idle, _, _ := unstructured.NestedInt64(board.Object, "spec", "sandbox", "idleMinutes")
	if idle != 15 {
		t.Errorf("idleMinutes not stored: %d", idle)
	}
	triage, _, _ := unstructured.NestedString(board.Object, "spec", "auto", "triage")
	review, _, _ := unstructured.NestedString(board.Object, "spec", "auto", "review")
	recency, _, _ := unstructured.NestedInt64(board.Object, "spec", "auto", "recencyDays")
	if triage != "unclaimed" || review != "requested" || recency != 30 {
		t.Errorf("auto block not stored: triage=%q review=%q recency=%d", triage, review, recency)
	}

	req, _ = http.NewRequest("GET", "/board/myboard/spec", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	for _, want := range []string{`"autoTriage":"unclaimed"`, `"autoReview":"requested"`, `"recencyDays":30`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("spec view missing %s: %s", want, w.Body.String())
		}
	}

	// Junk enum values degrade to off, never to a launch.
	req, _ = http.NewRequest("PUT", "/board/myboard/spec", strings.NewReader(`{"autoReview": "everything", "recencyDays": 0}`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	board, _ = dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	review, _, _ = unstructured.NestedString(board.Object, "spec", "auto", "review")
	recency, _, _ = unstructured.NestedInt64(board.Object, "spec", "auto", "recencyDays")
	if review != "off" || recency != 7 {
		t.Errorf("expected off/7 fallback, got %q/%d", review, recency)
	}
}

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
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 10, "title": "assigned, being fixed", "html_url": "https://github.com/test/repo/issues/10", "updated_at": "2026-09-16T10:00:00Z",
			 "assignees": [{"login": "alice"}]},
			{"number": 13, "title": "assigned, bot PR open", "html_url": "https://github.com/test/repo/issues/13", "updated_at": "2026-09-16T08:00:00Z",
			 "assignees": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 12, "title": "my filed issue", "html_url": "https://github.com/test/repo/issues/12", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
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

	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open":                               `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "draft ready", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T10:00:00Z"},
			{"number": 21, "title": "plain", "html_url": "https://github.com/test/repo/issues/21", "updated_at": "2026-09-16T09:00:00Z"},
			{"number": 22, "title": "excluded", "html_url": "https://github.com/test/repo/issues/22", "updated_at": "2026-09-16T08:00:00Z",
			 "labels": [{"name": "wontfix"}]},
			{"number": 23, "title": "labeled routes to fix", "html_url": "https://github.com/test/repo/issues/23", "updated_at": "2026-09-16T07:00:00Z",
			 "labels": [{"name": "agent"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
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
	// The paused triage sandbox surfaces on the resting row (Agent column
	// chip + logs link), like plan-ready and fix-done rows do.
	if row := byKey["issue-20"]; row.Sandbox == nil || row.Sandbox.Name != "triage-repo-20" {
		t.Errorf("issue-20 should carry its triage sandbox: %+v", row.Sandbox)
	}
	if row := byKey["issue-21"]; row.Group != "issues" || row.Stage != "untriaged" {
		t.Errorf("issue-21 row wrong: %+v", row)
	}
	// The feed is the universe: label-carrying rows surface with their
	// labels as metadata; hiding is client-side view state (or the
	// board's view.labels filter).
	if row, ok := byKey["issue-22"]; !ok || len(row.Labels) != 1 || row.Labels[0] != "wontfix" {
		t.Errorf("issue-22 should appear with its labels: %s", w.Body.String())
	}
	if row, ok := byKey["issue-23"]; !ok || row.Group != "issues" {
		t.Errorf("issue-23 should list in triage: %s", w.Body.String())
	}
}

// A bare GitHub review request is "review-requested" (not "queued" — nothing
// launches without a click), is nobody's claim, and only fresh requests are
// needs-you; fossils stay out of UP NEXT.
func TestGetBoardWorkReviewRequested(t *testing.T) {
	fresh := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open":                               `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
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
func TestFeedReturnsFullUniverse(t *testing.T) {
	// The feed carries every open PR plus the metadata client-side views
	// filter on (labels, author, reviewRequested) — what the member LOOKS
	// at is UI state, not server logic.
	prsJSON := `[
		{"number": 80, "title": "uninvolved", "html_url": "https://github.com/test/repo/pull/80", "updated_at": "2026-09-17T10:00:00Z",
		 "user": {"login": "carol"}, "labels": [{"name": "agent"}]}
	]`
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": prsJSON,
	}

	_, r, _ := boardTestServer(t, ghResponses, boardCR())
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 80 {
			if item.ReviewRequested || len(item.Labels) != 1 || item.Labels[0] != "agent" || item.Author != "carol" {
				t.Errorf("universe row missing view metadata: %+v", item)
			}
			return
		}
	}
	t.Fatalf("uninvolved PR must be in the feed universe: %s", w.Body.String())
}

// GitHub's native "review again": submitting clears you from
// requested_reviewers, a re-request re-adds you — a submitted row with a
// fresh request returns to review-requested instead of staying quiet.
func TestSubmittedThenReRequested(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
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
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
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

// Everyone gets the whole PR queue in the feed — scope narrowing (mine /
// requested) is client-side view state, not a permission perk.
func TestFeedIncludesUninvolvedPRs(t *testing.T) {
	prsJSON := `[
		{"number": 300, "title": "someone elses PR", "html_url": "https://github.com/test/repo/pull/300", "updated_at": "2026-09-17T10:00:00Z",
		 "user": {"login": "carol"}, "requested_reviewers": [{"login": "dave"}]}
	]`
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": prsJSON,
	}
	_, r, _ := boardTestServer(t, ghResponses, boardCR())
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 300 && item.Group == "review" && !item.ReviewRequested {
			return
		}
	}
	t.Fatalf("uninvolved PR should be in the universe: %s", w.Body.String())
}

func TestFriendlyReviewError(t *testing.T) {
	if got := friendlyReviewError(""); got != "" {
		t.Errorf("empty error must stay empty, got %q", got)
	}
	got := friendlyReviewError("failed to create review on GitHub: 403 the `kubernetes-sigs` organization has enabled OAuth App access restrictions")
	if !strings.Contains(got, "manual_pat") {
		t.Errorf("OAuth-restriction error should point at manual_pat, got %q", got)
	}
	got = friendlyReviewError("HTTP 401: Bad credentials (https://api.github.com/graphql)")
	if !strings.Contains(got, "sign in again") && !strings.Contains(got, "personal access token") {
		t.Errorf("bad-credentials error should point at re-auth, got %q", got)
	}
	if got := friendlyReviewError("some novel failure"); got != "some novel failure" {
		t.Errorf("unknown errors must pass through, got %q", got)
	}
}

// A pending review parked on GitHub is rediscovered from GitHub itself:
// with no sandbox breadcrumb at all (clean slate, restart) the requested-
// review row must still render "Pending on GitHub", not "Review requested".
func TestGetBoardWorkRediscoversPendingReview(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 42, "title": "review me", "html_url": "https://github.com/test/repo/pull/42", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls/42/reviews?per_page=100": `[
			{"id": 7, "state": "PENDING", "user": {"login": "alice"}}
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
	if len(work) != 1 {
		t.Fatalf("expected 1 row, got %d: %s", len(work), w.Body.String())
	}
	if work[0].Stage != "review-pending" || work[0].Attention != "needs-you" {
		t.Errorf("expected rediscovered review-pending row, got %+v", work[0])
	}
}

// Someone else's pending review (or none) leaves the row as a plain
// review request.
func TestGetBoardWorkNoPendingReviewStaysRequested(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 42, "title": "review me", "html_url": "https://github.com/test/repo/pull/42", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
		"https://api.github.com/repos/test/repo/pulls/42/reviews?per_page=100": `[]`,
	}

	_, r, _ := boardTestServer(t, ghResponses, boardCR())

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(work) != 1 || work[0].Stage != "review-requested" {
		t.Errorf("expected review-requested row, got %s", w.Body.String())
	}
}

// Triage drafts are member-editable, but only within the schema publish
// consumes: malformed YAML, unknown fields, and empty suggestions are
// rejected with the reason; a valid edit replaces the stored draft.
func TestPutBoardTriageDraft(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "triaged", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
	triageSandbox := sandboxCR("triage-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"agentDraft": "triage:\n  labels: [bug]", "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), triageSandbox)

	put := func(draft string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"draft": draft})
		req, _ := http.NewRequest("PUT", "/board/myboard/issues/20/draft", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := put("triage: ["); w.Code != http.StatusBadRequest {
		t.Errorf("malformed YAML: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := put("triage:\n  bogus: field"); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := put("triage: {}"); w.Code != http.StatusBadRequest {
		t.Errorf("empty suggestion: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := put(""); w.Code != http.StatusBadRequest {
		t.Errorf("empty draft: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	edited := "triage:\n  labels: [bug, p1]\n  assessment: human-refined"
	if w := put(edited); w.Code != http.StatusOK {
		t.Fatalf("valid edit: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The stored draft (and hence the work feed) reflects the edit.
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	found := false
	for _, item := range work {
		if item.Type == "issue" && item.Number == 20 {
			found = true
			if !strings.Contains(item.Draft, "human-refined") {
				t.Errorf("draft not updated: %q", item.Draft)
			}
		}
	}
	if !found {
		t.Fatalf("issue-20 missing from feed: %s", w.Body.String())
	}
}

// The plan loop, API side: a plan-carrying fix sandbox renders plan-ready
// with the draft; feedback stamps the refinement markers; approve stamps
// approval and files the fix request; reject clears the draft.
func TestPlanEndpoints(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 42, "title": "needs planning", "html_url": "https://github.com/test/repo/issues/42", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
	planSandbox := sandboxCR("fix-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"htmlURL": "https://github.com/test/repo/issues/42",
			"sandbox.gemini.google.com/last-task-type":  "plan",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"board.gemini.google.com/plan":              "## Summary\nDo the thing.",
			"board.gemini.google.com/planned-at":        "2026-09-17T00:00:00Z",
		}, 1)

	srv, r, dyn := boardTestServer(t, ghResponses, boardCR(), planSandbox)
	_ = srv

	// Feed: plan-ready with the draft attached.
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(work) != 1 || work[0].Stage != "plan-ready" || work[0].Attention != "needs-you" || !strings.Contains(work[0].Plan, "Do the thing.") {
		t.Fatalf("expected plan-ready row with draft, got %s", w.Body.String())
	}

	post := func(path, body string) *httptest.ResponseRecorder {
		req, _ := http.NewRequest("POST", "/board/myboard/issues/42/"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	getAnnotations := func() map[string]string {
		sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), "fix-repo-42", v1.GetOptions{})
		if err != nil {
			t.Fatalf("get sandbox: %v", err)
		}
		return sb.GetAnnotations()
	}

	// Refine: feedback stamped.
	if w := post("plan-feedback", `{"feedback": ""}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty feedback: expected 400, got %d", w.Code)
	}
	if w := post("plan-feedback", `{"feedback": "merge steps 2 and 3"}`); w.Code != http.StatusOK {
		t.Fatalf("feedback: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	annotations := getAnnotations()
	if annotations["board.gemini.google.com/plan-feedback"] != "merge steps 2 and 3" || annotations["board.gemini.google.com/plan-feedback-at"] == "" {
		t.Errorf("feedback not stamped: %v", annotations)
	}

	// Approve: approval stamped and the fix request filed.
	if w := post("plan-approve", `{}`); w.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if getAnnotations()["board.gemini.google.com/plan-approved-at"] == "" {
		t.Error("approval not stamped")
	}
	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	if !strings.Contains(board.GetAnnotations()["board.gemini.google.com/requests"], "fix-42") {
		t.Errorf("approve did not file the fix request: %v", board.GetAnnotations())
	}

	// Reject: draft cleared, reject stamped.
	if w := post("plan-reject", `{}`); w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	annotations = getAnnotations()
	if annotations["board.gemini.google.com/plan"] != "" || annotations["board.gemini.google.com/plan-rejected-at"] == "" {
		t.Errorf("reject did not clear the draft: %v", annotations)
	}
}

// Onboarding suggestions: involvement-searched repos rank first by
// frequency, then activity events, then the member's own non-fork repos;
// the member's copy of an already-suggested upstream is dropped as a fork.
func TestGetRepoSuggestions(t *testing.T) {
	prevNow := suggestionNow
	suggestionNow = func() time.Time { return time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) }
	defer func() { suggestionNow = prevNow }()

	ghResponses := map[string]string{
		"https://api.github.com/search/issues?per_page=100&q=involves%3Aalice+updated%3A%3E2026-06-18": `{
			"total_count": 4, "items": [
				{"repository_url": "https://api.github.com/repos/org/main-repo"},
				{"repository_url": "https://api.github.com/repos/other/side-repo"},
				{"repository_url": "https://api.github.com/repos/org/main-repo"},
				{"repository_url": "https://api.github.com/repos/alice/main-repo"}
			]
		}`,
		"https://api.github.com/users/alice/events?per_page=100": `[
			{"repo": {"name": "org/evented-repo"}},
			{"repo": {"name": "org/main-repo"}}
		]`,
		"https://api.github.com/user/repos?affiliation=owner&per_page=30&sort=pushed": `[
			{"full_name": "alice/own-repo", "fork": false},
			{"full_name": "alice/forked-repo", "fork": true}
		]`,
	}
	_, r, _ := boardTestServer(t, ghResponses, boardCR())

	req, _ := http.NewRequest("GET", "/repo-suggestions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []repoSuggestion
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	// alice/main-repo is dropped (fork of the suggested org/main-repo);
	// alice/forked-repo is dropped (fork flag).
	want := []string{"org/main-repo", "other/side-repo", "org/evented-repo", "alice/own-repo"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %+v", want, got)
	}
	for i, name := range want {
		if got[i].FullName != name || got[i].URL != "https://github.com/"+name {
			t.Errorf("suggestion %d: got %+v, want %s", i, got[i], name)
		}
	}
}

// Clicks beyond the launch limits render "queued", not "starting": with
// two active sandboxes (the per-user default), a third review click has
// no capacity until a slot frees.
func TestMailboxQueuedAtCapacity(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 90, "title": "reviewing a", "html_url": "https://github.com/test/repo/pull/90", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]},
			{"number": 91, "title": "reviewing b", "html_url": "https://github.com/test/repo/pull/91", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]},
			{"number": 92, "title": "third click", "html_url": "https://github.com/test/repo/pull/92", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	running := func(n string, pr string) *unstructured.Unstructured {
		return sandboxCR(n,
			map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": pr},
			map[string]interface{}{"sandbox.gemini.google.com/last-task-state": "Running", "htmlURL": "https://github.com/test/repo/pull/" + pr}, 1)
	}
	board := boardCR()
	board.SetAnnotations(map[string]string{"board.gemini.google.com/requests": `{"review-92": "alice"}`})
	// Single limit knob: capacity for this test is the board's maxActive.
	_ = unstructured.SetNestedField(board.Object, int64(2), "spec", "limits", "maxActive")

	_, r, _ := boardTestServer(t, ghResponses, board, running("factory-pr-repo-90", "90"), running("factory-pr-repo-91", "91"))

	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	for _, item := range work {
		if item.Number == 92 {
			if item.Stage != "queued" || item.Attention != "waiting" {
				t.Errorf("pr-92 should be queued/waiting, got %s/%s", item.Stage, item.Attention)
			}
			return
		}
	}
	t.Fatalf("pr-92 missing: %s", w.Body.String())
}

// spec.view.labels narrows the feed server-side (view only) — the UI can
// only narrow further, never widen. In-flight items always surface.
func TestBoardViewLabelFilter(t *testing.T) {
	board := boardCR()
	_ = unstructured.SetNestedStringSlice(board.Object, []string{"area/net"}, "spec", "view", "labels")

	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 60, "title": "in scope", "html_url": "https://github.com/test/repo/issues/60", "updated_at": "2026-09-16T10:00:00Z", "labels": [{"name": "area/net"}]},
			{"number": 61, "title": "out of scope", "html_url": "https://github.com/test/repo/issues/61", "updated_at": "2026-09-16T10:00:00Z"},
			{"number": 62, "title": "out of scope but fixing", "html_url": "https://github.com/test/repo/issues/62", "updated_at": "2026-09-16T10:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 70, "title": "scoped pr", "html_url": "https://github.com/test/repo/pull/70", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}, "labels": [{"name": "area/net"}]},
			{"number": 71, "title": "unscoped pr", "html_url": "https://github.com/test/repo/pull/71", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}}
		]`,
	}
	fixSandbox := sandboxCR("fix-repo-62",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"sandbox.gemini.google.com/last-task-state": "Running", "htmlURL": "https://github.com/test/repo/issues/62"}, 1)

	_, r, _ := boardTestServer(t, ghResponses, board, fixSandbox)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	got := map[int]bool{}
	for _, item := range work {
		got[item.Number] = true
	}
	for _, want := range []int{60, 62, 70} {
		if !got[want] {
			t.Errorf("expected #%d in the filtered view: %s", want, w.Body.String())
		}
	}
	for _, hidden := range []int{61, 71} {
		if got[hidden] {
			t.Errorf("#%d should be hidden by spec.view.labels: %s", hidden, w.Body.String())
		}
	}
}

// Rejecting a triage clears the breadcrumbs, tombstones the sandbox, and
// the row returns to its resting stage with no sandbox chip — Triage /
// Plan / Fix are available again.
func TestRejectBoardTriage(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "drafted", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
	triageSandbox := sandboxCR("triage-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"agentDraft":                                "triage:\n  labels: [bug]",
			"agentDraftType":                            "triage",
			"board.gemini.google.com/triaged-at":        "2026-09-17T00:00:00Z",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"htmlURL": "https://github.com/test/repo/issues/20",
		}, 1)

	_, r, dyn := boardTestServer(t, ghResponses, boardCR(), triageSandbox)

	req, _ := http.NewRequest("POST", "/board/myboard/issues/20/triage-reject", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), "triage-repo-20", v1.GetOptions{})
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	annotations := sb.GetAnnotations()
	if annotations["agentDraft"] != "" || annotations["board.gemini.google.com/triaged-at"] != "" {
		t.Errorf("breadcrumbs not cleared: %v", annotations)
	}
	if annotations["board.gemini.google.com/triage-rejected-at"] == "" {
		t.Error("tombstone missing")
	}

	// The feed shows a fully reset row: untriaged, no sandbox chip.
	req, _ = http.NewRequest("GET", "/board/myboard/work", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 20 {
			if item.Stage != "untriaged" || item.Sandbox != nil || item.Draft != "" {
				t.Errorf("row not reset: %+v", item)
			}
			return
		}
	}
	t.Fatalf("issue-20 missing: %s", w.Body.String())
}

// A pre-task launch failure (sandbox-ready timeout) stamps the review
// error but never a task state: the row must read Review failed with a
// Retry path, not sit on "starting" forever.
func TestPrelaunchFailureRendersFailed(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 55, "title": "stuck", "html_url": "https://github.com/test/repo/pull/55", "updated_at": "` + fresh + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	stuck := sandboxCR("factory-pr-repo-55",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "55"},
		map[string]interface{}{
			"htmlURL":                           "https://github.com/test/repo/pull/55",
			"review.gemini.google.com/error":    "connecting to sandbox: timed out waiting for sandbox pod",
			"review.gemini.google.com/error-at": fresh,
		}, 1)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), stuck)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 55 {
			if item.Stage != "review-failed" || item.Error == "" {
				t.Errorf("expected review-failed with reason, got %+v", item)
			}
			return
		}
	}
	t.Fatalf("pr-55 missing: %s", w.Body.String())
}

// Within needs-you, finished agent work awaiting a verdict outranks a
// bare review request: Up Next shows what is ready for you before what
// merely asks of you.
func TestUpNextDefersBareReviewRequests(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "drafted", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "` + fresh + `"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 42, "title": "asks of you", "html_url": "https://github.com/test/repo/pull/42", "updated_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
			 "user": {"login": "carol"}, "requested_reviewers": [{"login": "alice"}]}
		]`,
	}
	triageSandbox := sandboxCR("triage-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{"agentDraft": "triage:\n  labels: [bug]", "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), triageSandbox)
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	if len(work) < 2 {
		t.Fatalf("expected 2 rows: %s", w.Body.String())
	}
	// Both are needs-you, and the PR is newer — yet the triage draft
	// (ready for a verdict) must come first.
	if work[0].Number != 20 || work[1].Number != 42 {
		t.Errorf("expected triage-ready before review-requested, got %d then %d", work[0].Number, work[1].Number)
	}
}

func TestSplitTaskName(t *testing.T) {
	for _, tc := range []struct{ name, wantType, wantTS string }{
		{"review-20260918-192435", "review", "2026-09-18T19:24:35Z"},
		{"plan-20260918-080208", "plan", "2026-09-18T08:02:08Z"},
		{"agent-my-planner-20260918-010203", "agent-my-planner", "2026-09-18T01:02:03Z"},
		{"weird", "weird", ""},
	} {
		gotType, gotTS := splitTaskName(tc.name)
		if gotType != tc.wantType || gotTS != tc.wantTS {
			t.Errorf("splitTaskName(%q) = %q,%q want %q,%q", tc.name, gotType, gotTS, tc.wantType, tc.wantTS)
		}
	}
}

// Paused sandboxes have no pod: the card says so and offers Wake instead
// of erroring.
func TestSandboxCardPaused(t *testing.T) {
	paused := sandboxCR("fix-repo-9",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"sandbox.gemini.google.com/last-task-type":  "fix",
			"htmlURL": "https://github.com/test/repo/issues/9",
		}, 0)
	server, r, _ := boardTestServer(t, map[string]string{}, boardCR(), paused)
	r.GET("/sandbox-card/:name", server.getSandboxCard)

	req, _ := http.NewRequest("GET", "/sandbox-card/fix-repo-9", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var card struct {
		Paused    bool   `json:"paused"`
		TaskState string `json:"taskState"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if !card.Paused || card.TaskState != "Completed" {
		t.Errorf("expected paused Completed card, got %s", w.Body.String())
	}
}

// A submitted review is rediscovered from GitHub itself: with no sandbox
// breadcrumb (clean slate), the row still reads Reviewed ✓ instead of a
// blank resting state.
func TestGetBoardWorkRediscoversSubmittedReview(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 43, "title": "already reviewed", "html_url": "https://github.com/test/repo/pull/43", "updated_at": "2026-09-16T12:00:00Z",
			 "user": {"login": "carol"}}
		]`,
		"https://api.github.com/repos/test/repo/pulls/43/reviews?per_page=100": `[
			{"id": 8, "state": "APPROVED", "user": {"login": "alice"}}
		]`,
	}
	_, r, _ := boardTestServer(t, ghResponses, boardCR())
	req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var work []models.WorkItem
	_ = json.Unmarshal(w.Body.Bytes(), &work)
	for _, item := range work {
		if item.Number == 43 {
			if item.Stage != "review-submitted" {
				t.Errorf("expected rediscovered review-submitted, got %+v", item)
			}
			return
		}
	}
	t.Fatalf("pr-43 missing: %s", w.Body.String())
}
