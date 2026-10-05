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

// The second half of writing a conversation down.
//
// The API sent the turn that writes the note; this pushes what that turn
// produced to the member's fork. It is here and not there for two
// reasons that happen to point the same way. The push needs the factory
// CLI, which only this image carries. And it cannot start until the turn
// ends, which is minutes away — a wait the API cannot hold, being
// replicated and stateless, but which this loop already does for
// everything else: the reconcile IS the retry.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// researchCaptureTTL bounds how long a save stays owed.
//
// Measured from when the prompt was sent, not from the sandbox's
// creation as the kickoff's TTL is: a capture can be asked for on a
// week-old session, so the sandbox's age says nothing about this wait.
// An hour is long for a turn and short for a session, which is the gap
// this has to sit in.
const researchCaptureTTL = time.Hour

// researchCaptureTimeout bounds asking one session whether its turn has
// finished. Short: it is a status read, it runs inline on the reconcile
// loop, and a sandbox whose acpd is wedged must not hold the board up.
const researchCaptureTimeout = 10 * time.Second

// saveNotesKey is the runner's single-flight key for the push.
//
// Distinct from researchKey's, and it has to be: they name invocations
// against the same sandbox, and sharing a key would have a save in
// flight read as the create still running.
func saveNotesKey(member, repo, sessionID string) string {
	return researchKey(member, repo, sessionID) + "/save-notes"
}

// completeResearchSaves pushes the notes of every session whose capture
// turn has finished.
//
// Driven from the sandboxes, like the kickoff pass and for the same
// reason: the request outlives whichever API replica took it, and the
// sandbox is the only thing that is still around by the time the work
// can be done.
func (r *Reconciler) completeResearchSaves(ctx context.Context, work *workState) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		if sb.GetLabels()[sandboxTypeLabel] != researchSandboxType {
			continue
		}
		annotations := sb.GetAnnotations()
		raw := annotations[research.CaptureAnnotation]
		if raw == "" {
			continue
		}
		pending, ok := research.DecodePending(raw)
		if !ok {
			// Unreadable, so there is no clock on it and it could never
			// expire. Dropping it is the only way it ends.
			logger.Info("dropping an unreadable research capture", "sandbox", sb.GetName())
			r.finishResearchSave(ctx, sb, fmt.Errorf("the pending save could not be read"))
			continue
		}
		if err := r.completeResearchSave(ctx, work, sb, pending); err != nil {
			// Expected for most of the wait: the turn is still running,
			// or the pod is not answering. Logged at info, and only given
			// up on when the TTL runs out.
			logger.Info("research notes not saved yet", "sandbox", sb.GetName(), "note", pending.Note, "reason", err.Error())
			if time.Since(pending.At) > researchCaptureTTL {
				r.finishResearchSave(ctx, sb, err)
			}
		}
	}
}

