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

	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Update review, from a review's session view: a revise (Request verb
// revise, with the review sandbox) asks the review recipe's review revise
// into the review's session, and the runner posts the Review it writes as
// the member's pending review, replacing the one factory posted before.
// The draft stays GitHub's, as for the review itself.

// reviewRevise reports whether a sandbox-keyed revise names a review
// sandbox (factorycli.ReviewSandboxName) rather than a conversation.
func reviewRevise(work *workState, spec boardv1alpha1.RequestSpec) bool {
	_, ok := factorycli.ReviewPROf(work.findSandbox(spec.Member, spec.Sandbox), work.repo)
	return ok
}

// ensureReviewRevise starts an Update review, as ensureNotesRevise starts
// a Save notes: not while the review itself runs.
func (r *Reconciler) ensureReviewRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	spec := req.Spec
	sb := work.findSandbox(spec.Member, spec.Sandbox)
	pr, ok := factorycli.ReviewPROf(sb, work.repo)
	if !ok {
		return
	}
	key := sandboxReviseKey(spec.Member, spec.Sandbox)
	if r.Factory.IsRunning(key) || r.Factory.IsRunning(reviewKey(work, spec.Member, pr)) {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
		return
	}
	annotations := sb.GetAnnotations()
	session := factorycli.RecordedRunSession(annotations, factorycli.AnnotationReviewRun)
	if session == "" {
		return
	}
	token, err := r.executorToken(ctx, spec.Member)
	if err != nil {
		logger.Error(err, "revise executor has no token", "executor", spec.Member, "sandbox", spec.Sandbox)
		return
	}
	prefix := sandboxRevisePrefix(work.board.Name, spec.Sandbox, spec.Revise)
	runName := fmt.Sprintf("%s%d", prefix, time.Now().Unix())
	if name, ok := r.resumableReviseRun(key, annotations, factorycli.AnnotationReviewRun, prefix, req); ok {
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
		Session:     session,
		GithubToken: token,
		RunName:     runName,
		PostReview:  true,
	}) {
		logger.Info("launched factory recipe revise", "sandbox", sb.GetName(), "revise", spec.Revise, "member", spec.Member)
	}
}

// settleReviewRevise is served by the revise's result since the click:
// the review it posted, or its failure.
func (r *Reconciler) settleReviewRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	sb := work.findSandbox(spec.Member, spec.Sandbox)
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
		if sb == nil {
			return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
		}
		// Posted again: pending, even when the member had submitted the
		// one before.
		if err := r.markReviewPending(ctx, sb, work.board.Name); err != nil {
			log.FromContext(ctx).Error(err, "unable to mark review pending", "sandbox", sb.GetName())
			return stillPending
		}
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	case sb == nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoReview",
			message: fmt.Sprintf("there is no review in %s to update", spec.Sandbox),
		}
	}
	return pendingOutcome(req, now)
}
