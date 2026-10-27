package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
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
// under the number it was asked for, calling onRequest first when it is
// non-nil.
func newEntityGitHub(t *testing.T, onRequest func()) *githubv39.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onRequest != nil {
			onRequest()
		}
		w.Header().Set("Content-Type", "application/json")
		entity := map[string]interface{}{"state": "closed"}
		if num, err := strconv.Atoi(path.Base(r.URL.Path)); err == nil {
			entity["number"] = num
		}
		_ = json.NewEncoder(w).Encode(entity)
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
	if !locks.TryAcquire("factory-pr-7", "task-pr-7-rule-comments.yaml") {
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
		leasedDuringConfirm.Store(!locks.TryAcquire("factory-pr-7", "task-pr-7-rule-comments.yaml"))
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
	if !locks.TryAcquire("factory-pr-7", "task-pr-7-rule-comments.yaml") {
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

// recordingLinkedWork records the closed issues the reconciler reports.
type recordingLinkedWork struct {
	closed []int
}

func (l *recordingLinkedWork) NudgeLinkedWorkflows(_ context.Context, closed *githubv39.Issue) {
	l.closed = append(l.closed, closed.GetNumber())
}

// prServingIssueSandbox is the sandbox of issue #65 once PR #70, which closes
// the issue, has started running its tasks in it.
func prServingIssueSandbox() *unstructured.Unstructured {
	sb := newSandbox("fix-test-repo-65", map[string]interface{}{annotationLastTaskState: "Completed"})
	sb.SetLabels(map[string]string{labelPR: "70"})
	return sb
}

// The issue cache counts every issue the watcher has worked on as open, so a
// merge closing issue #65 is noticed through its PR leaving the open set.
func TestCollectGarbageCollectsIssueSandboxWhenItsPRCloses(t *testing.T) {
	linked := &recordingLinkedWork{}
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, prServingIssueSandbox()),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks: concurrency.NewSandboxLockRegistry(),
		Entities: &stubEntityState{
			prsPopulated:    true,
			issuesPopulated: true,
			openIssues:      map[int]bool{65: true},
		},
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "fix-test-repo-65") {
		t.Errorf("expected the sandbox of issue #65 to be deleted once its PR closed")
	}
	if len(linked.closed) != 1 || linked.closed[0] != 65 {
		t.Errorf("reported closed issues %v, want [65]", linked.closed)
	}
}

func TestCollectGarbageKeepsIssueSandboxWhileItsPRIsOpen(t *testing.T) {
	linked := &recordingLinkedWork{}
	var requests atomic.Int32
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, prServingIssueSandbox()),
			GitHub: newEntityGitHub(t, func() { requests.Add(1) }),
		}),
		Locks: concurrency.NewSandboxLockRegistry(),
		Entities: &stubEntityState{
			prsPopulated:    true,
			issuesPopulated: true,
			openPRs:         map[int]bool{70: true},
			openIssues:      map[int]bool{65: true},
		},
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "fix-test-repo-65") {
		t.Errorf("expected the sandbox of issue #65 to be kept while PR #70 is open")
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("made %d GitHub requests, want none for a sandbox the cache vouches for", n)
	}
	if len(linked.closed) != 0 {
		t.Errorf("reported closed issues %v, want none", linked.closed)
	}
}

// The nudge goes out in a dry run too; the implementation is the one that
// reports instead of queueing.
func TestCollectGarbageReportsClosedIssuesInDryRun(t *testing.T) {
	linked := &recordingLinkedWork{}
	r := New(Config{DryRun: true}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("wf-issue-42", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:      concurrency.NewSandboxLockRegistry(),
		Entities:   warmCache(),
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "wf-issue-42") {
		t.Errorf("expected dry run to keep the sandbox")
	}
	if len(linked.closed) != 1 || linked.closed[0] != 42 {
		t.Errorf("reported closed issues %v, want [42]", linked.closed)
	}
}

// newMergedPRGitHub returns a GitHub client where PR #75, merged, references
// closed issue #71, open issue #72 and PR #69 - only the first of which the
// merge can have closed.
func newMergedPRGitHub(t *testing.T) *githubv39.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		num, _ := strconv.Atoi(path.Base(r.URL.Path))
		entity := map[string]interface{}{"number": num, "state": "closed"}
		switch {
		case strings.Contains(r.URL.Path, "/pulls/"):
			entity["merged"] = true
			entity["body"] = "Fixes #71. Follows #69, see also #72."
		case num == 72:
			entity["state"] = "open"
		case num == 69:
			entity["pull_request"] = map[string]interface{}{"url": "https://example.com/pulls/69"}
		}
		_ = json.NewEncoder(w).Encode(entity)
	}))
	t.Cleanup(server.Close)

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(server.URL + "/")
	return client
}

