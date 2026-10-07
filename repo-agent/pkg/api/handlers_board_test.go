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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type boardMockRT struct {
	mu        sync.Mutex
	responses map[string]string
	// writes records every non-GET as "METHOD path", in order, so a
	// test can assert what a handler actually changed on the fork —
	// deleting the wrong file and deleting nothing look the same from
	// the response alone.
	writes []string
}

func (m *boardMockRT) RoundTrip(req *http.Request) (*http.Response, error) {
	// The feed is one GraphQL request now, but tests describe the world in
	// REST fixtures — that is the readable form and it is what every board
	// test here is already written in. So the fake assembles the answer out
	// of those same fixtures. The GraphQL keys below are spelled out as
	// literals rather than marshalled through the decoder's own structs, so
	// a wrong json tag on either side fails a test instead of cancelling
	// itself out.
	if body, ok := m.graphqlAnswer(req); ok {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    req,
		}, nil
	}
	if req.Method != http.MethodGet {
		m.mu.Lock()
		m.writes = append(m.writes, req.Method+" "+req.URL.Path)
		m.mu.Unlock()
	}
	// A method-qualified key wins, so a test can answer a DELETE to a
	// path it also serves a GET for.
	body, ok := m.responses[req.Method+" "+req.URL.String()]
	if !ok {
		body, ok = m.responses[req.URL.String()]
	}
	status := http.StatusOK
	if !ok {
		// An unregistered read is a 404 — that is how tests say a path
		// does not exist. An unregistered write succeeds: what it did
		// is in writes, and making every test spell out a response for
		// each file it expects deleted would only restate the
		// assertion.
		body, status = `{}`, http.StatusNotFound
		if req.Method != http.MethodGet {
			status = http.StatusOK
		}
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// fixture returns the body registered for a REST URL, or an empty list.
func (m *boardMockRT) fixture(url string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if body, ok := m.responses[url]; ok {
		return body
	}
	return "[]"
}

func (m *boardMockRT) graphqlAnswer(req *http.Request) (string, bool) {
	if !strings.HasSuffix(req.URL.Path, "/graphql") {
		return "", false
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return "", false
	}
	var q struct {
		Variables struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
			Me    string `json:"me"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		return "", false
	}
	base := "https://api.github.com/repos/" + q.Variables.Owner + "/" + q.Variables.Name
	const listing = "direction=desc&per_page=100&sort=updated&state=open"

	issueNodes := func(query string) []any {
		var rest []map[string]any
		_ = json.Unmarshal([]byte(m.fixture(base+query)), &rest)
		out := []any{}
		for _, it := range rest {
			// repository.issues never returns pull requests; the REST
			// listing did, which is why the feed had to skip them.
			if _, isPR := it["pull_request"]; isPR {
				continue
			}
			out = append(out, map[string]any{
				"number":    it["number"],
				"title":     it["title"],
				"body":      it["body"],
				"url":       it["html_url"],
				"updatedAt": it["updated_at"],
				"author":    gqlLoginObj(it["user"]),
				"labels":    map[string]any{"nodes": gqlNameList(it["labels"])},
				"assignees": map[string]any{"nodes": gqlLoginList(it["assignees"])},
			})
		}
		return out
	}

	var restPRs []map[string]any
	_ = json.Unmarshal([]byte(m.fixture(base+"/pulls?"+listing)), &restPRs)
	prNodes := []any{}
	for _, pr := range restPRs {
		num, _ := pr["number"].(float64)
		var restReviews []map[string]any
		_ = json.Unmarshal([]byte(m.fixture(base+"/pulls/"+strconv.Itoa(int(num))+"/reviews?per_page=100")), &restReviews)
		// The query asks for reviews(author:$me), so only the member's own
		// come back — the login filter lives in GitHub, not in the caller.
		mine := []any{}
		for _, rv := range restReviews {
			if user, _ := rv["user"].(map[string]any); user != nil {
				if login, _ := user["login"].(string); strings.EqualFold(login, q.Variables.Me) {
					mine = append(mine, map[string]any{"state": rv["state"]})
				}
			}
		}
		requested := []any{}
		for _, r := range gqlSlice(pr["requested_reviewers"]) {
			requested = append(requested, map[string]any{"requestedReviewer": gqlLoginObj(r)})
		}
		prNodes = append(prNodes, map[string]any{
			"id":             pr["node_id"],
			"number":         pr["number"],
			"title":          pr["title"],
			"body":           pr["body"],
			"url":            pr["html_url"],
			"updatedAt":      pr["updated_at"],
			"isDraft":        pr["draft"],
			"author":         gqlLoginObj(pr["user"]),
			"headRepository": gqlHeadRepo(pr["head"]),
			"labels":         map[string]any{"nodes": gqlNameList(pr["labels"])},
			"reviewRequests": map[string]any{"nodes": requested},
			"reviews":        map[string]any{"nodes": mine},
		})
	}

	answer := map[string]any{"data": map[string]any{
		"rateLimit": map[string]any{"cost": 9, "remaining": 4991, "resetAt": time.Now().Add(time.Hour).Format(time.RFC3339)},
		"repository": map[string]any{
			"pullRequests": map[string]any{"nodes": prNodes},
			"assigned":     map[string]any{"nodes": issueNodes("/issues?assignee=" + q.Variables.Me + "&" + listing)},
			"created":      map[string]any{"nodes": issueNodes("/issues?creator=" + q.Variables.Me + "&" + listing)},
			"triage":       map[string]any{"nodes": issueNodes("/issues?" + listing)},
		},
	}}
	out, err := json.Marshal(answer)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// gqlHeadRepo is nil when the fixture names no head repository, as GitHub
// reports a deleted fork.
func gqlHeadRepo(v any) any {
	head, _ := v.(map[string]any)
	repo, _ := head["repo"].(map[string]any)
	if repo == nil {
		return nil
	}
	return map[string]any{"nameWithOwner": repo["full_name"], "isFork": repo["fork"], "owner": gqlLoginObj(repo["owner"])}
}

func gqlSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// gqlLoginObj is nil for a missing user, which is how GitHub reports a
// ghost account — and the decoder has to survive it.
func gqlLoginObj(v any) any {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	return map[string]any{"login": m["login"]}
}

func gqlLoginList(v any) []any {
	out := []any{}
	for _, e := range gqlSlice(v) {
		out = append(out, gqlLoginObj(e))
	}
	return out
}

func gqlNameList(v any) []any {
	out := []any{}
	for _, e := range gqlSlice(v) {
		m, _ := e.(map[string]any)
		out = append(out, map[string]any{"name": m["name"]})
	}
	return out
}

func boardTestServer(t *testing.T, ghResponses map[string]string, objs ...runtime.Object) (*Server, *gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	server, r, dyn, _ := boardTestServerWithRT(t, ghResponses, objs...)
	return server, r, dyn
}

// boardTestServerWithRT hands back the fake GitHub as well, for tests
// about what a handler writes rather than what it returns.
func boardTestServerWithRT(t *testing.T, ghResponses map[string]string, objs ...runtime.Object) (*Server, *gin.Engine, *fake.FakeDynamicClient, *boardMockRT) {
	t.Helper()
	// Package-global caches; drop verdicts from earlier tests.
	workFeedCache.Lock()
	workFeedCache.entries = map[string]workFeedEntry{}
	workFeedCache.refreshing = map[string]bool{}
	workFeedCache.Unlock()
	repoSuggestionCache.Lock()
	repoSuggestionCache.entries = map[string]repoSuggestionEntry{}
	repoSuggestionCache.Unlock()
	repoPermCache.Lock()
	repoPermCache.entries = map[string]repoPermEntry{}
	repoPermCache.Unlock()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrSandbox:   "SandboxList",
		repoBoardGVR: "RepoBoardList",
		requestGVR:   "RequestList",
	})
	generateNames(dynamicClient)
	// Seed via explicit Create: the fake's kind->resource guesser pluralizes
	// Sandbox as "sandboxs", so initial-object seeding lands in the wrong
	// resource.
	for _, o := range objs {
		u := o.(*unstructured.Unstructured)
		gvr := gvrSandbox
		switch u.GetKind() {
		case "RepoBoard":
			gvr = repoBoardGVR
		case "Request":
			gvr = requestGVR
		}
		if _, err := dynamicClient.Resource(gvr).Namespace(u.GetNamespace()).Create(context.Background(), u, v1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", u.GetName(), err)
		}
	}
	k8sClient := kubernetesfake.NewClientset(&corev1.Secret{
		ObjectMeta: v1.ObjectMeta{Name: "github-pat", Namespace: "alice"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_alice")},
	})

	rt := &boardMockRT{responses: ghResponses}
	prev := githubClientForToken
	githubClientForToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: rt})
	}
	prevHTTP := githubHTTPForToken
	githubHTTPForToken = func(_ string) *http.Client {
		return &http.Client{Transport: rt}
	}
	t.Cleanup(func() {
		githubClientForToken = prev
		githubHTTPForToken = prevHTTP
	})

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
	r.POST("/board/:board/prs/:id/abandon", server.abandonBoardReview)
	r.POST("/board/:board/issues/:id/rerun", server.rerunBoardIssue)
	r.POST("/board/:board/research", server.startResearchSession)
	r.GET("/board/:board/research/prompts", server.getResearchPrompts)
	r.POST("/board/:board/issues/:id/plan", server.kickoffPlan)
	r.POST("/board/:board/issues/:id/plan-feedback", server.planBoardFeedback)
	r.POST("/board/:board/issues/:id/plan-approve", server.planBoardApprove)
	r.POST("/board/:board/issues/:id/plan-reject", server.planBoardReject)
	r.POST("/board/:board/issues/:id/triage-reject", server.rejectBoardTriage)
	r.PUT("/board/:board/issues/:id/plan-draft", server.putBoardPlanDraft)
	r.PUT("/board/:board/issues/:id/draft", server.putBoardTriageDraft)
	r.POST("/board/:board/issues/:id/actions/:verb", server.boardIssueAction)
	r.GET("/boards", server.getBoards)
	r.POST("/boards", server.createBoard)
	r.DELETE("/board/:board", server.deleteBoard)
	r.GET("/board/:board/runbook", server.getBoardRunbooks)
	r.POST("/board/:board/runbook", server.kickoffRunbook)
	r.DELETE("/board/:board/runbook/instance/:instance", server.removeRunbookInstance)
	return server, r, dynamicClient, rt
}

// generateNames makes the fake client mint names from generateName the
// way the API server does. Clicks are filed with a prefix and no name,
// so without this two of them collide on the empty string — and every
// handler that files one would look broken the second time it is used.
func generateNames(dyn *fake.FakeDynamicClient) {
	dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := create.GetObject().(*unstructured.Unstructured)
		if !ok || obj.GetName() != "" || obj.GetGenerateName() == "" {
			return false, nil, nil
		}
		obj.SetName(obj.GetGenerateName() + utilrand.String(5))
		// Not handled: the object is mutated in place and the tracker
		// stores it.
		return false, nil, nil
	})
}

// requestCR is a standing click on the fixture board, seeded the way
// the API files one.
func requestCR(spec boardv1alpha1.RequestSpec) *unstructured.Unstructured {
	spec.Board = "myboard"
	if spec.Member == "" {
		spec.Member = "alice"
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&boardv1alpha1.Request{
		ObjectMeta: v1.ObjectMeta{
			Name:              strings.ReplaceAll(spec.Key(), "/", "-"),
			Namespace:         "alice",
			CreationTimestamp: v1.Now(),
			Labels: map[string]string{
				boardv1alpha1.LabelBoard: "myboard",
				boardv1alpha1.LabelVerb:  spec.Verb,
			},
		},
		Spec: spec,
	})
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetAPIVersion("board.gemini.google.com/v1alpha1")
	u.SetKind("Request")
	return u
}

// filedRequests reads back the clicks the handlers filed, decoded the
// way the controller will read them. The click used to be a key in a
// JSON map on the board, which is why so many tests once asserted on
// substrings of an annotation.
func filedRequests(t *testing.T, dyn *fake.FakeDynamicClient, namespace string) []boardv1alpha1.Request {
	t.Helper()
	list, err := dyn.Resource(requestGVR).Namespace(namespace).List(context.Background(), v1.ListOptions{})
	if err != nil {
		t.Fatalf("listing requests: %v", err)
	}
	out := make([]boardv1alpha1.Request, 0, len(list.Items))
	for i := range list.Items {
		var req boardv1alpha1.Request
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &req); err != nil {
			t.Fatalf("decoding request %s: %v", list.Items[i].GetName(), err)
		}
		out = append(out, req)
	}
	return out
}

// theRequest is filedRequests for the common case: exactly one click.
func theRequest(t *testing.T, dyn *fake.FakeDynamicClient, namespace string) boardv1alpha1.Request {
	t.Helper()
	reqs := filedRequests(t, dyn, namespace)
	if len(reqs) != 1 {
		t.Fatalf("filed %d requests, want exactly one: %+v", len(reqs), reqs)
	}
	return reqs[0]
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
	reviewSandbox := sandboxCR("review-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "sandbox.gemini.google.com/type": "recipe"},
		map[string]interface{}{"reviewState": "pending", "htmlURL": "https://github.com/test/repo/pull/42",
			factorycli.AnnotationReviewRun: `{"name":"review/myboard/42/1","task":"recipe-review-1","startedAt":"2026-09-16T12:00:00Z"}`}, 1)

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
	// The review's session, for Continue session and Update review.
	if rs := byKey["pr-42"].ReviewSession; rs == nil || rs.Sandbox != "review-repo-42" || rs.Task != "recipe-review-1" {
		t.Errorf("pr-42 review session wrong: %+v", rs)
	}

	// needs-you rows sort before working rows.
	if work[len(work)-1].Attention != "working" {
		t.Errorf("expected the working row last, got %+v", work)
	}
}

// The PR a fix opened carries the fix's session, and names what its
// sandbox is doing from the recorded run: a revise the board filed by its
// id, one the watch ran (no run name) as a follow-up, the fix itself as
// fixing.
func TestPRRowFromTheFixRun(t *testing.T) {
	for _, tc := range []struct{ run, state, stage string }{
		{`{"name":"revise/myboard/fix-repo-7/address-comments/2","task":"recipe-fix-2","session":"recipe-fix-1","startedAt":"2026-09-16T12:00:00Z"}`, "Running", "addressing"},
		{`{"name":"revise/myboard/fix-repo-7/fix-ci/2","task":"recipe-fix-2","session":"recipe-fix-1","startedAt":"2026-09-16T12:00:00Z"}`, "Failed", "investigating-failed"},
		{`{"task":"recipe-fix-2","session":"recipe-fix-1","startedAt":"2026-09-16T12:00:00Z"}`, "Running", "iterating"},
		{`{"name":"fix/myboard/7/1","task":"recipe-fix-1","startedAt":"2026-09-16T12:00:00Z"}`, "Running", "fixing"},
		{`{"name":"revise/myboard/fix-repo-7/iterate/2","task":"recipe-fix-2","session":"recipe-fix-1","startedAt":"2026-09-16T12:00:00Z"}`, "Completed", "open"},
	} {
		ghResponses := map[string]string{
			"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[
				{"number": 9, "title": "the fix", "html_url": "https://github.com/test/repo/pull/9", "updated_at": "2026-09-16T12:00:00Z",
				 "user": {"login": "alice"}, "body": "Fixes #7"}
			]`,
		}
		sb := sandboxCR("fix-repo-7",
			map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "9", factorycli.LabelIssue: "7"},
			map[string]interface{}{
				"repo": "repo", "htmlURL": "https://github.com/test/repo/pull/9",
				"sandbox.gemini.google.com/last-task-type":  "fix",
				"sandbox.gemini.google.com/last-task-state": tc.state,
				factorycli.AnnotationFixRun:                 tc.run,
				annoFixError:                                "push: the branch moved",
			}, 1)
		_, r, _ := boardTestServer(t, ghResponses, boardCR(), sb)
		req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var work []models.WorkItem
		if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
			t.Fatalf("bad json: %v: %s", err, w.Body.String())
		}
		var row *models.WorkItem
		for i := range work {
			if work[i].Type == "pr" && work[i].Number == 9 {
				row = &work[i]
			}
		}
		if row == nil {
			t.Fatalf("%s: no PR row: %s", tc.run, w.Body.String())
		}
		if row.Stage != tc.stage {
			t.Errorf("%s %s: stage %q, want %q", tc.run, tc.state, row.Stage, tc.stage)
		}
		if fs := row.FixSession; fs == nil || fs.Sandbox != "fix-repo-7" || fs.Task != "recipe-fix-1" {
			t.Errorf("%s: fix session %+v, want the fix's", tc.run, fs)
		}
		if failed := strings.HasSuffix(tc.stage, "-failed"); failed != (row.Error != "") {
			t.Errorf("%s: error %q on stage %s", tc.run, row.Error, tc.stage)
		}
	}
}

