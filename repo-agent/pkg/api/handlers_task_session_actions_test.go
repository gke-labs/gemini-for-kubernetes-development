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
		Revises: []factorycli.RecordedRevise{{ID: "plan", Label: "Update plan"}},
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
	if revise == nil || revise.Spec.Number != 42 || revise.Spec.Revise != "plan" || revise.Spec.Sandbox != "" {
		t.Errorf("revise filed %+v, want Update plan on #42", revise)
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