// Review sandboxes carry the repo in their name; the issue a merged PR closes
// may have no sandbox of its own, so the nudge comes from the PR's.
func TestCollectGarbageCollectsRepoScopedPRSandboxAndNudges(t *testing.T) {
	linked := &recordingLinkedWork{}
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-test-repo-75", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newMergedPRGitHub(t),
		}),
		Locks:      concurrency.NewSandboxLockRegistry(),
		Entities:   warmCache(),
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "factory-pr-test-repo-75") {
		t.Errorf("expected the sandbox of merged PR #75 to be deleted")
	}
	if len(linked.closed) != 1 || linked.closed[0] != 71 {
		t.Errorf("reported closed issues %v, want [71]", linked.closed)
	}
}

// The sandbox a PR's recipes made (EnsurePRSandbox) is collected with
// the PR, as the PR's: the merge nudges the issues it closed. The issue
// sweep, for which its name looks like an issue's, leaves it alone.
func TestCollectGarbageCollectsPRFixSandboxWithItsPR(t *testing.T) {
	sb := newSandbox("fix-test-repo-75", map[string]interface{}{annotationLastTaskState: "Completed"})
	sb.SetLabels(map[string]string{labelPR: "75"})
	linked := &recordingLinkedWork{}
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, sb),
			GitHub: newMergedPRGitHub(t),
		}),
		Locks:      concurrency.NewSandboxLockRegistry(),
		Entities:   warmCache(),
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "fix-test-repo-75") {
		t.Errorf("expected the fix sandbox of merged PR #75 to be deleted")
	}
	if len(linked.closed) != 1 || linked.closed[0] != 71 {
		t.Errorf("reported closed issues %v, want [71]", linked.closed)
	}
}

func TestCollectGarbageKeepsPRFixSandboxWhilePRIsOpen(t *testing.T) {
	sb := newSandbox("fix-test-repo-75", map[string]interface{}{annotationLastTaskState: "Completed"})
	sb.SetLabels(map[string]string{labelPR: "75"})
	var requests atomic.Int32
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, sb),
			GitHub: newEntityGitHub(t, func() { requests.Add(1) }),
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: &stubEntityState{prsPopulated: true, issuesPopulated: true, openPRs: map[int]bool{75: true}},
	})

	r.CollectGarbage(context.Background())

	if !sandboxExists(t, r, "fix-test-repo-75") {
		t.Errorf("expected the fix sandbox of open PR #75 to be kept")
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("made %d GitHub requests, want none for a PR the cache vouches for", n)
	}
}

func TestCollectGarbageDoesNotNudgeForUnmergedPR(t *testing.T) {
	linked := &recordingLinkedWork{}
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-test-repo-7", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:      concurrency.NewSandboxLockRegistry(),
		Entities:   warmCache(),
		LinkedWork: linked,
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, "factory-pr-test-repo-7") {
		t.Errorf("expected the sandbox of closed PR #7 to be deleted")
	}
	if len(linked.closed) != 0 {
		t.Errorf("reported closed issues %v, want none for a PR closed without merging", linked.closed)
	}
}