func TestKickoffFixFilesARequest(t *testing.T) {
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

	filed := theRequest(t, dyn, "alice")
	if filed.Spec.Verb != boardv1alpha1.VerbFix || filed.Spec.Number != 77 || filed.Spec.Member != "alice" {
		t.Errorf("filed %+v, want alice's fix on 77", filed.Spec)
	}
	// The controller lists by these, so a click that carries neither is
	// a click no board ever sees.
	if filed.Labels[boardv1alpha1.LabelBoard] != "myboard" || filed.Labels[boardv1alpha1.LabelVerb] != boardv1alpha1.VerbFix {
		t.Errorf("labels = %v, want the board and the verb", filed.Labels)
	}
	// Owned by the board: deleting the board takes its clicks with it.
	if len(filed.OwnerReferences) != 1 || filed.OwnerReferences[0].Name != "myboard" {
		t.Fatalf("ownerReferences = %+v, want the board", filed.OwnerReferences)
	}
	// And owned as the CONTROLLER, because that is the only reference
	// Owns() enqueues on. A plain owner still gets garbage-collected,
	// but the click sits there until the next resync — which is the
	// wait the mailbox never had.
	if ctrl := filed.OwnerReferences[0].Controller; ctrl == nil || !*ctrl {
		t.Error("the board owns the click but does not control it, so nothing wakes the reconciler")
	}
}

