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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	podacpd "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
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
		Kind: "Notes", Revises: []factorycli.RecordedRevise{{ID: notesRevise, Label: "Save notes"}},
	})
	annotations := sb.GetAnnotations()
	annotations[factorycli.ResearchRunAnnotation] = string(run)
	sb.SetAnnotations(annotations)
	return sb
}

// notesRevise is the research recipe's Save notes revise.
const notesRevise = "notes"

// recipeConversation is the recipe sandbox's conversation: its task
// session.
func recipeConversation() string {
	return "/api/task-sessions/" + recipeResearchSandboxCR().GetName() + "/" + researchTaskID
}

// recipeResearchServer stands the daemon's task sessions up. hosts false
// is an image whose daemon keeps no task sessions.
func recipeResearchServer(t *testing.T, daemon *fakeSessions, hosts bool) *gin.Engine {
	t.Helper()
	r, _ := recipeResearchServerDyn(t, daemon, hosts)
	return r
}

// recipeResearchServerDyn is recipeResearchServer with its cluster.
func recipeResearchServerDyn(t *testing.T, daemon *fakeSessions, hosts bool) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	return recipeResearchServerWith(t, daemon, hosts, recipeResearchSandboxCR())
}

// recipeResearchServerWith serves sb, running, beside the others in the
// namespace.
func recipeResearchServerWith(t *testing.T, daemon *fakeSessions, hosts bool, sb *unstructured.Unstructured, others ...*unstructured.Unstructured) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	r, dyn := researchTestServer(t, nil, append([]*unstructured.Unstructured{sb}, others...),
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
	return r, dyn
}

// A recipe's conversation is continued in the daemon's session for its
// task, loaded where the task left it.
func TestRecipeResearchPromptContinuesTheTaskSession(t *testing.T) {
	daemon := &fakeSessions{}
	r := recipeResearchServer(t, daemon, true)

	w := doJSON(t, r, http.MethodPost, recipeConversation()+"/prompt", `{"text":"and the backoff?"}`)
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
}

// While the recipe's start is still asking the opening question the
// session is held: the status says so, and the list reads its state off
// the daemon.
func TestRecipeResearchHeldWhileTheStartRuns(t *testing.T) {
	daemon := &fakeSessions{live: map[string]acpd.Session{
		researchTaskID: {ID: researchTaskID, Busy: true, Held: true},
	}}
	r := recipeResearchServer(t, daemon, true)

	w := doJSON(t, r, http.MethodGet, recipeConversation(), "")
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
}

// An image whose daemon keeps no task sessions has nowhere to continue
// the conversation: a 409 naming it.
func TestRecipeResearchOnAnImageWithoutTaskSessionsIsConflict(t *testing.T) {
	r := recipeResearchServer(t, &fakeSessions{}, false)

	w := doJSON(t, r, http.MethodPost, recipeConversation()+"/prompt", `{"text":"hi"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"legacy":true`) {
		t.Fatalf("status = %d, body %s; want 409 legacy", w.Code, w.Body.String())
	}
}

// Save notes on a research conversation files the recipe's notes revise
// for its sandbox and pins the note's name; no engine is asked from the
// API.
func TestRecipeResearchCaptureFilesTheNotesRevise(t *testing.T) {
	daemon := &fakeSessions{}
	r, dyn := recipeResearchServerDyn(t, daemon, true)
	seedNotesBoard(t, dyn)

	w := doJSON(t, r, http.MethodPost, recipeConversation()+"/revise", `{"revise":"notes"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
	req := theRequest(t, dyn, "alice")
	if req.Spec.Verb != boardv1alpha1.VerbRevise || req.Spec.Revise != notesRevise ||
		req.Spec.Sandbox != recipeResearchSandboxCR().GetName() || req.Spec.Member != "alice" || req.Spec.Number != 0 {
		t.Errorf("filed %+v, want the notes revise of the sandbox", req.Spec)
	}
	if note := notesSandboxAnnotations(t, dyn)[research.NoteAnnotation]; note == "" {
		t.Error("the note's name was not pinned")
	}
	if len(daemon.calls) != 0 {
		t.Errorf("the save reached an engine: %v", daemon.calls)
	}
}

// Without a board for the repository there is no controller to run the
// revise: a 409, and nothing filed.
func TestRecipeResearchCaptureWithoutABoardIsConflict(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeSessions{}, true)

	w := doJSON(t, r, http.MethodPost, recipeConversation()+"/revise", `{"revise":"notes"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s; want 409", w.Code, w.Body.String())
	}
	if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
		t.Errorf("filed %+v without a board", reqs)
	}
}

