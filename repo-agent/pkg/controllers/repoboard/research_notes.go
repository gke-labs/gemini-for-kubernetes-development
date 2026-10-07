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
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Save notes, for a research conversation `factory recipe research`
// started: a revise (revise.go) asks the recipe's notes revise into the
// conversation's session, and its Notes task output is stored on the
// sandbox as the notes draft (revisedNotes). Save to
// research/notes is an apply (apply.go) of the research run's push-notes:
// factory apply pushes the draft, as it is then, to the member's fork.

// When the notes draft on the research sandbox
// (factorycli.AnnotationNotesOutput) was stored.
const AnnotationNotesDraftedAt = "board.gemini.google.com/notes-drafted-at"

// storeNotesDraft stores a revise's Notes task output as the draft: not
// yet saved.
func (r *Reconciler) storeNotesDraft(ctx context.Context, sb *unstructured.Unstructured, doc string) error {
	annotations := sb.GetAnnotations()
	annotations[factorycli.AnnotationNotesOutput] = doc
	delete(annotations, factorycli.AnnotationNotesApplied)
	annotations[AnnotationNotesDraftedAt] = time.Now().UTC().Format(time.RFC3339)
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}