// A second click on the same thing is the same click. The map the
// mailbox was collapsed them for free; objects do not, so the handler
// looks for a standing one first — otherwise an impatient double-click
// is two sandboxes.
func TestKickoffFixTwiceIsOneRequest(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{
		"https://api.github.com/repos/test/repo/issues/77/assignees": `{}`,
		"https://api.github.com/repos/test/repo/issues/77/labels":    `[]`,
	}, boardCR())

	fix := func(issue string) {
		t.Helper()
		req, _ := http.NewRequest("POST", "/board/myboard/issues/"+issue+"/fix", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("issue %s: expected 200, got %d: %s", issue, w.Code, w.Body.String())
		}
	}
	fix("77")
	fix("77")
	first := theRequest(t, dyn, "alice")

	// ...and a click on something else is its own click, so the dedup is
	// on the subject and not on "has this board been clicked".
	fix("78")
	if filed := filedRequests(t, dyn, "alice"); len(filed) != 2 {
		t.Fatalf("filed %d requests, want one per issue: %+v", len(filed), filed)
	}

	// Once it is settled, the same click is a new one: a retry has to be
	// able to happen, it just must not happen by accident.
	settle(t, dyn, first, boardv1alpha1.RequestSucceeded)
	fix("77")
	if filed := filedRequests(t, dyn, "alice"); len(filed) != 3 {
		t.Errorf("a click after the last one finished must file a new request, got %d", len(filed))
	}
}

