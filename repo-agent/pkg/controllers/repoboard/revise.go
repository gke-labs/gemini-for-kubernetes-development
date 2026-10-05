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
)

// Revises: a plan draft rewritten from the conversation a member
// continued in the plan's agent session (Use as plan). `factory recipe
// revise` asks the recipe's revise into that session, as its next turn,
// and its result is a Plan task output, stored as the draft as a plan's
// is. It posts nothing. The Request stands until the new draft is stored,
// or the revise fails; a failure is not retried — the session may be
// mid-turn, and the button is the retry.

// reviseKey is the runner key of an issue's revises. One per issue, not
// per revise: two rewrites of one draft at once would race to store it.
func reviseKey(work *workState, member string, issue int) string {
	return fmt.Sprintf("%s/revise-plan-%s-%d", member, work.repo, issue)
}

// planKey is the runner key of an issue's plans.
func planKey(work *workState, member string, issue int) string {
	return fmt.Sprintf("%s/plan-%s-%d", member, work.repo, issue)
}

// reviseRunName is what a revise's task is recorded under in the sandbox,
// as planRunName is for a plan's.
func reviseRunName(board string, issue int, revise string) string {
	return fmt.Sprintf("revise/%s/%d/%s/%d", board, issue, revise, time.Now().Unix())
}

// reviseDraftSandbox is the member's issue sandbox holding the plan draft
// a revise rewrites, or nil.
func reviseDraftSandbox(work *workState, spec boardv1alpha1.RequestSpec) *unstructured.Unstructured {
	sb := work.issueSandbox(spec.Member, spec.Number)
	if sb == nil || sb.GetAnnotations()[AnnotationPlanDraft] == "" {
		return nil
	}
	return sb
}

// reviseSession is the session to revise in: the one the draft's task
// output came from, else the recorded plan run's.
func reviseSession(annotations map[string]string) string {
	if s := factorycli.TaskOutputSession("Plan", annotations[factorycli.AnnotationPlanOutput]); s != "" {
		return s
	}
	return factorycli.RecordedRunSession(annotations, factorycli.AnnotationPlanRun)
}

// ensureRevises starts the revise each Request asks for, once: not while
// it or a plan of the issue runs, and not again once it has a result —
// settle reads that.
func (r *Reconciler) ensureRevises(ctx context.Context, work *workState, reqs []*boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	for _, req := range reqs {
		spec := req.Spec
		if spec.Revise == "" {
			continue
		}
		key := reviseKey(work, spec.Member, spec.Number)
		if r.Factory.IsRunning(key) || r.Factory.IsRunning(planKey(work, spec.Member, spec.Number)) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
			continue
		}
		sb := reviseDraftSandbox(work, spec)
		if sb == nil {
			continue
		}
		token, err := r.executorToken(ctx, spec.Member)
		if err != nil {
			logger.Error(err, "revise executor has no token", "executor", spec.Member, "issue", spec.Number)
			continue
		}
		annotations := sb.GetAnnotations()
		runName := reviseRunName(work.board.Name, spec.Number, spec.Revise)
		if name, ok := r.resumableRevise(key, annotations, req); ok {
			runName = name
		}
		if err := r.wake(ctx, sb); err != nil {
			logger.Error(err, "unable to wake the sandbox for a revise", "sandbox", sb.GetName())
			continue
		}
		if r.Factory.StartRevise(key, factorycli.ReviseOptions{
			Namespace:   spec.Member,
			SandboxName: sb.GetName(),
			Revise:      spec.Revise,
			Session:     reviseSession(annotations),
			GithubToken: token,
			RunName:     runName,
		}) {
			logger.Info("launched factory recipe revise", "issue", spec.Number, "revise", spec.Revise, "member", spec.Member)
		}
	}
}

// resumableRevise is the revise run recorded on the sandbox for req, when
// this controller has no result for key (it restarted): one started since
// the click. Invoking factory with its name follows it to its end.
func (r *Reconciler) resumableRevise(key string, annotations map[string]string, req *boardv1alpha1.Request) (string, bool) {
	if _, ok := r.Factory.LastResult(key); ok {
		return "", false
	}
	// A second early: the click and the launch can share one.
	since := req.CreationTimestamp.Add(-time.Second)
	name, ok := factorycli.RecordedRunName(annotations, factorycli.AnnotationPlanRun, since)
	if !ok || !strings.HasPrefix(name, fmt.Sprintf("revise/%s/%d/%s/", req.Spec.Board, req.Spec.Number, req.Spec.Revise)) {
		return "", false
	}
	return name, true
}

// wake scales a paused sandbox back up, stamped as unpaused so the pause
// pass leaves it to boot: factory recipe revise reaches the sandbox as it
// is, and does not wake it as a recipe's start does.
func (r *Reconciler) wake(ctx context.Context, sb *unstructured.Unstructured) error {
	replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	if err != nil || !found || replicas != 0 {
		return nil
	}
	annotations := sb.GetAnnotations()
	annotations[AnnotationUnpausedAt] = time.Now().UTC().Format(time.RFC3339)
	sb.SetAnnotations(annotations)
	if err := unstructured.SetNestedField(sb.Object, int64(1), "spec", "replicas"); err != nil {
		return err
	}
	return r.Update(ctx, sb)
}

// settleRevise is served by the revise's result since the click: its plan,
// stored as the draft, or its failure.
func (r *Reconciler) settleRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	if spec.Revise == "" {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "Malformed",
			message: "a revise names the recipe revise to run",
		}
	}
	sb := reviseDraftSandbox(work, spec)
	key := reviseKey(work, spec.Member, spec.Number)
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
		plan := factorycli.ExtractPlan(res.Output)
		if plan == "" {
			return requestOutcome{
				phase:   boardv1alpha1.RequestFailed,
				reason:  "NoPlan",
				message: "the revise ended without a plan",
			}
		}
		if sb == nil {
			// Revised, and the draft went since (rejected, or approved).
			return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
		}
		if err := r.storeRevisedPlan(ctx, work, sb, spec.Member, plan, factorycli.PlanTaskOutput(res.Output)); err != nil {
			log.FromContext(ctx).Error(err, "storing the revised plan", "sandbox", sb.GetName())
			return stillPending
		}
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	case sb == nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoDraft",
			message: fmt.Sprintf("there is no plan draft on #%d to revise", spec.Number),
		}
	}
	return pendingOutcome(req, now)
}

// storeRevisedPlan stores a revise's plan as the draft, as ensurePlan
// stores a plan's: not yet posted.
func (r *Reconciler) storeRevisedPlan(ctx context.Context, work *workState, sb *unstructured.Unstructured, member, plan, doc string) error {
	annotations := sb.GetAnnotations()
	annotations[AnnotationPlanDraft] = plan
	setOrDelete(annotations, factorycli.AnnotationPlanOutput, doc)
	delete(annotations, factorycli.AnnotationPlanCommented)
	annotations[AnnotationPlannedAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = work.board.Name
	annotations[AnnotationExecutor] = member
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}