// The status carries the draft and what the newest clicks on it say.
func TestRecipeResearchStatusCarriesTheNotes(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeSessions{}, true)
	setNotesAnnotations(t, dyn, map[string]string{factorycli.AnnotationNotesOutput: storedOutput("Notes", "# Findings"), annoNotesDraftedAt: "2026-10-05T10:00:00Z"})
	failed := requestCR(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbApply, Member: "alice", Sandbox: recipeResearchSandboxCR().GetName(),
		Apply: &boardv1alpha1.ApplyRequest{Run: "research", Action: "push-notes"},
	})
	_ = unstructured.SetNestedField(failed.Object, string(boardv1alpha1.RequestFailed), "status", "phase")
	_ = unstructured.SetNestedField(failed.Object, "push refused", "status", "message")
	writing := requestCR(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbRevise, Member: "alice", Sandbox: recipeResearchSandboxCR().GetName(), Revise: notesRevise,
	})
	writing.SetName("writing")
	for _, obj := range []*unstructured.Unstructured{failed, writing} {
		if _, err := dyn.Resource(requestGVR).Namespace("alice").Create(context.Background(), obj, v1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	w := doJSON(t, r, http.MethodGet, recipeConversation(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Revises []models.WorkAction `json:"revises"`
		Draft   *taskSessionDraft   `json:"draft"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Revises) != 1 || got.Revises[0].Label != "Save notes" || got.Revises[0].Enabled || got.Revises[0].Reason != revisingReason {
		t.Errorf("revises = %+v, want Save notes, revising", got.Revises)
	}
	if got.Draft == nil || got.Draft.Kind != "Notes" || got.Draft.Markdown != "# Findings" {
		t.Fatalf("draft = %+v, want the notes", got.Draft)
	}
	push, ok := findWorkAction(got.Draft.Actions, "push-notes", "")
	if !ok || push.Error != "push refused" || push.Enabled || push.Reason != notesRewritingReason {
		t.Errorf("push-notes = %+v, want waiting on the rewrite, with the save's failure", push)
	}
	if _, ok := findWorkAction(got.Draft.Actions, "revise", ""); ok {
		t.Errorf("the draft offers the session's revise: %+v", got.Draft.Actions)
	}
}

// The draft's actions are offered once there is a draft, and Save notes
// is offered from the start, before any output exists.
func TestRecipeResearchOffersSaveNotesBeforeAnyDraft(t *testing.T) {
	r := recipeResearchServer(t, &fakeSessions{}, true)
	w := doJSON(t, r, http.MethodGet, recipeConversation(), "")
	var got struct {
		Revises []models.WorkAction `json:"revises"`
		Draft   *taskSessionDraft   `json:"draft"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Revises) != 1 || got.Revises[0].Revise != notesRevise || !got.Revises[0].Enabled || got.Draft != nil {
		t.Errorf("status = %s, want Save notes and no draft", w.Body.String())
	}
}

// Save to research/notes files the push of the draft; with no draft there
// is nothing to push.
func TestRecipeResearchSaveNotesFilesThePush(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeSessions{}, true)
	seedNotesBoard(t, dyn)

	if w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/push-notes", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("save without a draft: status = %d, body %s; want 404", w.Code, w.Body.String())
	}
	setNotesAnnotations(t, dyn, map[string]string{factorycli.AnnotationNotesOutput: storedOutput("Notes", "# Findings")})
	w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/push-notes", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
	req := theRequest(t, dyn, "alice")
	if req.Spec.Verb != boardv1alpha1.VerbApply || req.Spec.Apply == nil ||
		req.Spec.Apply.Run != "research" || req.Spec.Apply.Action != "push-notes" || req.Spec.Sandbox != recipeResearchSandboxCR().GetName() {
		t.Errorf("filed %+v, want push-notes of the sandbox", req.Spec)
	}
}

