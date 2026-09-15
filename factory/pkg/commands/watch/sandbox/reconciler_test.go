package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// stubEntityState is a hand written EntityState whose answers are fixed per test.
type stubEntityState struct {
	prsPopulated    bool
	issuesPopulated bool
	openPRs         map[int]bool
	openIssues      map[int]bool
}

func (s *stubEntityState) HasOpenPRs() bool        { return s.prsPopulated }
func (s *stubEntityState) HasOpenIssues() bool     { return s.issuesPopulated }
func (s *stubEntityState) IsOpenPR(num int) bool   { return s.openPRs[num] }
func (s *stubEntityState) IsOpenIssue(id int) bool { return s.openIssues[id] }

// warmCache is an EntityState reporting that both scans have run and found
// nothing open, which is what opens both collection gates.
func warmCache() *stubEntityState {
	return &stubEntityState{prsPopulated: true, issuesPopulated: true}
}

// newClosedEntityGitHub returns a GitHub client whose pull requests and issues
// all report as closed, which is what makes a sandbox eligible for collection.
func newClosedEntityGitHub(t *testing.T) *githubv39.Client {
	t.Helper()
	return newEntityGitHub(t, nil)
}

// newEntityGitHub returns a GitHub client reporting every entity as closed,
// calling onRequest first when it is non-nil.
func newEntityGitHub(t *testing.T, onRequest func()) *githubv39.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onRequest != nil {
			onRequest()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "closed"})
	}))
	t.Cleanup(server.Close)

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")
	return client
}

// sandboxExists reports whether the named sandbox is still present in the cluster.
func sandboxExists(t *testing.T, r *Reconciler, name string) bool {
	t.Helper()

	_, err := r.sandboxes.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func TestCollectGarbageDeletesClosedEntitySandboxes(t *testing.T) {
	kube := newFakeKubeClient(t,
		newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"}),
		newSandbox("fix-test-repo-42", map[string]interface{}{annotationLastTaskState: "Completed"}),
	)
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   kube,
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: warmCache(),
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected the sandbox of closed PR #7 to be deleted")
	}
	if sandboxExists(t, r, "fix-test-repo-42") {
		t.Errorf("expected the sandbox of closed issue #42 to be deleted")
	}
}

func TestCollectGarbageSkipsOpenEntities(t *testing.T) {
	kube := newFakeKubeClient(t,
		newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"}),
		newSandbox("fix-test-repo-42", map[string]interface{}{annotationLastTaskState: "Completed"}),
	)
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   kube,
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks: concurrency.NewSandboxLockRegistry(),
		Entities: &stubEntityState{
			prsPopulated:    true,
			issuesPopulated: true,
			openPRs:         map[int]bool{7: true},
			openIssues:      map[int]bool{42: true},
		},
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected the sandbox of open PR #7 to be kept")
	}
	if !sandboxExists(t, r, "fix-test-repo-42") {
		t.Errorf("expected the sandbox of open issue #42 to be kept")
	}
}

func TestCollectGarbageSkipsLeasedSandboxes(t *testing.T) {
	kube := newFakeKubeClient(t,
		newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"}),
	)
	locks := concurrency.NewSandboxLockRegistry()
	if !locks.TryAcquire("factory-pr-7", "task-pr-7-comments.yaml") {
		t.Fatalf("failed to acquire the sandbox lease used by this test")
	}

	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   kube,
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:    locks,
		Entities: warmCache(),
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected a leased sandbox to survive garbage collection even though its PR is closed")
	}
}

