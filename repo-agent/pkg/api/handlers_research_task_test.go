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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	podacpd "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
)

// researchTaskID is the task `factory recipe research` started in the
// sandbox, whose agent session the conversation is.
const researchTaskID = "research-task-1"

// recipeResearchSandboxCR is a research sandbox the recipe made: it
// records its start, so its conversation is the daemon's task session.
func recipeResearchSandboxCR() *unstructured.Unstructured {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: factorycli.ResearchRunName(researchSession), Task: researchTaskID, StartedAt: time.Now().UTC(),
	})
	annotations := sb.GetAnnotations()
	annotations[factorycli.ResearchRunAnnotation] = string(run)
	sb.SetAnnotations(annotations)
	return sb
}

// recipeResearchServer stands acpd and the daemon's task sessions up
// apart, so a test can see which one a call reached. hosts false is an
// image whose daemon keeps no task sessions.
func recipeResearchServer(t *testing.T, acp, daemon *fakeACPD, hosts bool) *gin.Engine {
	t.Helper()
	sb := recipeResearchSandboxCR()
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.1.2.3", corev1.PodRunning))
	srv := httptest.NewServer(daemon.handler())
	t.Cleanup(srv.Close)
	prev := taskSessionClientForPod
	taskSessionClientForPod = func(context.Context, *podacpd.Dialer, *corev1.Pod) (*acpd.Client, bool) {
		if !hosts {
			return nil, false
		}
		return acpd.New(srv.URL), true
	}
	t.Cleanup(func() { taskSessionClientForPod = prev })
	return r
}

// A recipe's conversation is continued in the daemon's session for its
// task, loaded where the task left it — acpd is never asked.
func TestRecipeResearchPromptContinuesTheTaskSession(t *testing.T) {
	acp, daemon := &fakeACPD{}, &fakeACPD{}
	r := recipeResearchServer(t, acp, daemon, true)

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"and the backoff?"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !daemon.sawCall("POST /sessions/" + researchTaskID + "/prompt") {
		t.Fatalf("the prompt did not reach the task session; daemon calls: %v", daemon.calls)
	}
	var create acpd.CreateSessionRequest
	if err := json.Unmarshal([]byte(daemon.createBody), &create); err != nil {
		t.Fatalf("create body %q: %v", daemon.createBody, err)
	}
	if create.ID != researchTaskID || create.Task != researchTaskID {
		t.Errorf("created %q for task %q, want both %q", create.ID, create.Task, researchTaskID)
	}
	if daemon.createKey != "engine-key-value" {
		t.Errorf("%s = %q, want the member's key", acpd.APIKeyHeader, daemon.createKey)
	}
	if len(acp.calls) != 0 {
		t.Errorf("acpd was asked about a task session: %v", acp.calls)
	}
}

// While the recipe's start is still asking the opening question the
// session is held: the status says so, and the list reads its state off
// the daemon.
func TestRecipeResearchHeldWhileTheStartRuns(t *testing.T) {
	acp := &fakeACPD{}
	daemon := &fakeACPD{live: map[string]acpd.Session{
		researchTaskID: {ID: researchTaskID, Busy: true, Held: true},
	}}
	r := recipeResearchServer(t, acp, daemon, true)

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["held"] != true || got["task"] != researchTaskID {
		t.Errorf("status = %s, want held for task %s", w.Body.String(), researchTaskID)
	}

	views := listResearch(t, r)
	if len(views) != 1 || !views[0].Held || !views[0].Busy || views[0].Task != researchTaskID {
		t.Errorf("list = %+v, want one held, busy task session", views)
	}
	if len(acp.calls) != 0 {
		t.Errorf("acpd was asked about a task session: %v", acp.calls)
	}
}

// An image whose daemon keeps no task sessions has nowhere to continue
// the conversation: a 409 naming it, not a create on acpd.
func TestRecipeResearchOnAnImageWithoutTaskSessionsIsConflict(t *testing.T) {
	acp := &fakeACPD{}
	r := recipeResearchServer(t, acp, &fakeACPD{}, false)

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/prompt", `{"text":"hi"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"legacy":true`) {
		t.Fatalf("status = %d, body %s; want 409 legacy", w.Code, w.Body.String())
	}
	if len(acp.calls) != 0 {
		t.Errorf("acpd was asked about a task session: %v", acp.calls)
	}
}

// Saving a recipe session's notes is not a capture: the controller's
// push reads acpd, which does not host it. Refused, with nothing stamped.
func TestRecipeResearchCaptureIsRefused(t *testing.T) {
	acp, daemon := &fakeACPD{}, &fakeACPD{}
	r := recipeResearchServer(t, acp, daemon, true)

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/capture", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s; want 409", w.Code, w.Body.String())
	}
	if len(daemon.calls) != 0 || len(acp.calls) != 0 {
		t.Errorf("a refused save reached an engine: daemon %v, acpd %v", daemon.calls, acp.calls)
	}
}
