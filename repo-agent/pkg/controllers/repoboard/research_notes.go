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

package repoboard

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// Save notes, for a research conversation `factory recipe research`
// started: a revise (revise.go) asks the recipe's notes revise into the
// conversation's session, and its Notes task output is stored on the
// sandbox as the notes draft (revisedNotes). Save to
// research/notes is an apply (verb apply, kind Notes, push-notes): factory
// apply pushes the draft, as it is then, to the member's fork. Both are
// keyed by the sandbox, as an issue's are by its number.

// The notes draft, on the research sandbox.
const (
	AnnotationNotesDraft     = "board.gemini.google.com/notes"
	AnnotationNotesDraftedAt = "board.gemini.google.com/notes-drafted-at"
	AnnotationNotesSaved     = "board.gemini.google.com/notes-saved-at"
)

// notesSandbox is the member's research sandbox a Save notes Request
// names, when the recipe started it.
func notesSandbox(work *workState, spec boardv1alpha1.RequestSpec) *unstructured.Unstructured {
	sb := work.findSandbox(spec.Member, spec.Sandbox)
	if sb == nil || factorycli.ResearchTask(sb.GetAnnotations()) == "" {
		return nil
	}
	return sb
}

// storeNotesDraft stores a revise's notes as the draft: not yet saved.
func (r *Reconciler) storeNotesDraft(ctx context.Context, sb *unstructured.Unstructured, notes, doc string) error {
	annotations := sb.GetAnnotations()
	annotations[AnnotationNotesDraft] = notes
	setOrDelete(annotations, factorycli.AnnotationNotesOutput, doc)
	delete(annotations, AnnotationNotesSaved)
	annotations[AnnotationNotesDraftedAt] = time.Now().UTC().Format(time.RFC3339)
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// notesDraftSandbox is the research sandbox holding the notes draft a
// Save to research/notes pushes, or nil.
func notesDraftSandbox(work *workState, spec boardv1alpha1.RequestSpec) *unstructured.Unstructured {
	if sb := notesSandbox(work, spec); sb != nil && sb.GetAnnotations()[AnnotationNotesDraft] != "" {
		return sb
	}
	return nil
}

// notesDoc is the Notes task output to push: the draft on sb as it is now,
// edits included, under the file name the conversation pinned.
func notesDoc(work *workState, sb *unstructured.Unstructured) (string, error) {
	a := sb.GetAnnotations()
	task := "board-" + sb.GetName()
	if t, err := time.Parse(time.RFC3339, a[AnnotationNotesDraftedAt]); err == nil {
		task = fmt.Sprintf("%s-%d", task, t.Unix())
	}
	url := fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo)
	return factorycli.ComposeNotes(a[factorycli.AnnotationNotesOutput], a[AnnotationNotesDraft], a[research.NoteAnnotation], url, task)
}