func TestPRNumberFromSandbox(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   int
		wantOK bool
	}{
		{name: "factory-pr-k8s-config-connector-13575", want: 13575, wantOK: true},
		{name: "factory-pr-7", want: 7, wantOK: true},
		{name: "factory-pr-truncated-slug-8", labels: map[string]string{labelPR: "9"}, want: 9, wantOK: true},
		{name: "factory-pr-repo", wantOK: false},
		{name: "fix-test-repo-42", labels: map[string]string{labelPR: "70"}, wantOK: false},
		{name: "fix-test-repo-70", labels: map[string]string{labelPR: "70"}, want: 70, wantOK: true},
		{name: "fix-other-repo-70", labels: map[string]string{labelPR: "70"}, wantOK: false},
		{name: "fix-test-repo-70", wantOK: false},
		{name: "review-test-repo-42", want: 42, wantOK: true},
		{name: "review-other-repo-42", wantOK: false},
		{name: "review-test-repo-x", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(tc.name, nil)
			sb.SetLabels(tc.labels)
			got, ok := prNumberFromSandbox(sb, "test-repo")
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("prNumberFromSandbox(%q) = %d, %v, want %d, %v", tc.name, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

type recordingWhyClosed struct {
	closedPRs []int
	polled    int
	reasons   map[int]string
}

func (w *recordingWhyClosed) OnClosedPR(_ context.Context, pr *githubv39.PullRequest) {
	w.closedPRs = append(w.closedPRs, pr.GetNumber())
}

func (w *recordingWhyClosed) Poll(_ context.Context) {
	w.polled++
}

func (w *recordingWhyClosed) CloseReason(prNum int) (string, bool) {
	if w.reasons != nil {
		r, ok := w.reasons[prNum]
		return r, ok
	}
	return "", false
}

func TestCollectGarbageNotifiesWhyClosedOnUnmergedPR(t *testing.T) {
	why := &recordingWhyClosed{}
	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   newFakeKubeClient(t, newSandbox("factory-pr-test-repo-7", map[string]interface{}{annotationLastTaskState: "Completed"})),
			GitHub: newClosedEntityGitHub(t),
		}),
		Locks:     concurrency.NewSandboxLockRegistry(),
		Entities:  warmCache(),
		WhyClosed: why,
	})

	r.CollectGarbage(context.Background())

	if len(why.closedPRs) != 1 || why.closedPRs[0] != 7 {
		t.Errorf("expected OnClosedPR for PR #7, got %v", why.closedPRs)
	}
	if why.polled != 1 {
		t.Errorf("expected Poll to be called once, got %d", why.polled)
	}
}

func TestCaptureFeedbackFiltering(t *testing.T) {
	mux := http.NewServeMux()

	prNum := 80
	headSHA := "abc1234"

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": prNum,
			"state":  "closed",
			"head":   map[string]string{"sha": headSHA},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"sha": headSHA, "commit": map[string]interface{}{
				"committer": map[string]string{"date": "2026-10-26T10:00:00Z"},
			}},
		})
	})

	// Reviews
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(1001),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"state":              "CHANGES_REQUESTED",
				"body":               "Please fix nil check",
				"submitted_at":       "2026-10-26T12:00:00Z",
				"commit_id":          headSHA,
			},
			{
				"id":                 int64(1002),
				"user":               map[string]string{"login": "dependabot[bot]", "type": "Bot"},
				"author_association": "NONE",
				"state":              "COMMENTED",
				"body":               "Automated bump",
				"submitted_at":       "2026-10-26T11:00:00Z",
				"commit_id":          headSHA,
			},
		})
	})

	// Inline comments
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(2001),
				"user":               map[string]string{"login": "bob", "type": "User"},
				"author_association": "COLLABORATOR",
				"path":               "pkg/foo.go",
				"line":               42,
				"in_reply_to_id":     int64(0),
				"body":               "Inline comment from Bob",
				"created_at":         "2026-10-26T12:05:00Z",
				"commit_id":          headSHA,
			},
			{
				"id":                 int64(2002),
				"user":               map[string]string{"login": "eve", "type": "User"},
				"author_association": "NONE",
				"path":               "pkg/foo.go",
				"line":               42,
				"body":               "Spam comment from untrusted Eve",
				"created_at":         "2026-10-26T12:06:00Z",
				"commit_id":          headSHA,
			},
			{
				"id":                 int64(2003),
				"user":               map[string]string{"login": "spock-watcher-bot", "type": "User"},
				"author_association": "COLLABORATOR",
				"path":               "pkg/foo.go",
				"line":               42,
				"body":               "Bot reply",
				"created_at":         "2026-10-26T12:07:00Z",
				"commit_id":          headSHA,
			},
		})
	})

	// Issue comments
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(3001),
				"user":               map[string]string{"login": "charlie", "type": "User"},
				"author_association": "NONE",
				"body":               "Issue comment from trusted login Charlie",
				"created_at":         "2026-10-26T12:10:00Z",
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	r := New(Config{
		TrustedLogins: []string{"charlie"},
	}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			GitHub: client,
		}),
	})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(prNum),
		State:  githubv39.String("closed"),
		Head:   &githubv39.PullRequestBranch{SHA: githubv39.String(headSHA)},
	}

	items := r.captureFeedback(context.Background(), pr, "factory-pr-80", prNum)

	if len(items) != 3 {
		t.Fatalf("expected 3 feedback items, got %d: %+v", len(items), items)
	}

	// 1. Alice's review
	if items[0].Author != "alice" || items[0].Kind != "review" || items[0].State != "CHANGES_REQUESTED" || items[0].Body != "Please fix nil check" || items[0].Head != headSHA {
		t.Errorf("unexpected item 0: %+v", items[0])
	}
	// 2. Bob's inline comment
	if items[1].Author != "bob" || items[1].Kind != "inline" || items[1].Path != "pkg/foo.go" || items[1].Line != 42 || items[1].Body != "Inline comment from Bob" {
		t.Errorf("unexpected item 1: %+v", items[1])
	}
	// 3. Charlie's issue comment
	if items[2].Author != "charlie" || items[2].Kind != "comment" || items[2].Body != "Issue comment from trusted login Charlie" {
		t.Errorf("unexpected item 2: %+v", items[2])
	}
}