// An edit replaces the draft and makes it unsaved; a discard drops it all.
func TestRecipeResearchNotesEditAndDiscard(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeSessions{}, true)
	setNotesAnnotations(t, dyn, map[string]string{
		factorycli.AnnotationNotesOutput: storedOutput("Notes", "# Findings"), annoNotesDraftedAt: "2026-10-05T10:00:00Z",
		factorycli.AnnotationNotesApplied: `{"push-notes":"2026-10-05T10:05:00Z"}`,
	})

	if w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/edit", `{"text":"  "}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty edit: status = %d, want 400", w.Code)
	}
	if w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/edit", `{"text":"# Edited\n"}`); w.Code != http.StatusNoContent {
		t.Fatalf("edit: status = %d, body %s", w.Code, w.Body.String())
	}
	a := notesSandboxAnnotations(t, dyn)
	if factorycli.NotesDraft(a) != "# Edited" || a[factorycli.AnnotationNotesApplied] != "" {
		t.Errorf("after the edit: draft %q, applied %q", factorycli.NotesDraft(a), a[factorycli.AnnotationNotesApplied])
	}

	if w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/reject", `{}`); w.Code != http.StatusNoContent {
		t.Fatalf("discard: status = %d, body %s", w.Code, w.Body.String())
	}
	a = notesSandboxAnnotations(t, dyn)
	for _, k := range []string{factorycli.AnnotationNotesOutput, annoNotesDraftedAt, factorycli.AnnotationNotesApplied} {
		if a[k] != "" {
			t.Errorf("after the discard %s = %q", k, a[k])
		}
	}
	if _, ok := a[factorycli.ResearchRunAnnotation]; !ok {
		t.Error("the discard dropped the recorded run")
	}
}

// A draft action the notes do not offer is refused, and a plan's verb
// is not one of them.
func TestRecipeResearchDraftRefusesAnUnofferedVerb(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeSessions{}, true)
	setNotesAnnotations(t, dyn, map[string]string{factorycli.AnnotationNotesOutput: storedOutput("Notes", "# Findings")})
	if w := doJSON(t, r, http.MethodPost, recipeConversation()+"/draft/comment", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, body %s; want 400", w.Code, w.Body.String())
	}
	if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
		t.Errorf("filed %+v", reqs)
	}
}

func seedNotesBoard(t *testing.T, dyn *fake.FakeDynamicClient) {
	t.Helper()
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Create(context.Background(), researchBoardCR(), v1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func notesSandboxAnnotations(t *testing.T, dyn *fake.FakeDynamicClient) map[string]string {
	t.Helper()
	sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(), recipeResearchSandboxCR().GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return sb.GetAnnotations()
}

func setNotesAnnotations(t *testing.T, dyn *fake.FakeDynamicClient, set map[string]string) {
	t.Helper()
	sbs := dyn.Resource(k8s.SandboxGVR).Namespace("alice")
	sb, err := sbs.Get(context.Background(), recipeResearchSandboxCR().GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := sb.GetAnnotations()
	for k, v := range set {
		a[k] = v
	}
	sb.SetAnnotations(a)
	if _, err := sbs.Update(context.Background(), sb, v1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
