package repoboard

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
)

// A board is reconciled every minute while someone is viewing it or work
// stands on it, and every ten minutes otherwise.
func TestRequeueFollowsViewersAndWork(t *testing.T) {
	now := time.Now()
	board := func(viewedAt string) *boardv1alpha1.RepoBoard {
		b := &boardv1alpha1.RepoBoard{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "alice"}}
		if viewedAt != "" {
			b.Annotations = map[string]string{AnnotationViewedAt: viewedAt}
		}
		return b
	}
	at := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	for _, tc := range []struct {
		name     string
		viewedAt string
		busy     bool
		want     time.Duration
	}{
		{"never viewed, idle", "", false, idleRequeue},
		{"viewed a minute ago", at(time.Minute), false, defaultRequeue},
		{"viewed an hour ago", at(time.Hour), false, idleRequeue},
		{"never viewed, work standing", "", true, defaultRequeue},
		{"a bad stamp", "yesterday", false, idleRequeue},
	} {
		if got := requeueAfter(board(tc.viewedAt), tc.busy, now); got != tc.want {
			t.Errorf("%s: requeue %v, want %v", tc.name, got, tc.want)
		}
	}
}

// GitHub discovery runs at most once per DiscoveryEvery per board: the
// reconciles a Request's status writes cause settle without listing
// GitHub again; zero discovers every time.
func TestDiscoveryIsThrottledPerBoard(t *testing.T) {
	now := time.Now()
	r := &Reconciler{DiscoveryEvery: time.Minute}
	if !r.discoveryDue("alice/b", now) {
		t.Fatal("first discovery not due")
	}
	if r.discoveryDue("alice/b", now.Add(10*time.Second)) {
		t.Error("discovered again within the minute")
	}
	if !r.discoveryDue("alice/other", now.Add(10*time.Second)) {
		t.Error("another board was throttled by this one")
	}
	if !r.discoveryDue("alice/b", now.Add(61*time.Second)) {
		t.Error("not discovered again after the minute")
	}
	unthrottled := &Reconciler{}
	for i := range 2 {
		if !unthrottled.discoveryDue("alice/b", now) {
			t.Errorf("zero DiscoveryEvery throttled discovery %d", i+1)
		}
	}
}

// A reconcile within the throttle does not list GitHub: the owner's
// identity and automation's listings wait for the next due one.
func TestThrottledReconcileSkipsGitHub(t *testing.T) {
	ghClient := testGithubClient(`[{"number": 20, "title": "assigned", "html_url": "https://github.com/test/repo/issues/20", "assignees": [{"login": "alice"}]}]`)
	calls := 0
	r := newTestReconciler(newFakeLauncher(), ghClient, testBoard(nil), githubSecret())
	inner := r.NewGithubClient
	r.NewGithubClient = func(ctx context.Context, rr *Reconciler, ns string) (*github.Client, string, error) {
		calls++
		return inner(ctx, rr, ns)
	}
	r.DiscoveryEvery = time.Minute
	for range 3 {
		if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("discovery ran %d times in three rapid reconciles, want 1", calls)
	}
}
