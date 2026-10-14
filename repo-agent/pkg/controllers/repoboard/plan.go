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

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// The plan loop: agent PLAN -> human APPROVE/REJECT -> agent FIX. `factory
// recipe plan` runs in the issue's fix sandbox and writes nothing to
// GitHub; the draft lives on the sandbox (factorycli.AnnotationPlanOutput)
// until the member applies its run action — "Fix with this plan" files a
// fix Request whose plan input (fix.yaml's input from: Plan) is the draft,
// and the fix publishes it as the PR description's Plan section (the
// durable record).

// ensurePlan drives one issue's plan state machine: harvest a finished
// run's plan, or launch a fresh one within limits.
func (r *Reconciler) ensurePlan(ctx context.Context, work *workState, req planRequest) {
	logger := log.FromContext(ctx)
	name := work.fixSandboxName(req.issue)
	sb := work.issueSandbox(req.member, req.issue)
	if sb != nil {
		name = sb.GetName()
	}
	key := planKey(work, req.member, req.issue)

	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	if annotations[AnnotationPlannedAt] != "" && !clickedSince(annotations[AnnotationPlannedAt], req.since) {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok && !planResultStale(annotations, res.FinishedAt) && !req.since.After(res.FinishedAt) {
		if res.Err == nil && sb != nil {
			if doc := factorycli.HarvestedOutput("Plan", res.Output); doc != "" {
				annotations[factorycli.AnnotationPlanOutput] = doc
				// A new plan has not been posted, nor approved.
				delete(annotations, factorycli.AnnotationPlanApplied)
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

	runName := r.resumableRun(key, annotations, factorycli.AnnotationPlanRun, planRunName(work.board.Name, req.issue),
		AnnotationPlannedAt, AnnotationPlanRejected)
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	opts := r.recipeOptions(work, "plan", req.member, name, fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, req.issue), token, runName)
	opts.Inputs = req.inputs
	if r.Factory.StartRecipe(key, opts) {
		logger.Info("launched factory recipe plan", "issue", req.issue, "board", work.board.Name, "executor", req.member)
	}
}

// planRunName is what a plan's task is recorded under in the sandbox
// (factory --run-name), to read its result by. factory runs a name once,
// so it names one launch: a failed plan is retried, not returned. A run
// this controller did not start (a restart's) is resumed under its
// recorded name instead (resumableRun). Identifiers only: anyone in the
// sandbox can read it.
func planRunName(board string, issue int) string {
	return fmt.Sprintf("plan/%s/%d/%d", board, issue, time.Now().Unix())
}

// resumableRun is the run name to invoke factory with for key: the run
// recorded on the sandbox under runKey, when this controller has no
// result for key (it restarted, or another replica started the run) and
// the run started after every marker — a run older than the last harvest
// or reject is not the one wanted. Invoking factory with it
// follows the run to its end, or reads its result if it has one; a run
// that failed fails again at once, and the retry after the backoff, with
// a result now in memory, gets fresh. Otherwise, fresh.
func (r *Reconciler) resumableRun(key string, annotations map[string]string, runKey, fresh string, markers ...string) string {
	if _, ok := r.Factory.LastResult(key); ok {
		return fresh
	}
	var since time.Time
	for _, m := range markers {
		if at, err := time.Parse(time.RFC3339, annotations[m]); err == nil && at.After(since) {
			since = at
		}
	}
	if name, ok := factorycli.RecordedRunName(annotations, runKey, since); ok {
		return name
	}
	return fresh
}

// planResultStale reports whether a remembered plan result predates a
// reject and must not be re-recorded: after a reject clears the draft, the
// old result would otherwise resurrect it.
func planResultStale(annotations map[string]string, finishedAt time.Time) bool {
	at, err := time.Parse(time.RFC3339, annotations[AnnotationPlanRejected])
	return err == nil && finishedAt.Before(at)
}

// AnnotationPlanRejected tombstones a rejected plan draft, as
// AnnotationTriageRejected does a triage's.
var AnnotationPlanRejected = factorycli.RejectedAnnotation(factorycli.AnnotationPlanRun)

// planKey is the runner key of an issue's plans.
func planKey(work *workState, member string, issue int) string {
	return fmt.Sprintf("%s/plan-%s-%d", member, work.repo, issue)
}
