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

// Revises, whichever recipe's: a member clicks one of the revises the
// run recorded on a sandbox offers (Update plan, Save notes, Update
// review, Iterate), and `factory recipe revise` asks it into that run's
// agent session, as its next turn. The Request is keyed by the sandbox;
// the run is the newest there whose recipe offers the revise. What the
// revise wrote is the run's kind's to keep (reviseHooks): a plan or notes
// draft stored, a review posted as pending, a fix's replies posted. The
// Request stands until then, or until the revise fails; a failure is not
// retried — the session may be mid-turn, and the button is the retry.

// reviseHook is what a revise of a run of one kind does beyond running.
// Every field may be nil.
type reviseHook struct {
	// busy is whether the run's own task is running in sb: the revise
	// waits for it.
	busy func(r *Reconciler, work *workState, member string, sb *unstructured.Unstructured) bool
	// missing is why sb has nothing to revise yet, "" when it has.
	missing func(sb *unstructured.Unstructured) string
	// options adjusts how the revise runs.
	options func(opts *factorycli.ReviseOptions, sb *unstructured.Unstructured)
	// done settles a revise that ran. Unset, it succeeded.
	done func(r *Reconciler, ctx context.Context, work *workState, spec boardv1alpha1.RequestSpec, sb *unstructured.Unstructured, res factorycli.Result) requestOutcome
}

// reviseHooks are by the kind of task output the run writes. A kind with
// none is revised and kept nowhere but its session.
var reviseHooks = map[string]reviseHook{
	"Plan": {
		busy: func(r *Reconciler, work *workState, member string, sb *unstructured.Unstructured) bool {
			issue, ok := factorycli.IssueOf(sb, work.repo)
			return ok && r.Factory.IsRunning(planKey(work, member, issue))
		},
		missing: func(sb *unstructured.Unstructured) string {
			if factorycli.PlanDraft(sb.GetAnnotations()) == "" {
				return "there is no plan draft to revise"
			}
			return ""
		},
		// The session the draft came from, which a member may have
		// continued in.
		options: func(opts *factorycli.ReviseOptions, sb *unstructured.Unstructured) {
			if s := factorycli.TaskOutputSession("Plan", sb.GetAnnotations()[factorycli.AnnotationPlanOutput]); s != "" {
				opts.Session = s
			}
		},
		done: (*Reconciler).revisedPlan,
	},
	"Change": {
		busy: func(r *Reconciler, work *workState, member string, sb *unstructured.Unstructured) bool {
			issue, ok := factorycli.IssueOf(sb, work.repo)
			return ok && r.Factory.IsRunning(fixKey(work, member, issue))
		},
		// A follow-up is an agent turn and a push, not a rewrite of a
		// draft: it has the time a fix's turn takes, and its replies and
		// report are posted on the PR.
		options: func(opts *factorycli.ReviseOptions, _ *unstructured.Unstructured) {
			opts.Timeout, opts.PostReplies = 45*time.Minute, true
		},
	},
	"Review": {
		busy: func(r *Reconciler, work *workState, member string, sb *unstructured.Unstructured) bool {
			pr, ok := factorycli.ReviewPROf(sb, work.repo)
			return ok && r.Factory.IsRunning(reviewKey(work, member, pr))
		},
		// Posted as the member's pending review, replacing the one
		// factory posted before.
		options: func(opts *factorycli.ReviseOptions, _ *unstructured.Unstructured) {
			opts.PostReview = true
		},
		done: (*Reconciler).revisedReview,
	},
	"Notes": {
		done: (*Reconciler).revisedNotes,
	},
}

// sandboxReviseKey is the runner key of a sandbox's revises: one at a
// time, as two rewrites of one draft at once would race to store it.
func sandboxReviseKey(member, sandbox string) string {
	return fmt.Sprintf("%s/revise-%s", member, sandbox)
}

// sandboxRevisePrefix is the run names of a sandbox's revise, before the
// time.
func sandboxRevisePrefix(board, sandbox, revise string) string {
	return fmt.Sprintf("revise/%s/%s/%s/", board, sandbox, revise)
}

// planKey is the runner key of an issue's plans.
func planKey(work *workState, member string, issue int) string {
	return fmt.Sprintf("%s/plan-%s-%d", member, work.repo, issue)
}

// reviseTarget is the sandbox a revise Request names, the run there that
// offers its revise, and that run's kind's hook; false when there is
// none.
func reviseTarget(work *workState, spec boardv1alpha1.RequestSpec) (*unstructured.Unstructured, factorycli.RecordedRun, reviseHook, bool) {
	sb := work.findSandbox(spec.Member, spec.Sandbox)
	if sb == nil {
		return nil, factorycli.RecordedRun{}, reviseHook{}, false
	}
	run, ok := factorycli.ReviseRun(sb.GetAnnotations(), spec.Revise)
	if !ok {
		return nil, factorycli.RecordedRun{}, reviseHook{}, false
	}
	return sb, run, reviseHooks[run.Kind], true
}

