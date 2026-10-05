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
	r, _ := recipeResearchServerDyn(t, acp, daemon, hosts)
	return r
}

// recipeResearchServerDyn is recipeResearchServer with its cluster.
func recipeResearchServerDyn(t *testing.T, acp, daemon *fakeACPD, hosts bool) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	return recipeResearchServerWith(t, acp, daemon, hosts, recipeResearchSandboxCR())
}

// recipeResearchServerWith serves sb, running, beside the others in the
// namespace.
func recipeResearchServerWith(t *testing.T, acp, daemon *fakeACPD, hosts bool, sb *unstructured.Unstructured, others ...*unstructured.Unstructured) (*gin.Engine, *fake.FakeDynamicClient) {
	t.Helper()
	r, dyn := researchTestServer(t, acp, append([]*unstructured.Unstructured{sb}, others...),
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

// 💾 on a recipe session files the recipe's notes revise for its sandbox
// and pins the note's name; no engine is asked from the API.
func TestRecipeResearchCaptureFilesTheNotesRevise(t *testing.T) {
	acp, daemon := &fakeACPD{}, &fakeACPD{}
	r, dyn := recipeResearchServerDyn(t, acp, daemon, true)
	seedNotesBoard(t, dyn)

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/capture", `{}`)
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
	if len(daemon.calls) != 0 || len(acp.calls) != 0 {
		t.Errorf("the save reached an engine: daemon %v, acpd %v", daemon.calls, acp.calls)
	}
}

// Without a board for the repository there is no controller to run the
// revise: a 409, and nothing filed.
func TestRecipeResearchCaptureWithoutABoardIsConflict(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeACPD{}, &fakeACPD{}, true)

	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/capture", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s; want 409", w.Code, w.Body.String())
	}
	if reqs := filedRequests(t, dyn, "alice"); len(reqs) != 0 {
		t.Errorf("filed %+v without a board", reqs)
	}
}

// The status carries the draft and what the newest clicks on it say.
func TestRecipeResearchStatusCarriesTheNotes(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeACPD{}, &fakeACPD{}, true)
	setNotesAnnotations(t, dyn, map[string]string{annoNotesDraft: "# Findings", annoNotesDraftedAt: "2026-10-05T10:00:00Z"})
	failed := requestCR(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbApply, Member: "alice", Sandbox: recipeResearchSandboxCR().GetName(),
		Apply: &boardv1alpha1.ApplyRequest{Kind: "Notes", Action: "push-notes"},
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

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Notes researchNotesState `json:"notes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Notes.Markdown != "# Findings" || !got.Notes.Writing || got.Notes.SaveError != "push refused" || got.Notes.Saving {
		t.Errorf("notes = %+v, want the draft, writing, and the save's failure", got.Notes)
	}
}

// Save to research/notes files the push of the draft; with no draft there
// is nothing to push.
func TestRecipeResearchSaveNotesFilesThePush(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeACPD{}, &fakeACPD{}, true)
	seedNotesBoard(t, dyn)

	if w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/notes/save", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("save without a draft: status = %d, body %s; want 404", w.Code, w.Body.String())
	}
	setNotesAnnotations(t, dyn, map[string]string{annoNotesDraft: "# Findings"})
	w := doJSON(t, r, http.MethodPost, "/api/research/"+researchSession+"/notes/save", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
	req := theRequest(t, dyn, "alice")
	if req.Spec.Verb != boardv1alpha1.VerbApply || req.Spec.Apply == nil ||
		req.Spec.Apply.Kind != "Notes" || req.Spec.Apply.Action != "push-notes" || req.Spec.Sandbox != recipeResearchSandboxCR().GetName() {
		t.Errorf("filed %+v, want push-notes of the sandbox", req.Spec)
	}
}

// An edit replaces the draft and makes it unsaved; a discard drops it all.
func TestRecipeResearchNotesEditAndDiscard(t *testing.T) {
	r, dyn := recipeResearchServerDyn(t, &fakeACPD{}, &fakeACPD{}, true)
	setNotesAnnotations(t, dyn, map[string]string{
		annoNotesDraft: "# Findings", annoNotesDraftedAt: "2026-10-05T10:00:00Z", annoNotesSaved: "2026-10-05T10:05:00Z",
		factorycli.AnnotationNotesOutput: "kind: Notes",
	})

	if w := doJSON(t, r, http.MethodPut, "/api/research/"+researchSession+"/notes", `{"markdown":"  "}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty edit: status = %d, want 400", w.Code)
	}
	if w := doJSON(t, r, http.MethodPut, "/api/research/"+researchSession+"/notes", `{"markdown":"# Edited\n"}`); w.Code != http.StatusNoContent {
		t.Fatalf("edit: status = %d, body %s", w.Code, w.Body.String())
	}
	a := notesSandboxAnnotations(t, dyn)
	if a[annoNotesDraft] != "# Edited" || a[annoNotesSaved] != "" {
		t.Errorf("after the edit: draft %q, saved %q", a[annoNotesDraft], a[annoNotesSaved])
	}

	if w := doJSON(t, r, http.MethodDelete, "/api/research/"+researchSession+"/notes", ""); w.Code != http.StatusNoContent {
		t.Fatalf("discard: status = %d, body %s", w.Code, w.Body.String())
	}
	a = notesSandboxAnnotations(t, dyn)
	for _, k := range []string{annoNotesDraft, annoNotesDraftedAt, annoNotesSaved, factorycli.AnnotationNotesOutput} {
		if a[k] != "" {
			t.Errorf("after the discard %s = %q", k, a[k])
		}
	}
	if _, ok := a[factorycli.ResearchRunAnnotation]; !ok {
		t.Error("the discard dropped the recorded run")
	}
}

// A sandbox the old research path made has no task session: its notes
// routes answer 409 legacy, like every conversation route.
func TestLegacyResearchNotesRoutesAreConflict(t *testing.T) {
	sb := legacyResearchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{sb})
	if w := doJSON(t, r, http.MethodDelete, "/api/research/"+researchSession+"/notes", ""); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"legacy":true`) {
		t.Errorf("status = %d, body %s; want 409 legacy", w.Code, w.Body.String())
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
