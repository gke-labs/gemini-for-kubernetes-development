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
	"strings"
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

func capturePath(sessionID string) string { return "/api/research/" + sessionID + "/capture" }

// sandboxAnnotations reads the sandbox back out of the fake cluster.
func captureAnnotations(t *testing.T, dyn *fake.FakeDynamicClient, sessionID string) map[string]string {
	t.Helper()
	sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(),
		researchSandboxCR("alice", sessionID, researchRepo, false).GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the sandbox back: %v", err)
	}
	return sb.GetAnnotations()
}

// The whole request in one pass: the turn is sent, and the push it will
// need is recorded on the sandbox for the controller to make.
func TestCaptureSendsThePromptAndRecordsThePendingSave(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession),
		`{"what":"how the scheduler picks a node","note":"Scheduling notes"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Note   string `json:"note"`
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Note != "scheduling-notes.md" {
		t.Errorf("note = %q, want the normalised name", got.Note)
	}
	if want := research.NotesDir(researchSession) + "/scheduling-notes.md"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.Offset == 0 {
		t.Error("the caller needs the offset the prompt landed at")
	}

	// The turn actually went to the engine, carrying the member's words
	// and naming the one file it may write.
	if !acp.sawCall("POST /sessions/" + researchSession + "/prompt") {
		t.Fatalf("no prompt was sent; calls were %v", acp.calls)
	}
	if !strings.Contains(acp.promptBody, "how the scheduler picks a node") {
		t.Errorf("the prompt does not carry the request: %s", acp.promptBody)
	}
	if !strings.Contains(acp.promptBody, "scheduling-notes.md") {
		t.Errorf("the prompt does not name the note: %s", acp.promptBody)
	}

	// And the save is owed.
	pending, ok := research.DecodePending(captureAnnotations(t, dyn, researchSession)[research.CaptureAnnotation])
	if !ok {
		t.Fatal("no pending save was recorded; nothing would ever push the note")
	}
	if pending.Note != "scheduling-notes.md" {
		t.Errorf("pending note = %q", pending.Note)
	}
	if pending.At.IsZero() {
		t.Error("a pending save with no clock on it could never expire")
	}
}

// A member who never thinks about where notes go gets one document per
// session, which is the readable default.
func TestCaptureWithoutANoteUsesTheSessionsOwnDocument(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), `{"what":"the retry loop"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), research.DefaultNote) {
		t.Errorf("body = %s, want the default note", w.Body.String())
	}
}

// Nothing to capture is a bad request, not an empty turn: a prompt is
// the one thing here that cannot be taken back once sent.
func TestCaptureRefusesAnEmptyRequest(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	for _, body := range []string{`{"what":"  "}`, `{"note":"notes"}`, `{"what":"x","note":"///"}`} {
		w := doJSON(t, r, http.MethodPost, capturePath(researchSession), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, w.Code)
		}
	}
	if acp.sawCall("POST /sessions/" + researchSession + "/prompt") {
		t.Error("a refused capture must not reach the engine")
	}
	if _, ok := captureAnnotations(t, dyn, researchSession)[research.CaptureAnnotation]; ok {
		t.Error("a refused capture must not leave a save owed")
	}
}

// The common collision: the member asks while a turn is still running.
// acpd's 409 travels through unchanged so the UI can say "wait" rather
// than showing a failure — and, critically, the pending save is undone.
func TestCaptureWhileBusyLeavesNothingOwed(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true, promptStatus: http.StatusConflict}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), `{"what":"the retry loop"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if _, ok := captureAnnotations(t, dyn, researchSession)[research.CaptureAnnotation]; ok {
		t.Error("the prompt was refused, so the controller must not push for it")
	}
}

// Asking again clears the last failure. Otherwise the conversation goes
// on reporting an error about a note that has since been saved.
func TestCaptureClearsAnEarlierFailure(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureErrorAnnotation] = "the conversation wrote no notes"
	sb.SetAnnotations(annotations)
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), `{"what":"the retry loop"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got, ok := captureAnnotations(t, dyn, researchSession)[research.CaptureErrorAnnotation]; ok {
		t.Errorf("the stale failure survived a fresh capture: %q", got)
	}
}

// A session may be captured many times; each request simply replaces
// what is owed. The push is of the whole directory, so there is nothing
// to queue.
func TestCaptureTwiceReplacesWhatIsOwed(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), `{"what":"a","note":"first"}`); w.Code != http.StatusAccepted {
		t.Fatalf("first capture: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), `{"what":"b","note":"second"}`); w.Code != http.StatusAccepted {
		t.Fatalf("second capture: %d %s", w.Code, w.Body.String())
	}
	pending, ok := research.DecodePending(captureAnnotations(t, dyn, researchSession)[research.CaptureAnnotation])
	if !ok || pending.Note != "second.md" {
		t.Errorf("pending = %+v, want the second note", pending)
	}
}

// The state the conversation reports back, so a member who asked for a
// note can tell "still working" from "never happened" without leaving
// the tab.
func TestSessionStatusReportsAnOwedSave(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureAnnotation] = research.Pending{Note: "notes.md", At: time.Now().UTC()}.Encode()
	sb.SetAnnotations(annotations)
	r, _ := researchTestServer(t, &fakeACPD{sessionExists: true}, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Capturing string `json:"capturing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Capturing != "notes.md" {
		t.Errorf("capturing = %q, want the note in flight", got.Capturing)
	}
}

// Notes are not listable without the member's GitHub token, and saying
// "no notes" would tell them the ones they saved had vanished. The form
// still works — they type a name — so this is a 200 with a reason.
func TestNotesListSaysWhenItCannotRead(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	r, _ := researchTestServer(t, nil, []*unstructured.Unstructured{sb})

	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession+"/notes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Notes       []map[string]any `json:"notes"`
		Branch      string           `json:"branch"`
		Dir         string           `json:"dir"`
		DefaultNote string           `json:"defaultNote"`
		Unreadable  string           `json:"unreadable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Unreadable == "" {
		t.Error("an unreadable branch has to say so, not report an empty archive")
	}
	if got.Notes == nil {
		t.Error("notes must be an empty array, not null: the form iterates it")
	}
	// The form needs all three to offer "same or new note" without
	// hard-coding any of the paths the write side agreed on.
	if got.Branch != notesBranch {
		t.Errorf("branch = %q, want %q", got.Branch, notesBranch)
	}
	if got.Dir != research.NotesDir(researchSession) {
		t.Errorf("dir = %q", got.Dir)
	}
	if got.DefaultNote != research.DefaultNote {
		t.Errorf("defaultNote = %q", got.DefaultNote)
	}
}

// Listing the notes of a session that does not exist is a 404, not an
// empty archive belonging to nobody.
func TestNotesListRejectsAnUnknownSession(t *testing.T) {
	r, _ := researchTestServer(t, nil, nil)
	w := doJSON(t, r, http.MethodGet, "/api/research/"+researchSession+"/notes", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
