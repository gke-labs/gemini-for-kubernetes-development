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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// Save notes, for a research conversation `factory recipe research`
// started: a revise (Request verb revise, with the sandbox) asks the
// recipe's notes revise into the conversation's session, and its Notes
// task output is stored on the sandbox as the notes draft. Save to
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

// sandboxReviseKey is the runner key of a revise filed for a sandbox (a
// conversation's Save notes, a review's Update review): one at a time, as
// for a plan.
func sandboxReviseKey(member, sandbox string) string {
	return fmt.Sprintf("%s/revise-%s", member, sandbox)
}

// sandboxRevisePrefix is the run names of a sandbox's revise, before the
// time.
func sandboxRevisePrefix(board, sandbox, revise string) string {
	return fmt.Sprintf("revise/%s/%s/%s/", board, sandbox, revise)
}

// ensureNotesRevise starts a Save notes, as ensureRevises starts an issue's
// revise.
func (r *Reconciler) ensureNotesRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	spec := req.Spec
	key := sandboxReviseKey(spec.Member, spec.Sandbox)
	if r.Factory.IsRunning(key) {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
		return
	}
	sb := notesSandbox(work, spec)
	if sb == nil {
		return
	}
	token, err := r.executorToken(ctx, spec.Member)
	if err != nil {
		logger.Error(err, "revise executor has no token", "executor", spec.Member, "sandbox", spec.Sandbox)
		return
	}
	annotations := sb.GetAnnotations()
	prefix := sandboxRevisePrefix(work.board.Name, spec.Sandbox, spec.Revise)
	runName := fmt.Sprintf("%s%d", prefix, time.Now().Unix())
	if name, ok := r.resumableReviseRun(key, annotations, factorycli.ResearchRunAnnotation, prefix, req); ok {
		runName = name
	}
	if err := r.wake(ctx, sb); err != nil {
		logger.Error(err, "unable to wake the sandbox for a revise", "sandbox", sb.GetName())
		return
	}
	if r.Factory.StartRevise(key, factorycli.ReviseOptions{
		Namespace:   spec.Member,
		SandboxName: sb.GetName(),
		Revise:      spec.Revise,
		Session:     factorycli.ResearchTask(annotations),
		GithubToken: token,
		RunName:     runName,
	}) {
		logger.Info("launched factory recipe revise", "sandbox", sb.GetName(), "revise", spec.Revise, "member", spec.Member)
	}
}

// settleNotesRevise is served by the revise's result since the click: its
// notes, stored as the draft, or its failure.
func (r *Reconciler) settleNotesRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	sb := notesSandbox(work, spec)
	key := sandboxReviseKey(spec.Member, spec.Sandbox)
	res, ran := r.Factory.LastResult(key)
	ran = ran && res.FinishedAt.After(req.CreationTimestamp.Time)
	switch {
	case ran && res.Err != nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "ReviseFailed",
			message: clipMessage(strings.TrimSpace(lastLines(res.Output, 3)+"\n"+res.Err.Error()), 400),
		}
	case ran:
		notes := factorycli.ExtractNotes(res.Output)
		if notes == "" {
			return requestOutcome{
				phase:   boardv1alpha1.RequestFailed,
				reason:  "NoNotes",
				message: "the revise ended without notes",
			}
		}
		if sb == nil {
			// Written, and the conversation went since.
			return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
		}
		if err := r.storeNotesDraft(ctx, sb, notes, factorycli.NotesTaskOutput(res.Output)); err != nil {
			log.FromContext(ctx).Error(err, "storing the notes draft", "sandbox", sb.GetName())
			return stillPending
		}
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	case sb == nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoConversation",
			message: fmt.Sprintf("there is no research conversation in %s to save notes from", spec.Sandbox),
		}
	}
	return pendingOutcome(req, now)
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
