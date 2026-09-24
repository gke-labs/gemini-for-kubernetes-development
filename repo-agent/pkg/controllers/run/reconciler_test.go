package run

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// ── fakes ────────────────────────────────────────────────────────────

type fakeLauncher struct {
	factorycli.Launcher // unused verbs panic if called, which is the point
	explored            int
	refuse              bool
	running             map[string]bool
	results             map[string]factorycli.Result
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{running: map[string]bool{}, results: map[string]factorycli.Result{}}
}

func (f *fakeLauncher) StartExplore(key string, _ factorycli.ExploreOptions) bool {
	if f.refuse {
		return false
	}
	f.explored++
	f.running[key] = true
	return true
}

func (f *fakeLauncher) IsRunning(key string) bool { return f.running[key] }

func (f *fakeLauncher) LastResult(key string) (factorycli.Result, bool) {
	res, ok := f.results[key]
	return res, ok
}

type fakeObserver struct {
	obs  factorycli.TaskObservation
	err  error
	seen int
}

func (f *fakeObserver) ObserveTask(_ context.Context, _, _, _ string) (factorycli.TaskObservation, error) {
	f.seen++
	return f.obs, f.err
}

// ── fixtures ─────────────────────────────────────────────────────────

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = boardv1alpha1.AddToScheme(s)
	return s
}

func v2Board() *boardv1alpha1.RepoBoard {
	return &boardv1alpha1.RepoBoard{
		ObjectMeta: metav1.ObjectMeta{Name: "granule", Namespace: "barney-s"},
		Spec: boardv1alpha1.RepoBoardSpec{
			RepoURL:  "https://github.com/gke-labs/granule",
			Platform: "v2",
		},
	}
}

func patSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "barney-s"},
		Data:       map[string][]byte{"manual_pat": []byte("ghp_test")},
	}
}

func testRun(mutate func(*boardv1alpha1.Run)) *boardv1alpha1.Run {
	r := &boardv1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "understand-1", Namespace: "barney-s"},
		Spec: boardv1alpha1.RunSpec{
			Repo: "granule", Recipe: "understand", Target: "repo", Requester: "barney-s",
		},
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func newTestReconciler(launcher factorycli.Launcher, observer factorycli.TaskObserver, objs ...runtime.Object) *Reconciler {
	builder := clientfake.NewClientBuilder().
		WithScheme(testScheme()).
		WithStatusSubresource(&boardv1alpha1.Run{})
	for _, o := range objs {
		builder = builder.WithRuntimeObjects(o)
	}
	return &Reconciler{
		Client: builder.Build(), Scheme: testScheme(),
		Factory: launcher, Observer: observer,
	}
}

func reconcileRun(t *testing.T, r *Reconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "barney-s"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func loadRun(t *testing.T, r *Reconciler, name string) *boardv1alpha1.Run {
	t.Helper()
	var out boardv1alpha1.Run
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "barney-s"}, &out); err != nil {
		t.Fatalf("get run: %v", err)
	}
	return &out
}

// ── launch ───────────────────────────────────────────────────────────

func TestLaunchRecordsWhereToLookForTheRun(t *testing.T) {
	launcher := newFakeLauncher()
	r := newTestReconciler(launcher, nil, v2Board(), patSecret(), testRun(nil))

	reconcileRun(t, r, "understand-1")

	run := loadRun(t, r, "understand-1")
	if run.Status.Phase != boardv1alpha1.RunPhaseRunning {
		t.Fatalf("phase = %q, want Running", run.Status.Phase)
	}
	// Without both of these a restarted controller cannot find the run's
	// record on disk, and can only time it out.
	if run.Status.Sandbox == "" || run.Status.TaskPrefix != "explore" {
		t.Errorf("sandbox=%q taskPrefix=%q, want both set", run.Status.Sandbox, run.Status.TaskPrefix)
	}
	if launcher.explored != 1 {
		t.Errorf("explored = %d, want 1", launcher.explored)
	}
}