// completeResearchSave advances one owed save by whatever step is
// available, reporting why it could not finish.
func (r *Reconciler) completeResearchSave(ctx context.Context, work *workState, sb *unstructured.Unstructured, pending research.Pending) error {
	sessionID := sb.GetAnnotations()[researchSessionIDAnnotation]
	if sessionID == "" {
		return fmt.Errorf("the sandbox carries no session id")
	}
	repo := sb.GetAnnotations()["repo"]
	if repo == "" {
		repo = work.repo
	}
	member := sb.GetNamespace()
	key := saveNotesKey(member, repo, sessionID)

	// A finished push is the receipt. Harvested before anything else so
	// that the common path — the run this pass is here to collect — does
	// not first go and poll a session that has nothing left to say.
	//
	// The timestamp comparison is what makes the key reusable. A session
	// can be captured any number of times, every capture uses this same
	// key, and without it the result of the first save would be read as
	// the second one's the instant it was asked for.
	if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(pending.At) {
		var cause error
		if res.Err != nil {
			cause = fmt.Errorf("pushing the notes: %w", res.Err)
		} else {
			log.FromContext(ctx).Info("saved research notes", "sandbox", sb.GetName(), "note", pending.Note)
		}
		// The save is no longer owed either way. A push that failed is
		// not retried: whatever refused it — no fork, no permission, no
		// file written — will refuse the next one too, and the error
		// annotation is what turns that into something the member can
		// act on.
		if err := r.recordResearchSaveResult(ctx, sb, cause); err != nil {
			return fmt.Errorf("recording the save's outcome: %w", err)
		}
		return nil
	}
	if r.Factory.IsRunning(key) {
		return nil // in flight; this pass has nothing to add
	}
	// A paused sandbox has no engine to finish the turn and no pod to
	// exec the push into. Un-pausing is the member's call; the save waits
	// for it, and expires with the TTL if it never comes.
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		return fmt.Errorf("the session is paused")
	}

	if err := r.researchTurnFinished(ctx, sb, sessionID); err != nil {
		return err
	}

	token, err := r.executorToken(ctx, member)
	if err != nil {
		return fmt.Errorf("reading the member's GitHub token: %w", err)
	}
	if r.Factory.StartSaveNotes(key, factorycli.SaveNotesOptions{
		Namespace: member,
		RepoURL:   fmt.Sprintf("https://github.com/%s/%s", work.owner, repo),
		SessionID: sessionID,
		// What the prompt was told to write, not what the session is
		// called now: a rename between the ask and the push must not
		// send the save looking for a file nothing wrote to.
		Note: pending.Note,
		// The whole point of the separate verb: the token is held for
		// one exec by a command the conversation cannot see, rather than
		// written onto a PVC an auto-approving agent can read.
		GithubToken: token,
	}) {
		log.FromContext(ctx).Info("launched factory research save-notes", "session", sessionID, "note", pending.Note)
	}
	return nil
}

// researchTurnFinished reports nil once the conversation is no longer
// working, and an error describing the wait otherwise.
//
// Busy is set synchronously by the prompt that started the turn, so a
// session that reports itself idle after a capture has genuinely
// finished writing — there is no window where the turn has been accepted
// but not yet counted. Waiting is a refinement of Busy, so a turn
// stopped on a permission request is correctly still "working": pushing
// then would save a half-written note.
func (r *Reconciler) researchTurnFinished(ctx context.Context, sb *unstructured.Unstructured, sessionID string) error {
	pod, err := r.researchPod(ctx, sb.GetNamespace(), sb.GetName())
	if err != nil {
		return err
	}
	if pod == nil {
		return fmt.Errorf("no running pod")
	}
	ctx, cancel := context.WithTimeout(ctx, researchCaptureTimeout)
	defer cancel()

	session, err := researchACPD(ctx, r.ACPD, pod).GetSession(ctx, sessionID)
	switch {
	case err == nil && session.Busy:
		return fmt.Errorf("the conversation is still working")
	case err == nil:
		return nil
	case errors.Is(err, acpd.ErrNotFound):
		// The engine is gone — acpd restarted, or the pod did. Whatever
		// the turn managed to write is on the PVC and is the only copy
		// there will ever be, so push it rather than wait for a session
		// that is not coming back. A note that was never written is not
		// silently accepted: save-notes fails loudly on a missing file,
		// which surfaces here as the save's error.
		return nil
	default:
		return err
	}
}

// recordResearchSaveResult clears the owed save, recording the failure
// if there was one. It returns only the error from the write itself, so
// a caller that cannot record the outcome retries next pass rather than
// treating the save as settled.
func (r *Reconciler) recordResearchSaveResult(ctx context.Context, sb *unstructured.Unstructured, cause error) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	delete(annotations, research.CaptureAnnotation)
	if cause != nil {
		annotations[research.CaptureErrorAnnotation] = cause.Error()
	} else {
		delete(annotations, research.CaptureErrorAnnotation)
	}
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// finishResearchSave gives up on an owed save, visibly.
//
// The error annotation is the whole point. A member asked for a note in
// the conversation and was told it was coming; if it is not, the place
// they will look is the same conversation, and silence there sends them
// to the fork to hunt for a file that was never pushed.
func (r *Reconciler) finishResearchSave(ctx context.Context, sb *unstructured.Unstructured, cause error) {
	if err := r.recordResearchSaveResult(ctx, sb, cause); err != nil {
		log.FromContext(ctx).Error(err, "unable to record a failed research save", "sandbox", sb.GetName())
	}
}