func TestCaptureFeedbackAfterBotReview(t *testing.T) {
	mux := http.NewServeMux()
	prNum := 81
	headSHA := "commit-1"
	botReviewTime := "2026-10-26T11:00:00Z"

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": prNum,
			"state":  "closed",
			"head":   map[string]string{"sha": headSHA},
		})
	})
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"sha": headSHA, "commit": map[string]interface{}{
				"committer": map[string]string{"date": "2026-10-26T10:00:00Z"},
			}},
		})
	})

	// Bot reviewed headSHA at 11:00 and commented on pkg/raised.go line 10
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(500),
				"user":               map[string]string{"login": "reviewbot", "type": "User"},
				"author_association": "COLLABORATOR",
				"state":              "CHANGES_REQUESTED",
				"body":               "Bot review comments",
				"submitted_at":       botReviewTime,
				"commit_id":          headSHA,
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			// Bot comment on pkg/raised.go:10
			{
				"id":                     int64(501),
				"user":                   map[string]string{"login": "reviewbot", "type": "User"},
				"path":                   "pkg/raised.go",
				"line":                   10,
				"body":                   "Bot raised point",
				"created_at":             "2026-10-26T11:00:00Z",
				"commit_id":              headSHA,
				"pull_request_review_id": int64(500),
			},
			// Human comment on pkg/raised.go:10 after bot review -> bot raised it -> afterBotReview = false
			{
				"id":                 int64(502),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/raised.go",
				"line":               10,
				"body":               "Human agree on raised",
				"created_at":         "2026-10-26T12:00:00Z",
				"commit_id":          headSHA,
			},
			// Human comment on pkg/missed.go:20 after bot review -> bot did not raise -> afterBotReview = true
			{
				"id":                 int64(503),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/missed.go",
				"line":               20,
				"body":               "Human on missed file",
				"created_at":         "2026-10-26T12:01:00Z",
				"commit_id":          headSHA,
			},
			// Human comment BEFORE bot review -> afterBotReview = false
			{
				"id":                 int64(504),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/early.go",
				"line":               30,
				"body":               "Human before bot review",
				"created_at":         "2026-10-26T10:30:00Z",
				"commit_id":          headSHA,
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			// General comment after bot review -> afterBotReview = true
			{
				"id":                 int64(505),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"body":               "General comment after bot review",
				"created_at":         "2026-10-26T12:02:00Z",
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	r := New(Config{
		ReviewerLogins: []string{"reviewbot"},
	}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			GitHub: client,
		}),
	})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(prNum),
		State:  githubv39.String("closed"),
		Head:   &githubv39.PullRequestBranch{SHA: githubv39.String(headSHA)},
	}

	items := r.captureFeedback(context.Background(), pr, "factory-pr-81", prNum)

	itemByBody := make(map[string]bool)
	for _, it := range items {
		itemByBody[it.Body] = it.AfterBotReview
	}

	if got := itemByBody["Human agree on raised"]; got != false {
		t.Errorf("expected afterBotReview=false for comment bot already raised, got %v", got)
	}
	if got := itemByBody["Human on missed file"]; got != true {
		t.Errorf("expected afterBotReview=true for comment bot missed, got %v", got)
	}
	if got := itemByBody["Human before bot review"]; got != false {
		t.Errorf("expected afterBotReview=false for comment before bot reviewed, got %v", got)
	}
	if got := itemByBody["General comment after bot review"]; got != true {
		t.Errorf("expected afterBotReview=true for general comment after bot review, got %v", got)
	}
}