func TestBusySandboxIsAWaitNotAFailure(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.refuse = true
	r := newTestReconciler(launcher, nil, v2Board(), patSecret(), testRun(nil))

	res := reconcileRun(t, r, "understand-1")

	// One task per sandbox is an invariant, so the worker being busy is a
	// queue. Failing here would turn "wait your turn" into a dead run.
	if run := loadRun(t, r, "understand-1"); run.Status.Phase == boardv1alpha1.RunPhaseFailed {
		t.Fatalf("phase = Failed (%q), want the run to stay pending", run.Status.Message)
	}
	if res.RequeueAfter == 0 {
		t.Error("no requeue scheduled; the run would never launch")
	}
}

func TestV1ManagedRepoRefusesTheRun(t *testing.T) {
	board := v2Board()
	board.Spec.Platform = "v1"
	r := newTestReconciler(newFakeLauncher(), nil, board, patSecret(), testRun(nil))

	reconcileRun(t, r, "understand-1")

	run := loadRun(t, r, "understand-1")
	if run.Status.Phase != boardv1alpha1.RunPhaseFailed {
		t.Fatalf("phase = %q, want Failed — two control planes on one repo is how duplicates happen", run.Status.Phase)
	}
}

func TestUnportedRecipeSaysSoRatherThanHanging(t *testing.T) {
	r := newTestReconciler(newFakeLauncher(), nil, v2Board(), patSecret(), testRun(func(run *boardv1alpha1.Run) {
		run.Spec.Recipe = "deploy"
	}))

	reconcileRun(t, r, "understand-1")

	run := loadRun(t, r, "understand-1")
	if run.Status.Phase != boardv1alpha1.RunPhaseFailed {
		t.Fatalf("phase = %q, want Failed", run.Status.Phase)
	}
	if run.Status.Message == "" {
		t.Error("no message; an unimplemented verb must not look like a hung run")
	}
}

// ── observe ──────────────────────────────────────────────────────────

func runningRun(started time.Time) *boardv1alpha1.Run {
	return testRun(func(r *boardv1alpha1.Run) {
		at := metav1.NewTime(started)
		r.Status = boardv1alpha1.RunStatus{
			Phase: boardv1alpha1.RunPhaseRunning, Key: "barney-s/run/understand-1",
			Sandbox: "explore-granule", TaskPrefix: "explore", StartedAt: &at,
		}
	})
}

func TestRestartedControllerAdoptsTheOutcomeFromDisk(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	// A fresh launcher has no memory of the run: exactly what a restart
	// leaves behind.
	observer := &fakeObserver{obs: factorycli.TaskObservation{
		State: factorycli.ObserveFinished, ExitCode: 128,
		Dir: "explore-20260924-215011", StartedAt: started.Add(time.Second),
	}}
	r := newTestReconciler(newFakeLauncher(), observer, v2Board(), patSecret(), runningRun(started))

	reconcileRun(t, r, "understand-1")

	run := loadRun(t, r, "understand-1")
	if run.Status.Phase != boardv1alpha1.RunPhaseFailed {
		t.Fatalf("phase = %q, want Failed from exit code 128", run.Status.Phase)
	}
	if run.Status.TaskDir != "explore-20260924-215011" {
		t.Errorf("taskDir = %q, want the directory holding the logs", run.Status.TaskDir)
	}
}

func TestDiskObservationSucceedsWithoutTheLauncher(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	observer := &fakeObserver{obs: factorycli.TaskObservation{
		State: factorycli.ObserveFinished, ExitCode: 0,
		Dir: "explore-20260924-215011", StartedAt: started.Add(time.Second),
	}}
	r := newTestReconciler(newFakeLauncher(), observer, v2Board(), patSecret(), runningRun(started))

	reconcileRun(t, r, "understand-1")

	if run := loadRun(t, r, "understand-1"); run.Status.Phase != boardv1alpha1.RunPhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded", run.Status.Phase)
	}
}

