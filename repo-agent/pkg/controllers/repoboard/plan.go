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
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// The plan loop: agent PLAN -> human REFINE -> agent UPDATE_PLAN -> human
// APPROVE/REJECT -> agent FIX. `factory plan` runs in the issue's fix
// sandbox and writes nothing to GitHub; the draft lives on the sandbox
// (AnnotationPlanDraft) until the member approves — approval launches the
// fix with --with-plan, which publishes the plan as the PR description's
// Plan section (the durable record). Refinement rounds are edge-triggered:
// a feedback stamp newer than the last planned-at re-runs the planner
// against the previous plan.

// ensurePlan drives one issue's plan state machine: harvest a finished
// run's plan, or launch (a fresh plan or a refinement) within limits.
func (r *Reconciler) ensurePlan(ctx context.Context, work *workState, req planRequest) {
	logger := log.FromContext(ctx)
	name := work.fixSandboxName(req.issue)
	sb := work.findSandbox(req.member, name)
	key := fmt.Sprintf("%s/plan-%s-%d", req.member, work.repo, req.issue)

	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	needFresh := annotations[AnnotationPlannedAt] == ""
	feedbackAt, feedbackErr := time.Parse(time.RFC3339, annotations[AnnotationPlanFeedbackAt])
	needRefine := false
	if feedbackErr == nil {
		plannedAt, err := time.Parse(time.RFC3339, annotations[AnnotationPlannedAt])
		needRefine = err != nil || feedbackAt.After(plannedAt)
	}
	if !needFresh && !needRefine {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok && !planResultStale(annotations, res.FinishedAt) {
		if res.Err == nil && sb != nil {
			if plan := factorycli.ExtractPlan(res.Output); plan != "" {
				annotations[AnnotationPlanDraft] = plan
				annotations[AnnotationPlannedAt] = time.Now().UTC().Format(time.RFC3339)
				annotations[AnnotationBoard] = work.board.Name
				annotations[AnnotationExecutor] = req.member
				sb.SetAnnotations(annotations)
				if err := r.Update(ctx, sb); err != nil {
					logger.Error(err, "unable to store plan draft", "issue", req.issue)
				}
				return
			}
		}
		// Error, or a success with no recognizable plan: back off rather
		// than hot-looping the agent (triage semantics — the request
		// stands, retried at most once per backoff window).
		if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		logger.Info("plan deferred: board at maxActive", "issue", req.issue, "limit", maxActive(work.board))
		return
	}
	token, err := r.executorToken(ctx, req.member)
	if err != nil {
		logger.Error(err, "plan executor has no token", "executor", req.member, "issue", req.issue)
		return
	}
	if err := r.ensureFactoryUserSecret(ctx, req.member, req.member, ""); err != nil {
		logger.Error(err, "unable to sync factory-user secret", "namespace", req.member)
		return
	}

	feedback := ""
	if needRefine {
		feedback = annotations[AnnotationPlanFeedback]
	}
	r.stampUnpaused(ctx, sb)
	if r.Factory.StartPlan(key, factorycli.PlanOptions{
		Namespace:         req.member,
		IssueURL:          fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, req.issue),
		Feedback:          feedback,
		Image:             work.board.Spec.Sandbox.Image,
		WorkspaceDiskSize: work.board.Spec.Sandbox.DiskSize,
		GithubToken:       token,
	}) {
		logger.Info("launched factory plan", "issue", req.issue, "board", work.board.Name, "executor", req.member, "refine", needRefine)
	}
}

// planResultStale reports whether a remembered plan result predates a
// member action (feedback or reject) and must not be re-recorded: after a
// reject clears the draft, the old result would otherwise resurrect it.
func planResultStale(annotations map[string]string, finishedAt time.Time) bool {
	for _, key := range []string{AnnotationPlanFeedbackAt, AnnotationPlanRejected} {
		if at, err := time.Parse(time.RFC3339, annotations[key]); err == nil && finishedAt.Before(at) {
			return true
		}
	}
	return false
}

// resumePlans re-drives refinement rounds after restarts: the feedback
// stamp survives on the sandbox while the mailbox entry (which only covers
// the fresh-plan bootstrap) is long gone.
func (r *Reconciler) resumePlans(ctx context.Context, work *workState) {
	prefix := "fix-" + work.repo + "-"
	for _, sb := range work.sandboxes {
		name := sb.GetName()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		annotations := sb.GetAnnotations()
		if annotations[AnnotationPlanFeedbackAt] == "" {
			continue
		}
		feedbackAt, err := time.Parse(time.RFC3339, annotations[AnnotationPlanFeedbackAt])
		if err != nil {
			continue
		}
		if plannedAt, err := time.Parse(time.RFC3339, annotations[AnnotationPlannedAt]); err == nil && !feedbackAt.After(plannedAt) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil {
			continue
		}
		r.ensurePlan(ctx, work, planRequest{issue: n, member: sb.GetNamespace()})
	}
}
