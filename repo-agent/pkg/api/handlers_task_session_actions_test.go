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
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// planSessionAt is the plan's session on the board test's issue 42.
const planSessionAt = "/api/task-sessions/fix-repo-42/recipe-plan-1"

// planSessionServer is the board test server with issue 42's plan
// sandbox: a draft from the session recipe-plan-1, whose recorded run
// offers Update plan; approved when approved is set.
func planSessionServer(t *testing.T, approved bool) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "plan/myboard/42/1", Task: "recipe-plan-1", StartedAt: time.Unix(1_000_000, 0),
		Kind: "Plan", Revises: []factorycli.RecordedRevise{{ID: "plan", Label: "Update plan"}},
	})
	annotations := map[string]interface{}{
		"repo":    "repo",
		"htmlURL": "https://github.com/test/repo/issues/42",
		annoBoard: "myboard",
		"sandbox.gemini.google.com/last-task-type":  "plan",
		"sandbox.gemini.google.com/last-task-state": "Completed",
		annoPlanDraft:                "## Summary\nDo the thing.",
		annoPlannedAt:                "2026-09-17T00:00:00Z",
		factorycli.AnnotationPlanRun: string(run),
		factorycli.AnnotationPlanOutput: `apiVersion: factory.gemini.google.com/v1alpha1
kind: Plan
source:
  task: recipe-plan-1
actions:
  - verb: comment
    label: Post plan
  - verb: reject
  - verb: revise
    revise: plan
    label: Update plan
`,
	}
	if approved {
		annotations[annoPlanApproved] = "2026-09-18T00:00:00Z"
	}
	server, r, dyn, _ := boardTestServerWithRT(t, issueFeed(42), boardCR(),
		sandboxCR("fix-repo-42", map[string]interface{}{"factory.gemini.google.com/managed": "true"}, annotations, 1))
	r.POST("/api/task-sessions/:sandbox/:task/revise", server.reviseTaskSession)
	r.POST("/api/task-sessions/:sandbox/:task/draft/:verb", server.taskSessionDraftAction)
	return r, dyn
}