// Testing the lease and then deleting is not enough: confirming the entity
// against GitHub takes long enough for a dispatcher to lease the sandbox and
// start a task inside it, which the delete would then destroy. The reconciler
// must hold the lease across the confirmation.
func TestCollectGarbageHoldsLeaseWhileConfirmingWithGitHub(t *testing.T) {
	locks := concurrency.NewSandboxLockRegistry()

	var leasedDuringConfirm atomic.Bool
	gh := newEntityGitHub(t, func() {
		// Stand in for the dispatcher trying to claim the sandbox mid-sweep.
		leasedDuringConfirm.Store(!locks.TryAcquire("factory-pr-7", "task-pr-7-comments.yaml"))
	})

	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: gh,
		}),
		Locks:    locks,
		Entities: warmCache(),
	})

	r.CollectGarbage(context.Background())

	if !leasedDuringConfirm.Load() {
		t.Errorf("expected the sandbox to be leased while its PR was being confirmed, so a dispatcher cannot start a task in a sandbox that is about to be deleted")
	}
	if sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected the sandbox of closed PR #7 to be deleted")
	}
	if locks.IsBusy("factory-pr-7") {
		t.Errorf("expected the collection lease to be released once the sweep finished")
	}
}

// The two caches are populated by different scans, so a cold issue cache must
// not be able to hold up PR collection, and vice versa.
func TestCollectGarbageGatesEachCacheIndependently(t *testing.T) {
	tests := []struct {
		name            string
		entities        *stubEntityState
		wantPRDeleted   bool
		wantIssueDelete bool
	}{
		{
			name:          "only the PR cache is warm",
			entities:      &stubEntityState{prsPopulated: true},
			wantPRDeleted: true,
		},
		{
			name:            "only the issue cache is warm",
			entities:        &stubEntityState{issuesPopulated: true},
			wantIssueDelete: true,
		},
		{
			name:     "neither cache is warm",
			entities: &stubEntityState{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Config{}, Deps{
				Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
					Kube: newFakeKubeClient(t,
						newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"}),
						newSandbox("fix-test-repo-42", map[string]interface{}{annotationLastTaskState: "Completed"}),
					),
					GitHub: newClosedEntityGitHub(t),
				}),
				Locks:    concurrency.NewSandboxLockRegistry(),
				Entities: tc.entities,
			})

			r.CollectGarbage(context.Background())

			if got := !sandboxExists(t, r, "factory-pr-7"); got != tc.wantPRDeleted {
				t.Errorf("PR sandbox deleted = %v, want %v", got, tc.wantPRDeleted)
			}
			if got := !sandboxExists(t, r, "fix-test-repo-42"); got != tc.wantIssueDelete {
				t.Errorf("issue sandbox deleted = %v, want %v", got, tc.wantIssueDelete)
			}
		})
	}
}

func TestCollectGarbagePausesWhileDraining(t *testing.T) {
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: warmCache(),
		Paused:   func() bool { return true },
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected no sandbox to be reclaimed while the watcher is draining or shutting down")
	}
}

func TestCollectGarbageEvictsStaleIdleSandboxes(t *testing.T) {
	stale := newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"})
	stale.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-48 * time.Hour)))

	fresh := newSandbox("factory-pr-8", map[string]interface{}{annotationLastTaskState: "Completed"})
	fresh.SetCreationTimestamp(metav1.NewTime(time.Now()))

	r := New(Config{EvictionAge: "24h"}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube: newFakeKubeClient(t, stale, fresh),
		}),
		Locks: concurrency.NewSandboxLockRegistry(),
		// PRs are reported open so only the eviction path can delete anything.
		Entities: &stubEntityState{
			prsPopulated:    true,
			issuesPopulated: true,
			openPRs:         map[int]bool{7: true, 8: true},
		},
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected the sandbox older than the eviction age to be evicted")
	}
	if !sandboxExists(t, r, "factory-pr-8") {
		t.Errorf("expected the recently created sandbox to be kept")
	}
}

// Eviction reads nothing but cluster state, so it must keep working in the
// modes where no scanner ever warms the entity cache.
func TestCollectGarbageEvictsWithoutAPopulatedCache(t *testing.T) {
	stale := newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"})
	stale.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-48 * time.Hour)))

	r := New(Config{EvictionAge: "24h"}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube: newFakeKubeClient(t, stale),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: &stubEntityState{},
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected eviction to run even though no scan has populated the entity cache")
	}
}

