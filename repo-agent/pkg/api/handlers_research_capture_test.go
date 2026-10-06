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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// savePath is Save notes on researchSession's conversation.
func savePath() string { return recipeConversation() + "/revise" }

// titled is a sandbox that has been named, which is every session past
// its first turn and the only kind whose note gets a readable name.
func titled(sb *unstructured.Unstructured, title string) *unstructured.Unstructured {
	annotations := sb.GetAnnotations()
	annotations[research.TitleAnnotation] = title
	sb.SetAnnotations(annotations)
	return sb
}

// recipeSandboxFor is a recipe research sandbox for any session.
func recipeSandboxFor(sessionID string) *unstructured.Unstructured {
	sb := researchSandboxCR("alice", sessionID, researchRepo, false)
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: factorycli.ResearchRunName(sessionID), Task: researchTaskID, StartedAt: time.Now().UTC(),
		Revises: []factorycli.RecordedRevise{{ID: notesRevise, Label: "Save notes"}},
	})
	annotations := sb.GetAnnotations()
	annotations[factorycli.ResearchRunAnnotation] = string(run)
	sb.SetAnnotations(annotations)
	return sb
}

// A session nobody has named yet still has an id, and an id is a worse
// file name than a name but a much better one than an empty path
// component.
func TestSaveNotesFallsBackToTheSessionIDWhenUnnamed(t *testing.T) {
	r, dyn := recipeResearchServerWith(t, &fakeSessions{}, true, recipeSandboxFor(researchSession))
	seedNotesBoard(t, dyn)

	if w := doJSON(t, r, http.MethodPost, savePath(), `{"revise":"notes"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := notesSandboxAnnotations(t, dyn)[research.NoteAnnotation]; got != researchSession+".md" {
		t.Errorf("note = %q, want the session id", got)
	}
}

// A named session saves under its name.
func TestSaveNotesNamesTheNoteAfterTheSession(t *testing.T) {
	r, dyn := recipeResearchServerWith(t, &fakeSessions{}, true,
		titled(recipeSandboxFor(researchSession), "Where the retry loop terminates"))
	seedNotesBoard(t, dyn)

	if w := doJSON(t, r, http.MethodPost, savePath(), `{"revise":"notes"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := notesSandboxAnnotations(t, dyn)[research.NoteAnnotation]; got != "where-the-retry-loop-terminates.md" {
		t.Errorf("note = %q, want the session's name", got)
	}
}

// Two sessions can genuinely want one name — every canned "first read"
// of a repository is called the same thing — and the save overwrites
// what is on the branch, so the second one sharing it would be the
// second one replacing the first one's notes.
func TestSaveNotesDoesNotTakeAnotherSessionsNote(t *testing.T) {
	const otherSession = "0c7b3d9a-1111-2222-3333-444455556666"
	taken := titled(recipeSandboxFor(otherSession), "first read")
	annotations := taken.GetAnnotations()
	annotations[research.NoteAnnotation] = "first-read.md"
	taken.SetAnnotations(annotations)

	r, dyn := recipeResearchServerWith(t, &fakeSessions{}, true,
		titled(recipeSandboxFor(researchSession), "First read"), taken)
	seedNotesBoard(t, dyn)

	if w := doJSON(t, r, http.MethodPost, savePath(), `{"revise":"notes"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := notesSandboxAnnotations(t, dyn)[research.NoteAnnotation]; got != "first-read-2.md" {
		t.Errorf("note = %q, want a file of its own", got)
	}
}