// A plan session's revise and draft actions are filed on the issue's row,
// as the row's own buttons file them: the browser names only the session.
func TestAPlanSessionActsOnItsIssueRow(t *testing.T) {
	r, dyn := planSessionServer(t, false)

	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/revise", `{"revise":"other"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a revise not offered: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/draft/revise", `{"run":"plan"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a revise through the draft: %d %s; the session offers it, not the draft", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/draft/comment", `{}`); w.Code != http.StatusAccepted {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	// A revise waits for no write; the draft's actions wait for it.
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/revise", `{"revise":"plan"}`); w.Code != http.StatusAccepted {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	var revise, apply *boardv1alpha1.Request
	for _, req := range filedRequests(t, dyn, "alice") {
		switch req.Spec.Verb {
		case boardv1alpha1.VerbRevise:
			revise = &req
		case boardv1alpha1.VerbApply:
			apply = &req
		}
	}
	if revise == nil || revise.Spec.Number != 42 || revise.Spec.Revise != "plan" || revise.Spec.Sandbox != "fix-repo-42" {
		t.Errorf("revise filed %+v, want Update plan of fix-repo-42, on #42", revise)
	}
	if apply == nil || apply.Spec.Number != 42 || apply.Spec.Apply == nil ||
		*apply.Spec.Apply != (boardv1alpha1.ApplyRequest{Kind: "Plan", Action: "comment"}) {
		t.Errorf("comment filed %+v, want the plan's comment on #42", apply)
	}
}

// Once the plan is approved it is not revised, and the session says why.
func TestAnApprovedPlansSessionRefusesItsRevise(t *testing.T) {
	r, dyn := planSessionServer(t, true)
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/revise", `{"revise":"plan"}`); w.Code != http.StatusConflict {
		t.Errorf("revise: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, planSessionAt+"/draft/comment", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("comment: %d %s, want 404: an approved plan is no draft", w.Code, w.Body.String())
	}
	if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
		t.Errorf("filed %+v", reqs)
	}
}

// reviewSessionAt is the review's session on the board test's PR 42.
const reviewSessionAt = "/api/task-sessions/review-repo-42/recipe-review-1"

// reviewSessionServer is the board test server with PR 42's review
// sandbox, whose recorded run offers Update review, in state (the
// review's last-task-state) with reviewState.
func reviewSessionServer(t *testing.T, state, reviewState string) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "review/myboard/42/1", Task: "recipe-review-1", StartedAt: time.Unix(1_000_000, 0),
		Kind: "Review", Revises: []factorycli.RecordedRevise{{ID: "review", Label: "Update review"}},
	})
	annotations := map[string]interface{}{
		"repo":    "repo",
		"htmlURL": "https://github.com/test/repo/pull/42",
		annoBoard: "myboard",
		"sandbox.gemini.google.com/last-task-type":  "recipe-review",
		"sandbox.gemini.google.com/last-task-state": state,
		"reviewState":                  reviewState,
		factorycli.AnnotationReviewRun: string(run),
	}
	server, r, dyn, _ := boardTestServerWithRT(t, issueFeed(1), boardCR(),
		sandboxCR("review-repo-42", map[string]interface{}{"factory.gemini.google.com/managed": "true"}, annotations, 1))
	r.POST("/api/task-sessions/:sandbox/:task/revise", server.reviseTaskSession)
	r.POST("/api/task-sessions/:sandbox/:task/draft/:verb", server.taskSessionDraftAction)
	return r, dyn
}

// A review's Update review is filed for its sandbox: the review has no
// row of its own to file it on, and no draft on the board (the draft is
// the pending review on GitHub).
func TestAReviewSessionFilesUpdateReviewForItsSandbox(t *testing.T) {
	r, dyn := reviewSessionServer(t, "Completed", "pending")
	if w := doJSON(t, r, http.MethodPost, reviewSessionAt+"/draft/post-review", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("a draft action: %d %s, want 404: the review's draft is on GitHub", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, reviewSessionAt+"/revise", `{"revise":"review"}`); w.Code != http.StatusAccepted {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	reqs := filedRequests(t, dyn, "alice")
	if len(reqs) != 1 {
		t.Fatalf("filed %+v, want one revise", reqs)
	}
	spec := reqs[0].Spec
	if spec.Verb != boardv1alpha1.VerbRevise || spec.Sandbox != "review-repo-42" || spec.Revise != "review" || spec.Number != 0 || spec.Member != "alice" {
		t.Errorf("revise filed %+v, want alice's Update review of review-repo-42", spec)
	}
	// One at a time.
	if w := doJSON(t, r, http.MethodPost, reviewSessionAt+"/revise", `{"revise":"review"}`); w.Code != http.StatusConflict {
		t.Errorf("second revise: %d %s, want 409 while the first stands", w.Code, w.Body.String())
	}
}

// Not while the review runs, nor before it is on GitHub.
func TestAReviewSessionRefusesUpdateReviewUntilPosted(t *testing.T) {
	for _, tc := range []struct{ state, reviewState string }{{"Running", ""}, {"Completed", ""}} {
		r, dyn := reviewSessionServer(t, tc.state, tc.reviewState)
		if w := doJSON(t, r, http.MethodPost, reviewSessionAt+"/revise", `{"revise":"review"}`); w.Code != http.StatusConflict {
			t.Errorf("%s/%q: revise %d %s, want 409", tc.state, tc.reviewState, w.Code, w.Body.String())
		}
		if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
			t.Errorf("filed %+v", reqs)
		}
	}
}

// fixSessionAt is the fix's session on the board test's issue 42.
const fixSessionAt = "/api/task-sessions/fix-repo-42/recipe-fix-1"

// fixSessionServer is the board test server with issue 42's sandbox after
// its fix, whose recorded run offers the follow-ups, in state; aliased to
// PR 9 when pr is set, as open-pr leaves it.
func fixSessionServer(t *testing.T, state string, pr bool) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "fix/myboard/42/1", Task: "recipe-fix-1", StartedAt: time.Unix(1_000_000, 0),
		Kind: "Change", Revises: []factorycli.RecordedRevise{{ID: "iterate", Label: "Iterate", Inputs: []string{"instruction"}}, {ID: "address-comments", Label: "Address comments"}, {ID: "fix-ci", Label: "Fix CI"}},
	})
	htmlURL := "https://github.com/test/repo/issues/42"
	if pr {
		htmlURL = "https://github.com/test/repo/pull/9"
	}
	annotations := map[string]interface{}{
		"repo":    "repo",
		"htmlURL": htmlURL,
		annoBoard: "myboard",
		"sandbox.gemini.google.com/last-task-type":  "fix",
		"sandbox.gemini.google.com/last-task-state": state,
		factorycli.AnnotationFixRun:                 string(run),
	}
	server, r, dyn, _ := boardTestServerWithRT(t, issueFeed(42), boardCR(),
		sandboxCR("fix-repo-42", map[string]interface{}{"factory.gemini.google.com/managed": "true", factorycli.LabelIssue: "42"}, annotations, 1))
	r.POST("/api/task-sessions/:sandbox/:task/revise", server.reviseTaskSession)
	r.POST("/api/task-sessions/:sandbox/:task/draft/:verb", server.taskSessionDraftAction)
	return r, dyn
}

// A fix's follow-ups are filed for its sandbox, Iterate with the member's
// instruction; it takes no other input, and cannot go without it.
func TestAFixSessionFilesItsFollowUpsForItsSandbox(t *testing.T) {
	r, dyn := fixSessionServer(t, "Completed", true)
	for _, body := range []string{
		`{"revise":"iterate"}`,
		`{"revise":"iterate","inputs":{"instruction":"  "}}`,
		`{"revise":"fix-ci","inputs":{"instruction":"x"}}`,
		`{"revise":"iterate","inputs":{"instruction":"x","other":"y"}}`,
	} {
		if w := doJSON(t, r, http.MethodPost, fixSessionAt+"/revise", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if w := doJSON(t, r, http.MethodPost, fixSessionAt+"/revise", `{"revise":"iterate","inputs":{"instruction":" rename it "}}`); w.Code != http.StatusAccepted {
		t.Fatalf("iterate: %d %s", w.Code, w.Body.String())
	}
	// One follow-up at a time: they push to the same branch.
	if w := doJSON(t, r, http.MethodPost, fixSessionAt+"/revise", `{"revise":"address-comments"}`); w.Code != http.StatusConflict {
		t.Errorf("a second follow-up: %d %s, want 409 while the first stands", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, fixSessionAt+"/draft/open-pr", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("a draft action: %d %s, want 404: the fix's draft is the PR", w.Code, w.Body.String())
	}
	reqs := filedRequests(t, dyn, "alice")
	if len(reqs) != 1 {
		t.Fatalf("filed %+v, want one revise", reqs)
	}
	if spec := reqs[0].Spec; spec.Verb != boardv1alpha1.VerbRevise || spec.Sandbox != "fix-repo-42" || spec.Revise != "iterate" ||
		spec.Number != 42 || spec.Member != "alice" || spec.Inputs["instruction"] != "rename it" {
		t.Errorf("iterate filed %+v, want alice's revise of fix-repo-42 with the instruction", spec)
	}
}

// Not while the fix's sandbox is at work, nor before the fix has a PR.
func TestAFixSessionRefusesFollowUpsUntilItsPR(t *testing.T) {
	for _, tc := range []struct {
		state string
		pr    bool
	}{{"Running", true}, {"Completed", false}} {
		r, dyn := fixSessionServer(t, tc.state, tc.pr)
		if w := doJSON(t, r, http.MethodPost, fixSessionAt+"/revise", `{"revise":"fix-ci"}`); w.Code != http.StatusConflict {
			t.Errorf("%s/%v: revise %d %s, want 409", tc.state, tc.pr, w.Code, w.Body.String())
		}
		if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
			t.Errorf("filed %+v", reqs)
		}
	}
}