func TestCollectGarbageDryRunKeepsSandboxes(t *testing.T) {
	r := New(Config{DryRun: true}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-7", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: warmCache(),
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "factory-pr-7") {
		t.Errorf("expected dry run to leave the sandbox of the closed PR in place")
	}
}

func TestReconcileOnceDeletesEvictedPods(t *testing.T) {
	evicted := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "factory-pr-7-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{"sandbox": "factory-pr-7"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"},
	}
	healthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "factory-pr-8-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{"sandbox": "factory-pr-8"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	kube := newFakeKubeClientWithPods(t, []runtime.Object{evicted, healthy})
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{Kube: kube}),
		Locks:     concurrency.NewSandboxLockRegistry(),
		Entities:  warmCache(),
	})

	r.ReconcileOnce(context.Background())

	remaining, err := kube.Clientset.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list pods: %v", err)
	}
	if len(remaining.Items) != 1 || remaining.Items[0].Name != healthy.Name {
		t.Errorf("expected only the healthy pod to remain, got %v", remaining.Items)
	}
}

// A label selector cannot express "has either key", so pods that carry only the
// namespaced label need their own listing.
func TestReconcileOnceDeletesEvictedPodsLabeledWithEitherKey(t *testing.T) {
	namespacedLabel := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wf-issue-12-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{"agents.x-k8s.io/sandbox": "wf-issue-12"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"},
	}
	shortLabel := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "factory-pr-7-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{"sandbox": "factory-pr-7"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"},
	}

	kube := newFakeKubeClientWithPods(t, []runtime.Object{namespacedLabel, shortLabel})
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{Kube: kube}),
		Locks:     concurrency.NewSandboxLockRegistry(),
		Entities:  warmCache(),
	})

	r.ReconcileOnce(context.Background())

	remaining, err := kube.Clientset.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list pods: %v", err)
	}
	if len(remaining.Items) != 0 {
		t.Errorf("expected evicted pods under both label keys to be deleted, got %v", remaining.Items)
	}
}

// idleSandbox builds a sandbox whose last activity is far enough in the past
// that it is a suspension candidate.
func idleSandbox(name string) *unstructured.Unstructured {
	sb := newSandbox(name, map[string]interface{}{
		annotationLastTaskState:                     "Completed",
		"sandbox.gemini.google.com/completion-time": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
	})
	sb.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-48 * time.Hour)))
	sb.Object["spec"] = map[string]interface{}{"replicas": int64(1)}
	return sb
}

// replicas reports the replica count recorded on a sandbox, or -1 if unset.
func replicas(t *testing.T, r *Reconciler, name string) int64 {
	t.Helper()

	obj, err := r.sandboxes.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to read sandbox %s: %v", name, err)
	}
	value, found, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if err != nil || !found {
		return -1
	}
	return value
}

func TestCollectGarbageSuspendsIdleSandboxes(t *testing.T) {
	locks := concurrency.NewSandboxLockRegistry()
	r := New(Config{IdleTimeout: time.Hour}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube: newFakeKubeClient(t, idleSandbox("factory-pr-7")),
		}),
		Locks: locks,
		// Reported open so only the suspension path can touch the sandbox.
		Entities: &stubEntityState{prsPopulated: true, issuesPopulated: true, openPRs: map[int]bool{7: true}},
	})

	r.CollectGarbage(context.Background())

	if got := replicas(t, r, "factory-pr-7"); got != 0 {
		t.Errorf("expected the idle sandbox to be scaled to zero replicas, got %d", got)
	}
	if locks.IsBusy("factory-pr-7") {
		t.Errorf("expected the suspension lease to be released once the sweep finished")
	}
}