func TestCaptureFeedbackAnsweredBy(t *testing.T) {
	mux := http.NewServeMux()
	prNum := 82
	headSHA := "commit-1"

	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": prNum,
			"state":  "closed",
			"head":   map[string]string{"sha": headSHA},
		})
	})
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"sha": headSHA, "commit": map[string]interface{}{
				"committer": map[string]string{"date": "2026-10-26T10:00:00Z"},
			}},
		})
	})
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(601),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/foo.go",
				"line":               10,
				"body":               "Fix this bug",
				"created_at":         "2026-10-26T12:00:00Z",
				"commit_id":          headSHA,
			},
			{
				"id":                 int64(602),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/bar.go",
				"line":               20,
				"body":               "Why this approach?",
				"created_at":         "2026-10-26T12:01:00Z",
				"commit_id":          headSHA,
			},
			{
				"id":                 int64(603),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "MEMBER",
				"path":               "pkg/baz.go",
				"line":               30,
				"body":               "Unanswered comment",
				"created_at":         "2026-10-26T12:02:00Z",
				"commit_id":          headSHA,
			},
		})
	})
	mux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	// Mock task server
	fakeTS := &fakeTaskServer{
		entries: []spool.Entry{
			{Task: spool.Task{ID: "recipe-care-comments-1"}},
			{Task: spool.Task{ID: "recipe-care-comments-2"}},
		},
		files: map[string]string{
			"recipe-care-comments-1/comments.jsonl": `{"id":601,"body":"Fix this bug"}` + "\n",
			"recipe-care-comments-1/push.json":      `{"head":"pushed-sha-2","unchanged":false}`,
			"recipe-care-comments-2/comments.jsonl": `{"id":602,"body":"Why this approach?"}` + "\n",
			"recipe-care-comments-2/push.json":      `{"unchanged":true}`,
			"recipe-care-comments-2/execution.log":  "Posting reply via gh api repos/o/r/pulls/comments/602/replies\n",
		},
	}
	withTasks(t, fakeTS, nil)

	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			GitHub: client,
		}),
	})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(prNum),
		State:  githubv39.String("closed"),
		Head:   &githubv39.PullRequestBranch{SHA: githubv39.String(headSHA)},
	}

	items := r.captureFeedback(context.Background(), pr, "factory-pr-82", prNum)

	ansByBody := make(map[string]*tokenusage.AnsweredBy)
	for _, it := range items {
		ansByBody[it.Body] = it.AnsweredBy
	}

	// 1. "Fix this bug" -> fixed by recipe-care-comments-1
	ans1 := ansByBody["Fix this bug"]
	if ans1 == nil || ans1.Task != "recipe-care-comments-1" || ans1.Outcome != "fixed" {
		t.Errorf("expected answeredBy {recipe-care-comments-1, fixed}, got %+v", ans1)
	}

	// 2. "Why this approach?" -> replied by recipe-care-comments-2
	ans2 := ansByBody["Why this approach?"]
	if ans2 == nil || ans2.Task != "recipe-care-comments-2" || ans2.Outcome != "replied" {
		t.Errorf("expected answeredBy {recipe-care-comments-2, replied}, got %+v", ans2)
	}

	// 3. "Unanswered comment" -> nil
	ans3 := ansByBody["Unanswered comment"]
	if ans3 != nil {
		t.Errorf("expected answeredBy=nil for unanswered comment, got %+v", ans3)
	}
}

