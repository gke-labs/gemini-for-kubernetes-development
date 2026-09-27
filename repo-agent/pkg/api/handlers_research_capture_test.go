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

// titled is a sandbox that has been named, which is every session past
// its first turn and the only kind whose note gets a readable name.
func titled(sb *unstructured.Unstructured, title string) *unstructured.Unstructured {
	annotations := sb.GetAnnotations()
	annotations[research.TitleAnnotation] = title
	sb.SetAnnotations(annotations)
	return sb
}

// captureAnnotations reads the sandbox back out of the fake cluster.
func captureAnnotations(t *testing.T, dyn *fake.FakeDynamicClient, sessionID string) map[string]string {
	t.Helper()
	sb, err := dyn.Resource(k8s.SandboxGVR).Namespace("alice").Get(context.Background(),
		researchSandboxCR("alice", sessionID, researchRepo, false).GetName(), v1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the sandbox back: %v", err)
	}
	return sb.GetAnnotations()
}

// The whole request in one pass, and the request is a click: no body,
// no note name, no description of what to capture. The turn is sent,
// and the push it will need is recorded on the sandbox for the
// controller to make.
func TestCaptureTakesNoInputsAndRecordsThePendingSave(t *testing.T) {
	sb := titled(researchSandboxCR("alice", researchSession, researchRepo, false),
		"Where the retry loop terminates")
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), "")
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
	// The session's name, not its id: this path is what someone reads
	// off the branch months later.
	if got.Note != "where-the-retry-loop-terminates.md" {
		t.Errorf("note = %q, want the session's one document", got.Note)
	}
	want := research.NotesPath("where-the-retry-loop-terminates.md")
	if got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.Offset == 0 {
		t.Error("the caller needs the offset the prompt landed at")
	}

	// The turn actually went to the engine, naming the one file it may
	// write and carrying the canned request in place of the member's.
	if !acp.sawCall("POST /sessions/" + researchSession + "/prompt") {
		t.Fatalf("no prompt was sent; calls were %v", acp.calls)
	}
	if !strings.Contains(acp.promptBody, want) {
		t.Errorf("the prompt does not name the note: %s", acp.promptBody)
	}
	if !strings.Contains(acp.promptBody, research.DefaultWhat) {
		t.Errorf("the prompt does not say what to capture: %s", acp.promptBody)
	}

	// And the save is owed, with the file to push travelling on it.
	annotations := captureAnnotations(t, dyn, researchSession)
	pending, ok := research.DecodePending(annotations[research.CaptureAnnotation])
	if !ok {
		t.Fatal("no pending save was recorded; nothing would ever push the note")
	}
	if pending.Note != "where-the-retry-loop-terminates.md" {
		t.Errorf("pending = %+v", pending)
	}
	if pending.At.IsZero() {
		t.Error("a pending save with no clock on it could never expire")
	}
	if annotations[research.NoteAnnotation] != "where-the-retry-loop-terminates.md" {
		t.Errorf("the note name was not pinned: %q", annotations[research.NoteAnnotation])
	}
}

// A caller with something narrower in mind can still say so. Nothing in
// the UI sends this, but the prompt is the natural place for it and a
// body that says nothing must not be a 400.
func TestCaptureCarriesAnExplicitRequest(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, _ := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession),
		`{"what":"only how the scheduler picks a node"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(acp.promptBody, "only how the scheduler picks a node") {
		t.Errorf("the prompt does not carry the request: %s", acp.promptBody)
	}
	if strings.Contains(acp.promptBody, research.DefaultWhat) {
		t.Errorf("the canned request survived an explicit one: %s", acp.promptBody)
	}
}

// A session nobody has named yet still has an id, and an id is a worse
// file name than a name but a much better one than an empty path
// component.
func TestCaptureFallsBackToTheSessionIDWhenUnnamed(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), ""); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := captureAnnotations(t, dyn, researchSession)[research.NoteAnnotation]; got != researchSession+".md" {
		t.Errorf("note = %q, want the session id", got)
	}
}

// The name is pinned at the first save and does not move afterwards. A
// session renamed between two captures would otherwise push its second
// note somewhere new and leave the first one orphaned under a name
// nothing refers to any more.
func TestCaptureKeepsTheNameItPinned(t *testing.T) {
	sb := titled(researchSandboxCR("alice", researchSession, researchRepo, false), "first read")
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), ""); w.Code != http.StatusAccepted {
		t.Fatalf("first capture: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPatch, "/api/research/"+researchSession,
		`{"title":"how the scheduler picks a node"}`); w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), ""); w.Code != http.StatusAccepted {
		t.Fatalf("second capture: %d %s", w.Code, w.Body.String())
	}

	annotations := captureAnnotations(t, dyn, researchSession)
	if got := annotations[research.NoteAnnotation]; got != "first-read.md" {
		t.Errorf("note = %q, want the one the first save pinned", got)
	}
	pending, ok := research.DecodePending(annotations[research.CaptureAnnotation])
	if !ok || pending.Note != "first-read.md" {
		t.Errorf("pending = %+v, want the pinned note", pending)
	}
}

// Two sessions can genuinely want one name — every canned "first read"
// of a repository is called the same thing — and the save overwrites
// what is on the branch, so the second one sharing it would be the
// second one replacing the first one's notes.
func TestCaptureDoesNotTakeAnotherSessionsNote(t *testing.T) {
	const otherSession = "0c7b3d9a-1111-2222-3333-444455556666"
	taken := titled(researchSandboxCR("alice", otherSession, researchRepo, false), "first read")
	annotations := taken.GetAnnotations()
	annotations[research.NoteAnnotation] = "first-read.md"
	taken.SetAnnotations(annotations)

	sb := titled(researchSandboxCR("alice", researchSession, researchRepo, false), "First read")
	acp := &fakeACPD{sessionExists: true}
	r, dyn := researchTestServer(t, acp, []*unstructured.Unstructured{sb, taken},
		researchPod("alice", sb.GetName(), "10.0.0.9", "Running"))

	if w := doJSON(t, r, http.MethodPost, capturePath(researchSession), ""); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := captureAnnotations(t, dyn, researchSession)[research.NoteAnnotation]; got != "first-read-2.md" {
		t.Errorf("note = %q, want a file of its own", got)
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

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), "")
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

	w := doJSON(t, r, http.MethodPost, capturePath(researchSession), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got, ok := captureAnnotations(t, dyn, researchSession)[research.CaptureErrorAnnotation]; ok {
		t.Errorf("the stale failure survived a fresh capture: %q", got)
	}
}

// The state the conversation reports back, so a member who asked for a
// note can tell "still working" from "never happened" without leaving
// the tab.
func TestSessionStatusReportsAnOwedSave(t *testing.T) {
	sb := researchSandboxCR("alice", researchSession, researchRepo, false)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureAnnotation] = research.Pending{
		Note: "first-read.md", At: time.Now().UTC(),
	}.Encode()
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
	if got.Capturing != "first-read.md" {
		t.Errorf("capturing = %q, want the note in flight", got.Capturing)
	}
}