// settle marks a filed Request terminal, the way the controller does
// once the sandbox it asked for exists.
func settle(t *testing.T, dyn *fake.FakeDynamicClient, req boardv1alpha1.Request, phase string) {
	t.Helper()
	ctx := context.Background()
	obj, err := dyn.Resource(requestGVR).Namespace(req.Namespace).Get(ctx, req.Name, v1.GetOptions{})
	if err != nil {
		t.Fatalf("get request %s: %v", req.Name, err)
	}
	if err := unstructured.SetNestedField(obj.Object, phase, "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(requestGVR).Namespace(req.Namespace).Update(ctx, obj, v1.UpdateOptions{}); err != nil {
		t.Fatalf("settle request %s: %v", req.Name, err)
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
// favor of the PR row (which records the linkage); every PR lands in the
// PRs group, marked mine when authored and myPR when its head is on the
// member's fork.
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
			 "user": {"login": "alice"}, "draft": true, "body": "This change...\n\nFixes #10",
			 "head": {"repo": {"full_name": "alice/repo", "fork": true, "owner": {"login": "alice"}}},
			 "base": {"repo": {"full_name": "test/repo", "owner": {"login": "test"}}}},
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
	if pr50.Group != "prs" || !pr50.Mine || !pr50.MyPR || !pr50.DraftPR || len(pr50.Fixes) != 1 || pr50.Fixes[0] != 10 {
		t.Errorf("pr-50 row wrong: %+v", pr50)
	}
	if row := byKey["pr-42"]; row.Group != "prs" || row.Mine || row.MyPR {
		t.Errorf("pr-42 should be a PRs row that is not mine: %+v", row)
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
	if pr60.Group != "prs" || pr60.Mine || len(pr60.Fixes) != 1 || pr60.Fixes[0] != 13 {
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
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]"), "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

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
	if row := byKey["issue-20"]; row.Sandbox == nil || row.Sandbox.Name != "fix-repo-20" {
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
	submittedSandbox := sandboxCR("review-repo-90",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "sandbox.gemini.google.com/type": "recipe"},
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

// Between a click and the run: a standing Request renders the row as
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
	clicked := requestCR(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbReview, Number: 5})
	provisioning := sandboxCR("review-repo-6",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "sandbox.gemini.google.com/type": "recipe"},
		map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/6"}, 1)

	_, r, _ := boardTestServer(t, ghResponses, boardCR(), provisioning, clicked)
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
		t.Errorf("clicked PR: want review-starting/working, got %v", s)
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
		if item.Number == 300 && item.Group == "prs" && !item.Mine && !item.ReviewRequested {
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
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]"), "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

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

// A draft stored as a Triage task output is shown, edited and saved as the
// triage: block.
func TestTriageDraftFromTaskOutput(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "triaged", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
	taskOutput := "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Triage\ntarget:\n  url: https://github.com/test/repo/issues/20\nspec:\n  labels:\n    - bug\n  assessment: %s\n"
	stored := fmt.Sprintf(taskOutput, "from the agent")
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{factorycli.AnnotationTriageOutput: stored, "htmlURL": "https://github.com/test/repo/issues/20"}, 0)
	_, r, _ := boardTestServer(t, ghResponses, boardCR(), triageSandbox)

	draft := func() string {
		req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var work []models.WorkItem
		if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		for _, item := range work {
			if item.Type == "issue" && item.Number == 20 {
				return item.Draft
			}
		}
		t.Fatalf("issue-20 missing from feed: %s", w.Body.String())
		return ""
	}
	if got, want := draft(), "triage:\n  labels:\n    - bug\n  assessment: from the agent"; got != want {
		t.Errorf("shown draft = %q, want %q", got, want)
	}

	body, _ := json.Marshal(map[string]string{"draft": fmt.Sprintf(taskOutput, "human-refined")})
	req, _ := http.NewRequest("PUT", "/board/myboard/issues/20/draft", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("task-output edit: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := draft(); !strings.HasPrefix(got, "triage:\n") || !strings.Contains(got, "human-refined") {
		t.Errorf("saved draft = %q, want the triage: block with the edit", got)
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
			"board.gemini.google.com/planned-at":        "2026-09-17T00:00:00Z",
			factorycli.AnnotationPlanOutput:             storedOutput("Plan", "## Summary\nDo the thing."),
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
	if !factorycli.IsApplied(getAnnotations(), factorycli.AnnotationPlanApplied, "run") {
		t.Error("approval not stamped")
	}
	filed := theRequest(t, dyn, "alice")
	if filed.Spec.Verb != boardv1alpha1.VerbFix || filed.Spec.Number != 42 {
		t.Errorf("approve filed %+v, want a fix click on 42", filed.Spec)
	}

	// Reject: draft cleared, reject stamped.
	if w := post("plan-reject", `{}`); w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	annotations = getAnnotations()
	if annotations[factorycli.AnnotationPlanOutput] != "" || annotations["board.gemini.google.com/plan-rejected-at"] == "" {
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
func TestRequestQueuedAtCapacity(t *testing.T) {
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
	// Single limit knob: capacity for this test is the board's maxActive.
	_ = unstructured.SetNestedField(board.Object, int64(2), "spec", "limits", "maxActive")
	clicked := requestCR(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbReview, Number: 92})

	_, r, _ := boardTestServer(t, ghResponses, board, clicked,
		running("factory-pr-repo-90", "90"), running("factory-pr-repo-91", "91"))

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
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{
			factorycli.AnnotationTriageOutput:                    storedOutput("Triage", "triage:\n  labels: [bug]"),
			"board.gemini.google.com/triaged-at":                 "2026-09-17T00:00:00Z",
			"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
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

	sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), "fix-repo-20", v1.GetOptions{})
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	annotations := sb.GetAnnotations()
	if annotations[factorycli.AnnotationTriageOutput] != "" || annotations["board.gemini.google.com/triaged-at"] != "" {
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
	stuck := sandboxCR("review-repo-55",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "sandbox.gemini.google.com/type": "recipe"},
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
	triageSandbox := sandboxCR("fix-repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true"},
		map[string]interface{}{factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]"), "htmlURL": "https://github.com/test/repo/issues/20"}, 0)

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

// Your own fresh draft PR awaits your promote — a pending human act, so
// it lands in Up Next (needs-you). A fossil draft (deliberately parked
// WIP) decays to waiting; a non-draft own PR was never needs-you.
func TestOwnDraftPRNeedsYou(t *testing.T) {
	fresh := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	stale := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	prsJSON := `[
		{"number": 400, "title": "fresh draft", "html_url": "https://github.com/test/repo/pull/400", "updated_at": "` + fresh + `",
		 "user": {"login": "alice"}, "draft": true},
		{"number": 401, "title": "fossil draft", "html_url": "https://github.com/test/repo/pull/401", "updated_at": "` + stale + `",
		 "user": {"login": "alice"}, "draft": true},
		{"number": 402, "title": "ready PR", "html_url": "https://github.com/test/repo/pull/402", "updated_at": "` + fresh + `",
		 "user": {"login": "alice"}}
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
	want := map[int]string{400: "needs-you", 401: "waiting", 402: "waiting"}
	for _, item := range work {
		if expected, ok := want[item.Number]; ok {
			if item.Attention != expected {
				t.Errorf("PR #%d attention = %q, want %q", item.Number, item.Attention, expected)
			}
			delete(want, item.Number)
		}
	}
	if len(want) != 0 {
		t.Fatalf("PRs missing from feed: %v\n%s", want, w.Body.String())
	}
}

// The tab badge must agree with Up Next: when the feed cache holds the
// board, needsHuman is the feed's needs-you count, not the controller's
// older sandbox-only status heuristic.
func TestBoardBadgeCountsFeedAttention(t *testing.T) {
	_, r, _ := boardTestServer(t, map[string]string{}, boardCR())
	workFeedPut("alice/myboard", []models.WorkItem{
		{Number: 1, Attention: "needs-you"},
		{Number: 2, Attention: "needs-you"},
		{Number: 3, Attention: "waiting"},
	})
	defer invalidateWorkFeed("alice", "myboard")

	req, _ := http.NewRequest("GET", "/boards", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var boards []models.Board
	_ = json.Unmarshal(w.Body.Bytes(), &boards)
	if len(boards) != 1 || boards[0].NeedsHuman != 2 {
		t.Fatalf("badge = %+v, want needsHuman=2 from the cached feed", boards)
	}
}

// The cache has to outlast the poll that reads it. Work.js polls a board
// every 20s; any freshness window shorter than that puts every single
// poll past fresh, which serves the cache and kicks a rebuild behind it —
// dozens of GitHub calls per tick, for a cache that appears to be working.
func TestFeedStaysFreshLongerThanTheUIPolls(t *testing.T) {
	const uiPoll = 20 * time.Second
	if workFeedFreshFor <= uiPoll {
		t.Fatalf("workFeedFreshFor = %s, must exceed the %s UI poll or every poll rebuilds", workFeedFreshFor, uiPoll)
	}
}

// ageFeedEntry backdates a cached feed so staleness can be tested without
// waiting for it.
func ageFeedEntry(t *testing.T, key string, age time.Duration, blockedUntil time.Time) {
	t.Helper()
	workFeedCache.Lock()
	defer workFeedCache.Unlock()
	e := workFeedCache.entries[key]
	e.at = time.Now().Add(-age)
	e.blockedUntil = blockedUntil
	workFeedCache.entries[key] = e
}

// Out of budget, a rebuild cannot learn anything — it can only deepen the
// hole and, if it half-succeeds, replace a good board with an empty one.
// Serve what we have, at any age, until GitHub says the budget is back.
func TestABlockedBoardServesWhatItHasInsteadOfRebuilding(t *testing.T) {
	key := "alice/blocked"
	workFeedPut(key, []models.WorkItem{{Number: 1, Attention: "needs-you"}})
	defer invalidateWorkFeed("alice", "blocked")
	ageFeedEntry(t, key, 10*workFeedServeStaleFor, time.Now().Add(30*time.Minute))

	items, ok, needsRefresh := workFeedGet(key)
	if !ok || len(items) != 1 {
		t.Fatalf("a blocked board must keep serving its rows, got ok=%v items=%d", ok, len(items))
	}
	if needsRefresh {
		t.Fatal("a blocked board must not kick a rebuild it cannot pay for")
	}
	if _, ok := workFeedPeek(key); !ok {
		t.Fatal("the badge must count the same rows the board is showing")
	}
}

// And when the budget comes back, the board does too.
func TestTheBlockLiftsAndTheBoardRefreshesAgain(t *testing.T) {
	key := "alice/unblocked"
	workFeedPut(key, []models.WorkItem{{Number: 1}})
	defer invalidateWorkFeed("alice", "unblocked")
	ageFeedEntry(t, key, workFeedFreshFor+time.Second, time.Now().Add(-time.Minute))

	if _, ok, needsRefresh := workFeedGet(key); !ok || !needsRefresh {
		t.Fatalf("a stale board past its block must rebuild, got ok=%v needsRefresh=%v", ok, needsRefresh)
	}
}

// A write the controller is doing ends on the sandbox, which nothing here
// hears of: while one stands, the feed goes stale in seconds rather than a
// minute, or the done write keeps reading "posting" — and a second click
// gets a 409 for a button that should not have been there.
func TestAFeedWithAWriteStandingGoesStaleInSeconds(t *testing.T) {
	key := "alice/posting"
	workFeedPut(key, []models.WorkItem{{Number: 1, TriageActions: []models.WorkAction{
		{Verb: "comment", Reason: postingReason},
	}}})
	defer invalidateWorkFeed("alice", "posting")
	ageFeedEntry(t, key, workFeedPostingFreshFor+time.Second, time.Time{})
	if _, ok, needsRefresh := workFeedGet(key); !ok || !needsRefresh {
		t.Fatalf("a feed with a write standing must rebuild after %v, got ok=%v needsRefresh=%v", workFeedPostingFreshFor, ok, needsRefresh)
	}

	quiet := "alice/quiet"
	workFeedPut(quiet, []models.WorkItem{{Number: 1, PlanActions: []models.WorkAction{
		{Verb: "comment", Reason: "plan posted"},
	}}})
	defer invalidateWorkFeed("alice", "quiet")
	ageFeedEntry(t, quiet, workFeedPostingFreshFor+time.Second, time.Time{})
	if _, ok, needsRefresh := workFeedGet(quiet); !ok || needsRefresh {
		t.Fatalf("a feed with no write standing keeps its minute, got ok=%v needsRefresh=%v", ok, needsRefresh)
	}
}

// The click records the executor NAMESPACE — boardWriteContext's fifth
// return is the member token, and writing it into a CR was a live
// credential leak (iterate-1324 → ghp_…). The controller fetches the
// credential from that namespace itself, at launch time.
func TestKickoffRecordsTheMemberNotTheToken(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
	req, _ := http.NewRequest("POST", "/board/myboard/issues/1324/fix", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("kickoff = %d: %s", w.Code, w.Body.String())
	}
	filed := theRequest(t, dyn, "alice")
	if filed.Spec.Member != "alice" {
		t.Errorf("member = %q, want the namespace 'alice'", filed.Spec.Member)
	}
	// Nothing anywhere in the object, not just the one field that used
	// to hold it: the secret the fixture hands the handler is gho_alice.
	raw, _ := json.Marshal(filed)
	if strings.Contains(string(raw), "gho_") {
		t.Fatalf("the click carries a token: %s", raw)
	}
}

// The gear's engine choice survives for every engine factory runs;
// anything else (including empty) is gemini.
func TestEngineOrDefault(t *testing.T) {
	for in, want := range map[string]string{
		"":            "gemini",
		"gemini":      "gemini",
		"claude":      "claude",
		"antigravity": "antigravity",
		"agy":         "gemini",
	} {
		if got := engineOrDefault(in); got != want {
			t.Errorf("engineOrDefault(%q) = %q, want %q", in, got, want)
		}
	}
}

// factory's record of what ran wins; the controller's launch stamp is
// the fallback for sandboxes from an older factory, and the caller's
// default for ones nothing recorded.
func TestSandboxEngine(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{"first launch, stamped only by factory", map[string]string{annoTaskEngine: "antigravity"}, "antigravity"},
		{"factory's record beats the controller's", map[string]string{annoTaskEngine: "claude", "board.gemini.google.com/engine": "gemini"}, "claude"},
		{"older factory, controller stamp", map[string]string{"board.gemini.google.com/engine": "claude"}, "claude"},
		{"nothing recorded", nil, "gemini"},
	}
	for _, c := range cases {
		if got := sandboxEngine(c.annotations, "gemini"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Triage in the issue's sandbox: the row reads the draft and state from
// triage's own keys, never the review's agentDraft or the fix's state, and
// editing and rejecting touch only those keys.
func TestTriageInIssueSandbox(t *testing.T) {
	ghResponses := map[string]string{
		"https://api.github.com/repos/test/repo/issues?assignee=alice&direction=desc&per_page=100&sort=updated&state=open": `[]`,
		"https://api.github.com/repos/test/repo/issues?creator=alice&direction=desc&per_page=100&sort=updated&state=open":  `[]`,
		"https://api.github.com/repos/test/repo/issues?direction=desc&per_page=100&sort=updated&state=open": `[
			{"number": 20, "title": "triaged", "html_url": "https://github.com/test/repo/issues/20", "updated_at": "2026-09-16T09:00:00Z"}
		]`,
		"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=updated&state=open": `[]`,
	}
	sb := sandboxCR("repo-20",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", factorycli.LabelIssue: "20"},
		map[string]interface{}{
			"repo":                            "repo",
			"htmlURL":                         "https://github.com/test/repo/issues/20",
			"agentDraft":                      "not a triage",
			factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]"),
			factorycli.AnnotationRecipeTriageTaskState: "Completed",
		}, 1)
	_, r, dyn := boardTestServer(t, ghResponses, boardCR(), sb)

	row := func() *models.WorkItem {
		t.Helper()
		req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var work []models.WorkItem
		if err := json.Unmarshal(w.Body.Bytes(), &work); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		for i := range work {
			if work[i].Type == "issue" && work[i].Number == 20 {
				return &work[i]
			}
		}
		t.Fatalf("issue-20 missing from feed: %s", w.Body.String())
		return nil
	}
	if got := row(); got.Stage != "triage-ready" || !strings.Contains(got.Draft, "labels:\n    - bug") ||
		got.Sandbox == nil || got.Sandbox.Name != "repo-20" || got.Sandbox.TaskState != "Completed" {
		t.Errorf("row = %+v sandbox %+v", got, got.Sandbox)
	}

	body, _ := json.Marshal(map[string]string{"draft": "triage:\n  labels: [bug, p1]"})
	req, _ := http.NewRequest("PUT", "/board/myboard/issues/20/draft", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	stored := func() map[string]string {
		got, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), "repo-20", v1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return got.GetAnnotations()
	}
	if a := stored(); !strings.Contains(a[factorycli.AnnotationTriageOutput], "p1") || a["agentDraft"] != "not a triage" {
		t.Errorf("after edit: %v", a)
	}

	req, _ = http.NewRequest("POST", "/board/myboard/issues/20/triage-reject", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if a := stored(); a[factorycli.AnnotationTriageOutput] != "" || a["agentDraft"] != "not a triage" || a["board.gemini.google.com/triage-rejected-at"] == "" {
		t.Errorf("after reject: %v", a)
	}
}

// The board's review lives in the PR's review sandbox: a Review click
// re-arms it and Abandon parks it there, never in the PR's fix or
// follow-up sandbox.
func TestReviewClicksFindTheReviewSandbox(t *testing.T) {
	review := sandboxCR("review-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "sandbox.gemini.google.com/type": "recipe"},
		map[string]interface{}{"repo": "repo", "reviewState": "pending", "htmlURL": "https://github.com/test/repo/pull/42"}, 1)
	followUp := sandboxCR("factory-pr-repo-42",
		map[string]interface{}{"factory.gemini.google.com/managed": "true", "factory.gemini.google.com/pr": "42"},
		map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/42"}, 1)
	_, r, dyn := boardTestServer(t, map[string]string{
		"https://api.github.com/repos/test/repo/pulls/42/requested_reviewers":  `{}`,
		"https://api.github.com/repos/test/repo/pulls/42/reviews?per_page=100": `[]`,
	}, boardCR(), review, followUp)
	annotations := func(name string) map[string]string {
		t.Helper()
		got, err := dyn.Resource(schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}).
			Namespace("alice").Get(context.Background(), name, v1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		return got.GetAnnotations()
	}

	req, _ := http.NewRequest("POST", "/board/myboard/prs/42/review", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("review: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if annotations("review-repo-42")[annoRereviewRequest] == "" {
		t.Error("the Review click did not re-arm the review sandbox")
	}
	if annotations("factory-pr-repo-42")[annoRereviewRequest] != "" {
		t.Error("the Review click stamped the PR's follow-up sandbox")
	}

	req, _ = http.NewRequest("POST", "/board/myboard/prs/42/abandon", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("abandon: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	a := annotations("review-repo-42")
	if a["reviewState"] != "" || a[annoReviewAbandoned] == "" {
		t.Errorf("abandon did not park the review sandbox: %v", a)
	}
	if annotations("factory-pr-repo-42")[annoReviewAbandoned] != "" {
		t.Error("abandon stamped the PR's follow-up sandbox")
	}
}

// A PR is the member's to push to only when its head is their fork, not a
// branch of the base repository.
func TestHeadOnMemberFork(t *testing.T) {
	pr := func(full, owner string, fork bool) *github.PullRequest {
		return &github.PullRequest{
			Head: &github.PullRequestBranch{Repo: &github.Repository{FullName: github.String(full), Fork: github.Bool(fork), Owner: &github.User{Login: github.String(owner)}}},
			Base: &github.PullRequestBranch{Repo: &github.Repository{FullName: github.String("test/repo")}},
		}
	}
	for _, tc := range []struct {
		name string
		pr   *github.PullRequest
		want bool
	}{
		{"own fork", pr("Alice/repo", "Alice", true), true},
		{"someone else's fork", pr("bob/repo", "bob", true), false},
		{"branch of the base repo", pr("test/repo", "alice", false), false},
		{"no head repo (fork deleted)", &github.PullRequest{}, false},
	} {
		if got := headOnMemberFork(tc.pr, "alice"); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The PR row's follow-ups are the fix run's recorded revises, each with the
// inputs it asks for.
func TestFixRevisesFromTheRecordedRun(t *testing.T) {
	if got := fixRevises(nil); got != nil {
		t.Errorf("no sandbox, no revises: %+v", got)
	}
	sb := &unstructured.Unstructured{}
	sb.SetAnnotations(map[string]string{factorycli.AnnotationFixRun: `{"name":"fix/repo/1/1","task":"t","kind":"Change","revises":[{"id":"iterate","label":"Iterate","inputs":["instruction"]},{"id":"rebase","label":"Rebase"}]}`})
	got := fixRevises(sb)
	if len(got) != 2 || got[0].Revise != "iterate" || len(got[0].Inputs) != 1 || got[0].Inputs[0] != "instruction" ||
		got[1].Revise != "rebase" || got[1].Label != "Rebase" || len(got[1].Inputs) != 0 || !got[1].Enabled || got[1].Verb != "revise" {
		t.Errorf("revises wrong: %+v", got)
	}
}