func TestCollectGarbageUploadsFeedback(t *testing.T) {
	prNum := 83
	headSHA := "sha-83"

	// Mock collector
	var uploadedFeedback []string
	collectorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == fmt.Sprintf("/v1/prs/test-owner/test-repo/%d/feedback", prNum) {
			body, _ := io.ReadAll(r.Body)
			uploadedFeedback = append(uploadedFeedback, string(body))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collectorSrv.Close()

	t.Setenv("COLLECTOR_URL", collectorSrv.URL)

	// Mock GitHub
	ghMux := http.NewServeMux()
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": prNum,
			"state":  "closed",
			"head":   map[string]string{"sha": headSHA},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"sha": headSHA, "commit": map[string]interface{}{
				"committer": map[string]string{"date": "2026-10-26T10:00:00Z"},
			}},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":                 int64(701),
				"user":               map[string]string{"login": "alice", "type": "User"},
				"author_association": "OWNER",
				"body":               "Closed with feedback",
				"created_at":         "2026-10-26T12:00:00Z",
			},
		})
	})

	ghSrv := httptest.NewServer(ghMux)
	defer ghSrv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(ghSrv.URL + "/")

	sbName := fmt.Sprintf("factory-pr-%d", prNum)
	kube := newFakeKubeClient(t,
		newSandbox(sbName, map[string]interface{}{annotationLastTaskState: "Completed"}),
	)

	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   kube,
			GitHub: client,
		}),
		Locks:    concurrency.NewSandboxLockRegistry(),
		Entities: warmCache(),
	})

	r.CollectGarbage(context.Background())

	if sandboxExists(t, r, sbName) {
		t.Errorf("expected sandbox %s to be deleted", sbName)
	}

	if len(uploadedFeedback) != 1 {
		t.Fatalf("expected 1 feedback upload, got %d", len(uploadedFeedback))
	}

	if !strings.Contains(uploadedFeedback[0], "Closed with feedback") || !strings.Contains(uploadedFeedback[0], `"author":"alice"`) {
		t.Errorf("uploaded feedback content unexpected:\n%s", uploadedFeedback[0])
	}
}