func TestATaskOlderThanTheRunIsNotTheRun(t *testing.T) {
	started := time.Now().Add(-5 * time.Minute)
	// Sandboxes are reused: granule's holds 56 explore directories. The
	// newest one predating our launch belongs to somebody else.
	observer := &fakeObserver{obs: factorycli.TaskObservation{
		State: factorycli.ObserveFinished, ExitCode: 0,
		Dir: "explore-20260923-100000", StartedAt: started.Add(-2 * time.Hour),
	}}
	r := newTestReconciler(newFakeLauncher(), observer, v2Board(), patSecret(), runningRun(started))

	reconcileRun(t, r, "understand-1")

	run := loadRun(t, r, "understand-1")
	if run.Status.Phase != boardv1alpha1.RunPhaseRunning {
		t.Fatalf("phase = %q, want Running — an older task must not resolve this run", run.Status.Phase)
	}
}

func TestUnreadableSandboxStillTimesOut(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	observer := &fakeObserver{err: errors.New("pod not found")}
	r := newTestReconciler(newFakeLauncher(), observer, v2Board(), patSecret(), runningRun(started))

	reconcileRun(t, r, "understand-1")

	if run := loadRun(t, r, "understand-1"); run.Status.Phase != boardv1alpha1.RunPhaseFailed {
		t.Fatalf("phase = %q, want Failed after the grace window", run.Status.Phase)
	}
}

// ── retention ────────────────────────────────────────────────────────

func terminalRun(phase string, finished time.Time) *boardv1alpha1.Run {
	return testRun(func(r *boardv1alpha1.Run) {
		done := metav1.NewTime(finished)
		r.Status = boardv1alpha1.RunStatus{Phase: phase, CompletionTime: &done}
	})
}

func TestSucceededRunsAreRetiredOnceTheArtifactIsTheRecord(t *testing.T) {
	r := newTestReconciler(newFakeLauncher(), nil, v2Board(), patSecret(),
		terminalRun(boardv1alpha1.RunPhaseSucceeded, time.Now().Add(-2*time.Hour)))

	reconcileRun(t, r, "understand-1")

	var out boardv1alpha1.Run
	err := r.Get(context.Background(), types.NamespacedName{Name: "understand-1", Namespace: "barney-s"}, &out)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("run still present (%v); a success is represented by its artifact, not by an object in etcd", err)
	}
}

func TestFailedRunsSurviveBecauseNothingElseRecordsThem(t *testing.T) {
	r := newTestReconciler(newFakeLauncher(), nil, v2Board(), patSecret(),
		terminalRun(boardv1alpha1.RunPhaseFailed, time.Now().Add(-2*time.Hour)))

	reconcileRun(t, r, "understand-1")

	// The granule case: the push 403'd, so no receipt and no notes commit
	// exist. Delete this and the failure leaves no trace anywhere.
	if run := loadRun(t, r, "understand-1"); run.Status.Phase != boardv1alpha1.RunPhaseFailed {
		t.Fatalf("failed run was retired early (phase %q)", run.Status.Phase)
	}
}

func TestFreshSuccessIsKeptLongEnoughToBeSeen(t *testing.T) {
	r := newTestReconciler(newFakeLauncher(), nil, v2Board(), patSecret(),
		terminalRun(boardv1alpha1.RunPhaseSucceeded, time.Now()))

	res := reconcileRun(t, r, "understand-1")

	if _, err := loadRunErr(r); err != nil {
		t.Fatalf("a just-completed run was deleted before anyone could see it: %v", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > succeededRetention {
		t.Errorf("requeue = %v, want a wake-up within the retention window", res.RequeueAfter)
	}
}

func loadRunErr(r *Reconciler) (*boardv1alpha1.Run, error) {
	var out boardv1alpha1.Run
	err := r.Get(context.Background(), types.NamespacedName{Name: "understand-1", Namespace: "barney-s"}, &out)
	return &out, err
}
