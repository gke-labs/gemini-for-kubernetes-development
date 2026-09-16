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
	return server, r, dynamicClient
}

func boardCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "board.gemini.google.com/v1alpha1",
		"kind":       "RepoBoard",
		"metadata":   map[string]interface{}{"name": "myboard", "namespace": "alice"},
		"spec": map[string]interface{}{
			"repoURL":  "https://github.com/test/repo",
			"access":   map[string]interface{}{"mode": "list", "allow": []interface{}{"alice"}},
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
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": `[
			{"number": 11, "title": "bobs labeled", "html_url": "https://github.com/test/repo/issues/11", "updated_at": "2026-09-16T11:00:00Z",
			 "labels": [{"name": "agent"}], "assignees": [{"login": "bob"}]}
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
		map[string]interface{}{"agentDraft": "review:\n  body: hi", "htmlURL": "https://github.com/test/repo/pull/42"}, 1)

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
	if len(work) != 3 {
		t.Fatalf("expected 3 rows, got %d: %s", len(work), w.Body.String())
	}

	byKey := map[string]models.WorkItem{}
	for _, item := range work {
		byKey[item.Type+"-"+itoa(item.Number)] = item
	}

	if row := byKey["issue-10"]; row.Stage != "fixing" || row.Attention != "working" || row.Sandbox == nil || row.Sandbox.Name != "fix-repo-10" {
		t.Errorf("issue-10 row wrong: %+v", row)
	}
	if row := byKey["issue-11"]; row.Stage != "awaiting-go" || row.Attention != "needs-you" || row.ClaimedBy != "bob" {
		t.Errorf("issue-11 row wrong: %+v", row)
	}
	if row := byKey["pr-42"]; row.Stage != "review-ready" || row.Attention != "needs-you" {
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
	board := boardCR()
	_ = unstructured.SetNestedStringSlice(board.Object, []string{"someoneelse"}, "spec", "access", "allow")
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