func TestCollectGarbageUploadsOutcomeRecord(t *testing.T) {
	prNum := 99
	agentHead := "agent-head-99"
	finalHead := "final-head-99"

	// Mock collector
	var uploadedOutcomes []tokenusage.OutcomeRecord
	collectorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == fmt.Sprintf("/v1/outcomes/test-owner/test-repo/%d", prNum) {
			var rec tokenusage.OutcomeRecord
			if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
				t.Errorf("decoding outcome: %v", err)
			}
			uploadedOutcomes = append(uploadedOutcomes, rec)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collectorSrv.Close()

	t.Setenv("COLLECTOR_URL", collectorSrv.URL)

	// Mock task server
	fakeTS := &fakeTaskServer{
		entries: []spool.Entry{
			{Task: spool.Task{ID: "recipe-care-1"}},
			{Task: spool.Task{ID: "recipe-fix-1"}},
		},
		files: map[string]string{
			"recipe-care-1/push.json": fmt.Sprintf(`{"head": %q}`, agentHead),
		},
	}
	withTasks(t, fakeTS, nil)

	// Mock GitHub
	ghMux := http.NewServeMux()
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": prNum,
			"state":  "closed",
			"merged": false,
			"head": map[string]string{
				"sha": finalHead,
				"ref": "fix-branch",
			},
			"base": map[string]string{
				"ref": "main",
			},
			"user": map[string]string{
				"login": "gemini-bot",
				"type":  "Bot",
			},
			"body": "Fixes #42. <sub>🤖 [Agent session](https://example.com/task-session/sb/task)</sub>",
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"state": "CHANGES_REQUESTED"},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 1, "body": "inline 1"},
			{"id": 2, "body": "inline 2"},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", prNum), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/compare/main...%s", agentHead), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"filename": "pkg/foo.go", "sha": "sha-old", "additions": 10, "deletions": 2},
			},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/compare/main...%s", finalHead), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"filename": "pkg/foo.go", "sha": "sha-new", "additions": 14, "deletions": 5},
				{"filename": "pkg/bar.go", "sha": "sha-bar", "additions": 3, "deletions": 1},
			},
		})
	})

	ghSrv := httptest.NewServer(ghMux)
	defer ghSrv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(ghSrv.URL + "/")

	sbName := fmt.Sprintf("factory-pr-%d", prNum)
	kube := newFakeKubeClient(t,
		newSandbox(sbName, map[string]interface{}{annotationLastTaskState: "Completed"}),
	)

	why := &recordingWhyClosed{
		reasons: map[int]string{
			prNum: "superseded by #100",
		},
	}

	r := New(Config{}, Deps{
		Sandboxes: NewService(ServiceConfig{Namespace: testNamespace, Owner: "test-owner", Repo: "test-repo"}, ServiceDeps{
			Kube:   kube,
			GitHub: client,
		}),
		Locks:     concurrency.NewSandboxLockRegistry(),
		Entities:  warmCache(),
		WhyClosed: why,
	})

	r.CollectGarbage(context.Background())

	if len(uploadedOutcomes) != 1 {
		t.Fatalf("expected 1 outcome uploaded, got %d", len(uploadedOutcomes))
	}

	outcome := uploadedOutcomes[0]
	if outcome.Repo != "test-owner/test-repo" || outcome.PR != prNum {
		t.Errorf("repo/PR unexpected: %s#%d", outcome.Repo, outcome.PR)
	}
	if outcome.State != "closed" {
		t.Errorf("state: expected closed, got %s", outcome.State)
	}
	if outcome.CloseReason != "superseded by #100" {
		t.Errorf("closeReason: expected 'superseded by #100', got %q", outcome.CloseReason)
	}
	if !outcome.OpenedByBot {
		t.Errorf("openedByBot: expected true")
	}
	if !outcome.HasSessionLink {
		t.Errorf("hasSessionLink: expected true")
	}
	if outcome.CarePasses != 1 {
		t.Errorf("carePasses: expected 1, got %d", outcome.CarePasses)
	}
	if outcome.ChangesRequested != 1 {
		t.Errorf("changesRequested: expected 1, got %d", outcome.ChangesRequested)
	}
	if outcome.ReviewComments != 2 {
		t.Errorf("reviewComments: expected 2, got %d", outcome.ReviewComments)
	}
	if outcome.AgentHead != agentHead {
		t.Errorf("agentHead: expected %s, got %s", agentHead, outcome.AgentHead)
	}
	if outcome.HumanEdit == nil {
		t.Fatalf("humanEdit was nil")
	}
	if outcome.HumanEdit.Files != 2 {
		t.Errorf("humanEdit files: expected 2, got %d", outcome.HumanEdit.Files)
	}
	if outcome.HumanEdit.Added != 7 || outcome.HumanEdit.Deleted != 4 {
		t.Errorf("humanEdit added/deleted: expected 7/4, got %d/%d", outcome.HumanEdit.Added, outcome.HumanEdit.Deleted)
	}
	if len(outcome.HumanEdit.Paths) != 2 || outcome.HumanEdit.Paths[0] != "pkg/bar.go" || outcome.HumanEdit.Paths[1] != "pkg/foo.go" {
		t.Errorf("humanEdit paths: expected [pkg/bar.go, pkg/foo.go], got %v", outcome.HumanEdit.Paths)
	}
}

func TestComputeHumanEditRebaseNoChange(t *testing.T) {
	agentHead := "agent-head-1"
	finalHead := "final-head-1"

	ghMux := http.NewServeMux()
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/compare/main...%s", agentHead), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"filename": "pkg/foo.go", "sha": "sha-same", "additions": 10, "deletions": 2},
			},
		})
	})
	ghMux.HandleFunc(fmt.Sprintf("/repos/test-owner/test-repo/compare/main...%s", finalHead), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"filename": "pkg/foo.go", "sha": "sha-same", "additions": 10, "deletions": 2},
			},
		})
	})

	ghSrv := httptest.NewServer(ghMux)
	defer ghSrv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(ghSrv.URL + "/")

	r := &Reconciler{
		sandboxes: &Service{
			owner: "test-owner",
			repo:  "test-repo",
			gh:    client,
		},
	}

	edit := r.computeHumanEdit(context.Background(), "main", agentHead, finalHead)
	if edit == nil {
		t.Fatalf("expected non-nil edit")
	}
	if edit.Files != 0 || edit.Added != 0 || edit.Deleted != 0 || len(edit.Paths) != 0 {
		t.Errorf("expected 0 files changed on clean rebase, got %+v", edit)
	}
}