// ensureRevises starts the revise each Request asks for, once: not while
// it, another revise of the sandbox or the run's own task runs, and not
// again once it has a result — settle reads that.
func (r *Reconciler) ensureRevises(ctx context.Context, work *workState, reqs []*boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	for _, req := range reqs {
		spec := req.Spec
		if spec.Revise == "" || spec.Sandbox == "" {
			continue
		}
		sb, run, hook, ok := reviseTarget(work, spec)
		if !ok {
			continue
		}
		key := sandboxReviseKey(spec.Member, spec.Sandbox)
		if r.Factory.IsRunning(key) || (hook.busy != nil && hook.busy(r, work, spec.Member, sb)) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
			continue
		}
		if hook.missing != nil && hook.missing(sb) != "" {
			continue
		}
		token, err := r.executorToken(ctx, spec.Member)
		if err != nil {
			logger.Error(err, "revise executor has no token", "executor", spec.Member, "sandbox", spec.Sandbox)
			continue
		}
		prefix := sandboxRevisePrefix(work.board.Name, spec.Sandbox, spec.Revise)
		runName := fmt.Sprintf("%s%d", prefix, time.Now().Unix())
		if name, ok := r.resumableReviseRun(key, sb.GetAnnotations(), run.Key, prefix, req); ok {
			runName = name
		}
		if err := r.wake(ctx, sb); err != nil {
			logger.Error(err, "unable to wake the sandbox for a revise", "sandbox", sb.GetName())
			continue
		}
		opts := factorycli.ReviseOptions{
			Namespace:   spec.Member,
			SandboxName: sb.GetName(),
			Revise:      spec.Revise,
			Session:     run.SessionOf(),
			GithubToken: token,
			RunName:     runName,
			Inputs:      spec.Inputs,
		}
		if hook.options != nil {
			hook.options(&opts, sb)
		}
		if r.Factory.StartRevise(key, opts) {
			logger.Info("launched factory recipe revise", "sandbox", sb.GetName(), "recipe", run.Recipe, "revise", spec.Revise, "member", spec.Member)
		}
	}
}

// resumableReviseRun is the revise run recorded on the sandbox under runKey for
// req, named with prefix, when this controller has no result for key (it
// restarted): one started since the click. Invoking factory with its name
// follows it to its end.
func (r *Reconciler) resumableReviseRun(key string, annotations map[string]string, runKey, prefix string, req *boardv1alpha1.Request) (string, bool) {
	if _, ok := r.Factory.LastResult(key); ok {
		return "", false
	}
	// A second early: the click and the launch can share one.
	since := req.CreationTimestamp.Add(-time.Second)
	name, ok := factorycli.RecordedRunName(annotations, runKey, since)
	if !ok || !strings.HasPrefix(name, prefix) {
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

// settleRevise is served by the revise's result since the click, as its
// run's kind keeps it, or by its failure.
func (r *Reconciler) settleRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	if spec.Revise == "" || spec.Sandbox == "" {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "Malformed",
			message: "a revise names the sandbox and the recipe revise to run",
		}
	}
	sb, _, hook, found := reviseTarget(work, spec)
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
	case ran && hook.done != nil:
		return hook.done(r, ctx, work, spec, sb, res)
	case ran:
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: spec.Sandbox}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	case !found:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoRun",
			message: fmt.Sprintf("no run in %s offers %s", spec.Sandbox, spec.Revise),
		}
	case hook.missing != nil && hook.missing(sb) != "":
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NothingToRevise",
			message: hook.missing(sb),
		}
	}
	return pendingOutcome(req, now)
}

// revisedPlan stores a revise's plan as the draft, as ensurePlan stores a
// plan's: not yet posted. A draft gone since (rejected, or approved) stays
// gone.
func (r *Reconciler) revisedPlan(ctx context.Context, work *workState, spec boardv1alpha1.RequestSpec, sb *unstructured.Unstructured, res factorycli.Result) requestOutcome {
	plan := factorycli.ExtractPlan(res.Output)
	if plan == "" {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoPlan",
			message: "the revise ended without a plan",
		}
	}
	if sb == nil || factorycli.PlanDraft(sb.GetAnnotations()) == "" {
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
	}
	annotations := sb.GetAnnotations()
	annotations[factorycli.AnnotationPlanOutput] = factorycli.PlanTaskOutput(res.Output)
	delete(annotations, factorycli.AnnotationPlanApplied)
	annotations[AnnotationPlannedAt] = time.Now().UTC().Format(time.RFC3339)
	annotations[AnnotationBoard] = work.board.Name
	annotations[AnnotationExecutor] = spec.Member
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "storing the revised plan", "sandbox", sb.GetName())
		return stillPending
	}
	return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
}

// revisedReview marks the review the revise posted pending, even when the
// member had submitted the one before.
func (r *Reconciler) revisedReview(ctx context.Context, work *workState, _ boardv1alpha1.RequestSpec, sb *unstructured.Unstructured, _ factorycli.Result) requestOutcome {
	if sb == nil {
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
	}
	if err := r.markReviewPending(ctx, sb, work.board.Name); err != nil {
		log.FromContext(ctx).Error(err, "unable to mark review pending", "sandbox", sb.GetName())
		return stillPending
	}
	return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
}

// revisedNotes stores a revise's notes as the draft: not yet saved.
func (r *Reconciler) revisedNotes(ctx context.Context, _ *workState, _ boardv1alpha1.RequestSpec, sb *unstructured.Unstructured, res factorycli.Result) requestOutcome {
	if factorycli.ExtractNotes(res.Output) == "" {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoNotes",
			message: "the revise ended without notes",
		}
	}
	if sb == nil {
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised"}
	}
	if err := r.storeNotesDraft(ctx, sb, factorycli.NotesTaskOutput(res.Output)); err != nil {
		log.FromContext(ctx).Error(err, "storing the notes draft", "sandbox", sb.GetName())
		return stillPending
	}
	return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: sb.GetName()}
}
