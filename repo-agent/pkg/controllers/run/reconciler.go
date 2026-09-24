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
	"errors"
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

// Retention. A Run is a work order, not an archive: once the work has
// left a durable artifact — notes on a branch, a receipt, a PR — the
// object is a duplicate of something git already holds, and it goes.
//
// A run that failed before producing anything is the exception, and the
// reason the object earns its place: nothing else records that it
// happened. Those are kept long enough to be seen and diagnosed.
const (
	succeededRetention = time.Hour
	failedRetention    = 7 * 24 * time.Hour
)

// noResultGrace bounds how long a Running run may go unexplained when
// the sandbox cannot be read at all (deleted, unreachable). With an
// Observer this is a backstop; without one it is the only stop.
const noResultGrace = 90 * time.Minute

type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Factory factorycli.Launcher
	// Observer reads a run's record off the sandbox disk. The launcher's
	// in-memory result is the fast path and dies with the process; this
	// is what makes a restart recoverable rather than a 90-minute
	// timeout. Nil falls back to the timeout.
	Observer factorycli.TaskObserver
}

//+kubebuilder:rbac:groups=board.gemini.google.com,resources=runs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=board.gemini.google.com,resources=runs/status,verbs=get;update;patch

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
		return r.retire(ctx, &run)
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
	where, err := exec(execContext{
		Factory: r.Factory,
		Key:     key,
		Run:     run,
		Board:   board,
		Member:  member,
		Token:   token,
	})
	if err != nil {
		var busy errBusy
		if errors.As(err, &busy) {
			// One task per sandbox is the invariant, so a busy worker is
			// a queue, not an error. Stay Pending and come back.
			return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
		}
		return ctrl.Result{}, r.fail(ctx, run, err.Error())
	}

	now := metav1.Now()
	run.Status.Phase = boardv1alpha1.RunPhaseRunning
	run.Status.Key = key
	run.Status.Sandbox = where.Sandbox
	run.Status.TaskPrefix = where.TaskPrefix
	run.Status.StartedAt = &now
	run.Status.Message = "launched"
	logger.Info("run launched", "recipe", run.Spec.Recipe, "target", run.Spec.Target, "repo", run.Spec.Repo)
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
}

// observe records the outcome, in order of authority.
//
// The launcher's in-memory result is the fast path and the richest —
// it carries the error text. It dies with the process, so the fallback
// is the task's own record on the sandbox disk, which does not: the
// same exit_code file that told us granule had failed fifty-six times
// while the cluster showed one standing claim.
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

	// This controller did not launch it — a restart, almost always. Ask
	// the sandbox.
	if obs, ok := r.observeOnDisk(ctx, run); ok {
		switch obs.State {
		case factorycli.ObserveRunning:
			return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
		case factorycli.ObserveFinished:
			run.Status.TaskDir = obs.Dir
			if obs.ExitCode != 0 {
				return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "",
					fmt.Sprintf("task %s exited %d", obs.Dir, obs.ExitCode))
			}
			return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseSucceeded, "",
				"completed (adopted from "+obs.Dir+")")
		case factorycli.ObserveDead:
			run.Status.TaskDir = obs.Dir
			return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "",
				"task "+obs.Dir+" died without an exit code (pod restarted mid-run)")
		}
	}

	// Nothing can be read: no observer, no sandbox, or the launch is
	// still settling. Bound the wait rather than hang forever.
	if run.Status.StartedAt != nil && time.Since(run.Status.StartedAt.Time) > noResultGrace {
		return ctrl.Result{}, r.finish(ctx, run, boardv1alpha1.RunPhaseFailed, "",
			"no result recorded within 90m and the sandbox has no record of the task")
	}
	return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
}

// observeOnDisk reads the run's own task record. A reused sandbox holds
// many tasks, so a record that predates this run belongs to someone
// else and is not an answer about us.
func (r *Reconciler) observeOnDisk(ctx context.Context, run *boardv1alpha1.Run) (factorycli.TaskObservation, bool) {
	if r.Observer == nil || run.Status.Sandbox == "" || run.Status.TaskPrefix == "" {
		return factorycli.TaskObservation{}, false
	}
	obs, err := r.Observer.ObserveTask(ctx, run.Namespace, run.Status.Sandbox, run.Status.TaskPrefix)
	if err != nil {
		log.FromContext(ctx).Info("cannot observe run task", "run", run.Name, "err", err)
		return factorycli.TaskObservation{}, false
	}
	if obs.State == factorycli.ObserveNone {
		return obs, false
	}
	if run.Status.StartedAt != nil && !obs.StartedAt.IsZero() &&
		obs.StartedAt.Before(run.Status.StartedAt.Time.Add(-2*time.Minute)) {
		// The newest task in this sandbox started before we did: ours
		// never got far enough to create a directory.
		return obs, false
	}
	return obs, true
}

// retire garbage-collects terminal runs. Deleting a succeeded run is
// not losing it: the recipe's artifact — notes, receipt, PR — is the
// record, and a recipe that succeeds without leaving one is a bug in
// the recipe. Failures with no artifact are the reason this object
// exists, so they outlive the rest.
func (r *Reconciler) retire(ctx context.Context, run *boardv1alpha1.Run) (ctrl.Result, error) {
	keep := succeededRetention
	if run.Status.Phase == boardv1alpha1.RunPhaseFailed {
		keep = failedRetention
	}
	done := run.Status.CompletionTime
	if done == nil {
		now := metav1.Now()
		done = &now
	}
	if age := time.Since(done.Time); age < keep {
		return ctrl.Result{RequeueAfter: keep - age}, nil
	}
	log.FromContext(ctx).Info("retiring run", "run", run.Name, "phase", run.Status.Phase)
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, run))
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