func TestCollectGarbageSkipsSuspendingLeasedSandboxes(t *testing.T) {
	locks := concurrency.NewSandboxLockRegistry()
	if !locks.TryAcquire("factory-pr-7", "task-pr-7-comments.yaml") {
		t.Fatalf("failed to acquire the sandbox lease used by this test")
	}

	r := New(Config{IdleTimeout: time.Hour}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube: newFakeKubeClient(t, idleSandbox("factory-pr-7")),
		}),
		Locks:    locks,
		Entities: &stubEntityState{prsPopulated: true, issuesPopulated: true, openPRs: map[int]bool{7: true}},
	})

	r.CollectGarbage(context.Background())

	if got := replicas(t, r, "factory-pr-7"); got != 1 {
		t.Errorf("expected a leased sandbox to stay running rather than be suspended, got %d replicas", got)
	}
}

// A sandbox that picks up work between the sweep's listing and its own
// suspension must not be scaled down on the strength of the stale view.
func TestCollectGarbageSkipsSuspendingSandboxThatBecameActive(t *testing.T) {
	stale := idleSandbox("factory-pr-7")
	kube := newFakeKubeClient(t, stale)

	// The sweep will read this fresher copy back before writing.
	active := idleSandbox("factory-pr-7")
	active.SetAnnotations(map[string]string{annotationLastTaskState: "Running"})
	if _, err := kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(testNamespace).Update(context.Background(), active, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("failed to mark the sandbox as running: %v", err)
	}

	r := New(Config{IdleTimeout: time.Hour}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{Kube: kube}),
		Locks:     concurrency.NewSandboxLockRegistry(),
		Entities:  &stubEntityState{prsPopulated: true, issuesPopulated: true, openPRs: map[int]bool{7: true}},
	})

	// Drive the pass with the stale listing, which still says the sandbox is idle.
	r.suspendIdleSandboxes(context.Background(), []unstructured.Unstructured{*stale}, map[string]bool{})

	if got := replicas(t, r, "factory-pr-7"); got != 1 {
		t.Errorf("expected a sandbox that became active to stay running, got %d replicas", got)
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	r := New(Config{Interval: time.Millisecond, GCInterval: time.Millisecond}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: warmCache(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Cancellation is how this subcontroller is asked to stop, so it is not
		// reported as a failure.
		if err != nil {
			t.Errorf("expected Run to return nil after cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	r := New(Config{}, Deps{})
	if r.cfg.Interval != DefaultInterval {
		t.Errorf("expected the default reconcile interval %s, got %s", DefaultInterval, r.cfg.Interval)
	}
	if r.cfg.GCInterval != DefaultGCInterval {
		t.Errorf("expected the default GC interval %s, got %s", DefaultGCInterval, r.cfg.GCInterval)
	}
}

func TestIssueNumberFromSandboxName(t *testing.T) {
	tests := []struct {
		name    string
		want    int
		wantOK  bool
		comment string
	}{
		{name: "wf-issue-12", want: 12, wantOK: true},
		{name: "fix-test-repo-42", want: 42, wantOK: true},
		{name: "factory-pr-7", wantOK: false, comment: "PR sandboxes are handled separately"},
		{name: "fix-test-repo", wantOK: false, comment: "no trailing number"},
		{name: "wf-issue-0", wantOK: false, comment: "issue numbers start at one"},
		{name: "wf-issue--3", wantOK: false, comment: "issue numbers are positive"},
		{name: "some-other-sandbox", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := issueNumberFromSandboxName(tc.name)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("issueNumberFromSandboxName(%q) = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseEvictionAge(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultEvictionAge, false},
		{"3d", 3 * 24 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"invalid", 0, true},
		{"xd", 0, true},
	}

	for _, tc := range tests {
		got, err := ParseEvictionAge(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseEvictionAge(%q) expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseEvictionAge(%q) unexpected error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("ParseEvictionAge(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
