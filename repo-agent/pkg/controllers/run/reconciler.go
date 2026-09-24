// Package run reconciles Run objects: platform v2's single execution
// path (docs/design/platform-v2.md).
//
// One reconciler replaces the per-verb passes of v1 — ensureFix,
// ensureReview, ensurePlan, ensureExploreClaims, ensureRunbookClaims —
// because every recipe is the same five steps: check the repo is v2's
// to drive, resolve credentials, launch the executor, record what
// happened. What differs between recipes is data, not control flow.
//
// Phase 1 ports exactly one recipe (understand). Others are rejected
// with a message rather than silently ignored: an unimplemented verb
// should say so, not look like a hung run.
package run

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// requeueWhileRunning paces status polling. A run takes minutes; this
// is about noticing it finished, not about driving it.
const requeueWhileRunning = 20 * time.Second

type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Factory factorycli.Launcher
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&boardv1alpha1.Run{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run boardv1alpha1.Run
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if run.Status.Phase == boardv1alpha1.RunPhaseSucceeded ||
		run.Status.Phase == boardv1alpha1.RunPhaseFailed {
		return ctrl.Result{}, nil
	}

	var board boardv1alpha1.RepoBoard
	if err := r.Get(ctx, types.NamespacedName{Name: run.Spec.Repo, Namespace: run.Namespace}, &board); err != nil {
		return ctrl.Result{}, r.fail(ctx, &run, fmt.Sprintf("repo %q not found", run.Spec.Repo))
	}

	// Exactly one control plane drives a repo. A Run against a
	// v1-managed repo is a mistake somewhere upstream, and executing it
	// anyway is how two schedulers produce duplicate work.
	if platform(&board) != "v2" {
		return ctrl.Result{}, r.fail(ctx, &run,
			fmt.Sprintf("repo %q is %s-managed; set spec.platform=v2 to run here", run.Spec.Repo, platform(&board)))
	}

	// Already launched: ask the runner, then the clock.
	if run.Status.Phase == boardv1alpha1.RunPhaseRunning {
		return r.observe(ctx, &run)
	}

	return r.launch(ctx, &run, &board)
}

func (r *Reconciler) launch(ctx context.Context, run *boardv1alpha1.Run, board *boardv1alpha1.RepoBoard) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	exec, ok := executors[run.Spec.Recipe]
	if !ok {
		return ctrl.Result{}, r.fail(ctx, run,
			fmt.Sprintf("recipe %q is not ported to v2 yet", run.Spec.Recipe))
	}

	member := run.Spec.Requester
	if member == "" {
		member = run.Namespace
	}
	token, err := r.memberToken(ctx, member)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, run, "no github token for "+member)
	}

	key := fmt.Sprintf("%s/run/%s", run.Namespace, run.Name)
	sandbox, err := exec(execContext{
		Factory: r.Factory,
		Key:     key,
		Run:     run,
		Board:   board,
		Member:  member,
		Token:   token,
	})
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, run, err.Error())
	}

	now := metav1.Now()
	run.Status.Phase = boardv1alpha1.RunPhaseRunning
	run.Status.Key = key
	run.Status.Sandbox = sandbox
	run.Status.StartedAt = &now
	run.Status.Message = "launched"
	logger.Info("run launched", "recipe", run.Spec.Recipe, "target", run.Spec.Target, "repo", run.Spec.Repo)
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
}

// observe records the outcome. The runner's in-memory result is the
// fast path; a controller restart loses it, so an older run whose
// sandbox has gone quiet is resolved from the sandbox's own record
// rather than left Running forever — the stale-Running bug v1 had to
// self-heal from the UI.
func (r *Reconciler) observe(ctx context.Context, run *boardv1alpha1.Run) (ctrl.Result, error) {
	if r.Factory.IsRunning(run.Status.Key) {
		return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
	}
	if res, ok := r.Factory.LastResult(run.Status.Key); ok {
		if run.Status.StartedAt == nil || !res.FinishedAt.Before(run.Status.StartedAt.Time) {
			if res.Err != nil {
				return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "", res.Err.Error())
			}
			return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseSucceeded, "", "completed")
		}
	}
	// No result and not running: either this controller never launched
	// it (restart) or the launch is still settling. Give it a grace
	// window before declaring anything.
	if run.Status.StartedAt != nil && time.Since(run.Status.StartedAt.Time) > 90*time.Minute {
		return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "",
			"no result recorded within 90m (controller restart or lost executor)")
	}
	return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
}

func (r *Reconciler) finish(ctx context.Context, run *boardv1alpha1.Run, phase, verdict, msg string) error {
	now := metav1.Now()
	run.Status.Phase = phase
	run.Status.Message = msg
	run.Status.CompletionTime = &now
	if verdict != "" {
		run.Status.Verdict = verdict
	}
	return r.Status().Update(ctx, run)
}

func (r *Reconciler) fail(ctx context.Context, run *boardv1alpha1.Run, msg string) error {
	log.FromContext(ctx).Info("run rejected", "run", run.Name, "reason", msg)
	return r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "", msg)
}

func (r *Reconciler) memberToken(ctx context.Context, namespace string) (string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: "github-pat", Namespace: namespace}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("no github-pat secret in %s", namespace)
		}
		return "", err
	}
	for _, key := range []string{"manual_pat", "oauth_pat", "pat"} {
		if v, ok := secret.Data[key]; ok && len(v) > 0 {
			return string(v), nil
		}
	}
	return "", fmt.Errorf("no github token in %s/github-pat", namespace)
}

func platform(board *boardv1alpha1.RepoBoard) string {
	if board.Spec.Platform == "" {
		return "v1"
	}
	return board.Spec.Platform
}
