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
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

type fakeLaunch struct {
	Key         string
	FixOpts     *factorycli.FixOptions
	ReviewOpts  *factorycli.ReviewOptions
	PRWatchOpts *factorycli.PRWatchOptions
	TriageOpts  *factorycli.TriageOptions
}

type fakeLauncher struct {
	mu      sync.Mutex
	calls   []fakeLaunch
	running map[string]bool
	results map[string]factorycli.Result
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{running: map[string]bool{}, results: map[string]factorycli.Result{}}
}

func (f *fakeLauncher) StartFix(key string, opts factorycli.FixOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, FixOpts: &opts})
	return true
}

func (f *fakeLauncher) StartReview(key string, opts factorycli.ReviewOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, ReviewOpts: &opts})
	return true
}

func (f *fakeLauncher) StartPRWatch(key string, opts factorycli.PRWatchOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, PRWatchOpts: &opts})
	return true
}

func (f *fakeLauncher) StartTriage(key string, opts factorycli.TriageOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, TriageOpts: &opts})
	return true
}

func (f *fakeLauncher) IsRunning(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[key]
}

func (f *fakeLauncher) LastResult(key string) (factorycli.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.results[key]
	return res, ok
}

func (f *fakeLauncher) launches() []fakeLaunch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeLaunch(nil), f.calls...)
}

type mockRoundTripper struct {
	responses map[string]func() *http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if fn, ok := m.responses[req.URL.String()]; ok {
		resp := fn()
		resp.Header = http.Header{"Content-Type": []string{"application/json"}}
		resp.Request = req
		return resp, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req, Header: http.Header{}}, nil
}

func jsonResp(body string) func() *http.Response {
	return func() *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	}
}

func testGithubClient(issuesJSON string) *github.Client {
	return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice", "email": "alice@example.com"}`),
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(issuesJSON),
	}}})
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = boardv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)
	return s
}

func testBoard(annotations map[string]string) *boardv1alpha1.RepoBoard {
	discreet := true
	return &boardv1alpha1.RepoBoard{
		ObjectMeta: metav1.ObjectMeta{Name: "test-board", Namespace: "alice", Annotations: annotations},
		Spec: boardv1alpha1.RepoBoardSpec{
			RepoURL:  "https://github.com/test/repo",
			Access:   boardv1alpha1.AccessSpec{Mode: "list", Allow: []string{"alice"}},
			Triggers: boardv1alpha1.TriggersSpec{Label: "agent", Discreet: &discreet},
			Limits:   boardv1alpha1.LimitsSpec{MaxActive: 5, MaxActivePerUser: 2},
			Sandbox:  boardv1alpha1.SandboxSpec{DiskSize: "10Gi", IdleMinutes: 60},
		},
	}
}

func githubSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "alice"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_alice")},
	}
}

func newTestReconciler(fake *fakeLauncher, ghClient *github.Client, objs ...runtime.Object) *Reconciler {
	builder := clientfake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&boardv1alpha1.RepoBoard{})
	for _, o := range objs {
		builder = builder.WithRuntimeObjects(o)
	}
	return &Reconciler{
		Client:  builder.Build(),
		Scheme:  testScheme(),
		Factory: fake,
		NewGithubClient: func(_ context.Context, _ *Reconciler, _ string) (*github.Client, string, error) {
			return ghClient, "gho_alice", nil
		},
	}
}

func boardRequest() reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-board", Namespace: "alice"}}
}

// A labeled issue assigned to the member launches a fix (with the draft-PR
// instruction); a labeled issue assigned to someone else never executes
// (executor-consent rule).
func TestLabelDiscovery_ExecutorConsent(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[
		{"number": 10, "title": "mine", "html_url": "https://github.com/test/repo/issues/10",
		 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]},
		{"number": 11, "title": "bobs", "html_url": "https://github.com/test/repo/issues/11",
		 "labels": [{"name": "agent"}], "assignees": [{"login": "bob"}]},
		{"number": 12, "title": "unassigned", "html_url": "https://github.com/test/repo/issues/12",
		 "labels": [{"name": "agent"}], "assignees": []}
	]`)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/fix-10"))
	g.Expect(launches[0].FixOpts.IssueURL).To(gomega.Equal("https://github.com/test/repo/issues/10"))
	g.Expect(launches[0].FixOpts.Instruction).To(gomega.ContainSubstring("draft pull request"))
	g.Expect(launches[0].FixOpts.GithubToken).To(gomega.Equal("gho_alice"))

	// factory-user secret materialized for the executor namespace.
	secret := &corev1.Secret{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-user", Namespace: "alice"}, secret)).To(gomega.Succeed())
	g.Expect(secret.Data["GITHUB_LOGIN"]).To(gomega.Equal([]byte("alice")))
	g.Expect(secret.Data["GITHUB_TOKEN"]).To(gomega.Equal([]byte("gho_alice")))
}

// A completed fix is not relaunched; a re-fix request overrides the terminal
// state.
func TestFixTerminalAndRefix(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[
		{"number": 10, "title": "mine", "html_url": "https://github.com/test/repo/issues/10",
		 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]}
	]`)

	completedSandbox := func(extra map[string]interface{}) *unstructured.Unstructured {
		annotations := map[string]interface{}{
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"sandbox.gemini.google.com/completion-time": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"htmlURL": "https://github.com/test/repo/issues/10",
		}
		for k, v := range extra {
			annotations[k] = v
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name": "fix-repo-10", "namespace": "alice",
				"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
				"annotations": annotations,
			},
			"spec": map[string]interface{}{"replicas": int64(0)},
		}}
	}

	// Terminal: no relaunch.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), completedSandbox(nil))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// Re-fix requested after completion: relaunch.
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, ghClient, testBoard(nil), githubSecret(), completedSandbox(map[string]interface{}{
		"review.gemini.google.com/refix-requested-at": time.Now().UTC().Format(time.RFC3339),
	}))
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.HaveLen(1))
	g.Expect(fake2.launches()[0].Key).To(gomega.Equal("alice/fix-10"))
}

// A finished review invocation's stdout draft is harvested onto the sandbox.
func TestMailboxReviewPendingOnGithub(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	prSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "factory-pr-42", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/42"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	// The invocation ran with --publish draft: the pending review is already
	// on GitHub; no banner output to harvest.
	fake.results["alice/review-pr-42"] = factorycli.Result{Output: "Posting review as a draft (pending) review to GitHub PR...\n"}
	// Mailbox holds the review request so ensureReview runs for PR 42.
	board := testBoard(map[string]string{AnnotationRequests: `{"review-42": "alice"}`})
	r := newTestReconciler(fake, ghClient, board, githubSecret(), prSandbox)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-pr-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewState]).To(gomega.Equal("pending"))
	g.Expect(updated.GetAnnotations()[AnnotationReviewedAt]).NotTo(gomega.BeEmpty())
	g.Expect(updated.GetAnnotations()[AnnotationAgentDraft]).To(gomega.BeEmpty())

	// No launch happened (completion short-circuits), and the mailbox entry
	// is cleared because the sandbox exists — after persisting the executor
	// on the sandbox so resumes keep the draft-publish identity.
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(updated.GetAnnotations()[AnnotationExecutor]).To(gomega.Equal("alice"))
	fetched := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), boardRequest().NamespacedName, fetched)).To(gomega.Succeed())
	g.Expect(fetched.GetAnnotations()).NotTo(gomega.HaveKey(AnnotationRequests))
}

// The production loop: a clicked review finished (pending posted on GitHub)
// but the mailbox was gone and the sandbox carried no executor stamp — the
// resume path reprocessed it as a draft-harvest, found no banner, and
// relaunched forever. The invocation output's draft-posted marker must win.
func TestResumeRecognizesPostedDraftWithoutExecutor(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	prSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "factory-pr-42", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/42"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/review-pr-42"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "...\nPosting review as a draft (pending) review to GitHub PR...\n",
	}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), prSandbox)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-pr-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewState]).To(gomega.Equal("pending"))
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A mailbox review click launches in the clicker's namespace under their
// identity with --publish draft (the pending review must be authored by
// them to be visible to them).
func TestMailboxReviewLaunchAsExecutor(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"review-42": "alice"}`})
	r := newTestReconciler(fake, ghClient, board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-pr-42"))
	g.Expect(launches[0].ReviewOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].ReviewOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].ReviewOpts.Publish).To(gomega.Equal("draft"))
	g.Expect(launches[0].ReviewOpts.GithubToken).To(gomega.Equal("gho_alice"))
}

// Mailbox fix requests launch and stay queued until the sandbox exists.
func TestMailboxFix(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)
	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"fix-77": "alice"}`})
	r := newTestReconciler(fake, ghClient, board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/fix-77"))
	g.Expect(launches[0].FixOpts.IssueURL).To(gomega.Equal("https://github.com/test/repo/issues/77"))

	// No sandbox yet: the request stays queued for the next reconcile.
	fetched := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), boardRequest().NamespacedName, fetched)).To(gomega.Succeed())
	g.Expect(fetched.GetAnnotations()[AnnotationRequests]).To(gomega.ContainSubstring("fix-77"))
}

// A fix sandbox aliased to a PR gets a pr-watch follow-up.
func TestFollowUpPRWatch(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	aliased := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-10", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "101",
			},
			"annotations": map[string]interface{}{
				"sandbox.gemini.google.com/last-task-state": "Completed",
				"htmlURL": "https://github.com/test/repo/pull/101",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), aliased)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/prwatch-101"))
	g.Expect(launches[0].PRWatchOpts.PRURL).To(gomega.Equal("https://github.com/test/repo/pull/101"))
}

// Shared board: execution routes to the consenting assignee's namespace with
// their identity; a label applied by someone else never executes (degrades
// to awaiting-go).
func TestSharedBoardExecutorConsent(t *testing.T) {
	g := gomega.NewWithT(t)

	sharedBoard := &boardv1alpha1.RepoBoard{
		ObjectMeta: metav1.ObjectMeta{Name: "kcc", Namespace: "board-kcc"},
		Spec: boardv1alpha1.RepoBoardSpec{
			RepoURL:      "https://github.com/test/repo",
			Access:       boardv1alpha1.AccessSpec{Mode: "github"},
			Triggers:     boardv1alpha1.TriggersSpec{Label: "agent"},
			Limits:       boardv1alpha1.LimitsSpec{MaxActive: 5, MaxActivePerUser: 2},
			PrepIdentity: boardv1alpha1.PrepIdentitySpec{SecretName: "prep-bot"},
		},
	}
	prepSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "prep-bot", Namespace: "board-kcc"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("gho_prep")},
	}
	bobSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "bob"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_bob")},
	}

	mkClient := func(events10, events11 string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
			"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(`[
				{"number": 10, "title": "bob self-labeled", "html_url": "https://github.com/test/repo/issues/10",
				 "labels": [{"name": "agent"}], "assignees": [{"login": "bob"}]},
				{"number": 11, "title": "carol labeled for bob", "html_url": "https://github.com/test/repo/issues/11",
				 "labels": [{"name": "agent"}], "assignees": [{"login": "bob"}]}
			]`),
			"https://api.github.com/repos/test/repo/issues/10/events?per_page=100": jsonResp(events10),
			"https://api.github.com/repos/test/repo/issues/11/events?per_page=100": jsonResp(events11),
		}}})
	}
	ghClient := mkClient(
		`[{"event": "labeled", "label": {"name": "agent"}, "actor": {"login": "bob"}}]`,
		`[{"event": "labeled", "label": {"name": "agent"}, "actor": {"login": "carol"}}]`,
	)

	prevFromToken := newGithubClientFromToken
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client { return ghClient }
	t.Cleanup(func() { newGithubClientFromToken = prevFromToken })

	fake := newFakeLauncher()
	builder := clientfake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&boardv1alpha1.RepoBoard{}).
		WithObjects(sharedBoard, prepSecret, bobSecret)
	r := &Reconciler{
		Client:  builder.Build(),
		Scheme:  testScheme(),
		Factory: fake,
		// Board namespace has no github-pat: personal path fails, prep path
		// engages.
		NewGithubClient: func(_ context.Context, _ *Reconciler, ns string) (*github.Client, string, error) {
			return nil, "", fmt.Errorf("no github-pat in %s", ns)
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "kcc", Namespace: "board-kcc"}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Only issue 10 (labeled by its assignee bob) executes — as bob, in
	// bob's namespace, with bob's token.
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("bob/fix-10"))
	g.Expect(launches[0].FixOpts.Namespace).To(gomega.Equal("bob"))
	g.Expect(launches[0].FixOpts.GithubToken).To(gomega.Equal("gho_bob"))

	// Bob's factory-user secret was materialized in bob's namespace.
	secret := &corev1.Secret{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-user", Namespace: "bob"}, secret)).To(gomega.Succeed())
	g.Expect(secret.Data["GITHUB_LOGIN"]).To(gomega.Equal([]byte("bob")))
}

// Two-key auto-fix: a label applied by a triager executes for an assignee
// who holds the standing opt-in (forced draft PR); without the opt-in the
// item stays awaiting-go.
func TestAutoFixTwoKeyConsent(t *testing.T) {
	g := gomega.NewWithT(t)

	falseVal := false
	mkBoard := func() *boardv1alpha1.RepoBoard {
		return &boardv1alpha1.RepoBoard{
			ObjectMeta: metav1.ObjectMeta{Name: "kcc", Namespace: "board-kcc"},
			Spec: boardv1alpha1.RepoBoardSpec{
				RepoURL:      "https://github.com/test/repo",
				Access:       boardv1alpha1.AccessSpec{Mode: "github"},
				Triggers:     boardv1alpha1.TriggersSpec{Label: "agent"},
				Intake:       boardv1alpha1.IntakeSpec{AutoFix: boardv1alpha1.AutoFixSpec{Enabled: true, Require: []string{"assigned", "label"}}},
				Limits:       boardv1alpha1.LimitsSpec{MaxActive: 5, MaxActivePerUser: 2},
				Policy:       boardv1alpha1.PolicySpec{DraftPR: &falseVal}, // rails override policy
				PrepIdentity: boardv1alpha1.PrepIdentitySpec{SecretName: "prep-bot"},
			},
		}
	}
	prepSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "prep-bot", Namespace: "board-kcc"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("gho_prep")},
	}
	bobSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "bob"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_bob")},
	}
	optIn := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-preferences", Namespace: "bob"},
		Data:       map[string]string{AutoFixPreferenceKey("board-kcc", "kcc"): "true"},
	}

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(`[
			{"number": 20, "title": "triaged to bob", "html_url": "https://github.com/test/repo/issues/20",
			 "labels": [{"name": "agent"}], "assignees": [{"login": "bob"}]}
		]`),
		"https://api.github.com/repos/test/repo/issues/20/events?per_page=100": jsonResp(
			`[{"event": "labeled", "label": {"name": "agent"}, "actor": {"login": "carol"}}]`),
	}}})

	prevFromToken := newGithubClientFromToken
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client { return ghClient }
	t.Cleanup(func() { newGithubClientFromToken = prevFromToken })

	failPersonal := func(_ context.Context, _ *Reconciler, ns string) (*github.Client, string, error) {
		return nil, "", fmt.Errorf("no github-pat in %s", ns)
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "kcc", Namespace: "board-kcc"}}

	// With bob's opt-in: executes as bob with the forced draft-PR rail.
	fake := newFakeLauncher()
	r := &Reconciler{
		Client:          clientfake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&boardv1alpha1.RepoBoard{}).WithObjects(mkBoard(), prepSecret, bobSecret, optIn).Build(),
		Scheme:          testScheme(),
		Factory:         fake,
		NewGithubClient: failPersonal,
	}
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("bob/fix-20"))
	g.Expect(fake.launches()[0].FixOpts.Instruction).To(gomega.ContainSubstring("draft pull request"))

	// Without the opt-in: never executes.
	fake2 := newFakeLauncher()
	r2 := &Reconciler{
		Client:          clientfake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&boardv1alpha1.RepoBoard{}).WithObjects(mkBoard(), prepSecret, bobSecret).Build(),
		Scheme:          testScheme(),
		Factory:         fake2,
		NewGithubClient: failPersonal,
	}
	_, err = r2.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
}

// Draft-review intake prepares a review for every inbound PR.
func TestDraftReviewIntake(t *testing.T) {
	g := gomega.NewWithT(t)

	board := testBoard(nil)
	board.Spec.Triggers.Label = ""
	board.Spec.Intake.DraftReviews = true

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": jsonResp(`[
			{"number": 5, "title": "a", "labels": []},
			{"number": 6, "title": "b", "labels": [{"name": "no-agent"}]}
		]`),
	}}})

	board.Spec.Intake.Filters.ExcludeLabels = []string{"no-agent"}
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-pr-5"))
	g.Expect(launches[0].ReviewOpts).NotTo(gomega.BeNil())
	// Personal board: the member is the executor — attributed, publish draft.
	g.Expect(launches[0].ReviewOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].ReviewOpts.Publish).To(gomega.Equal("draft"))
	g.Expect(launches[0].ReviewOpts.GithubToken).To(gomega.Equal("gho_alice"))
}

// Triage intake: candidates launch the triage agent; a finished run's
// report is harvested from the sandbox and stored as a draft.
func TestTriageIntake(t *testing.T) {
	g := gomega.NewWithT(t)

	board := testBoard(nil)
	board.Spec.Intake.TriageIssues = true

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(`[]`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 30, "title": "untriaged", "html_url": "https://github.com/test/repo/issues/30", "labels": []},
			{"number": 31, "title": "already fixing", "html_url": "https://github.com/test/repo/issues/31", "labels": [{"name": "agent"}]}
		]`),
	}}})

	// Phase 1: launch.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/triage-repo-30"))
	g.Expect(launches[0].TriageOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].TriageOpts.IssueURL).To(gomega.Equal("https://github.com/test/repo/issues/30"))

	// Phase 2: harvest after completion.
	triageSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "triage-repo-30", "namespace": "alice",
			"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/issues/30"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	fake2 := newFakeLauncher()
	fake2.results["alice/triage-repo-30"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "...\n================= ISSUE TRIAGE =================\ntriage:\n  labels: [bug]\n  priority: high\n  assessment: broken\n================================================\n",
	}
	r2 := newTestReconciler(fake2, ghClient, testBoardWithTriage(), githubSecret(), triageSandbox)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Name: "triage-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationAgentDraft]).To(gomega.ContainSubstring("priority: high"))
	g.Expect(updated.GetAnnotations()[AnnotationDraftType]).To(gomega.Equal("triage"))
	g.Expect(updated.GetAnnotations()[AnnotationTriagedAt]).NotTo(gomega.BeEmpty())
}

func testBoardWithTriage() *boardv1alpha1.RepoBoard {
	b := testBoard(nil)
	b.Spec.Intake.TriageIssues = true
	return b
}

// A token that cannot answer GET /user (e.g. a CI installation token) still
// authenticates when the github-pat secret records the identity in its
// name/email keys; the factory-user secret uses that fallback login.
func TestDiscoveryIdentityFallbackFromSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": func() *http.Response {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"message": "Resource not accessible by integration"}`))}
		},
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(`[]`),
	}}})

	secret := githubSecret()
	secret.Data["name"] = []byte("ci-bot")
	secret.Data["email"] = []byte("ci-bot@example.com")

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), secret)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	board := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), boardRequest().NamespacedName, board)).To(gomega.Succeed())
	var auth *metav1.Condition
	for i := range board.Status.Conditions {
		if board.Status.Conditions[i].Type == "Auth" {
			auth = &board.Status.Conditions[i]
		}
	}
	g.Expect(auth).NotTo(gomega.BeNil())
	g.Expect(string(auth.Status)).To(gomega.Equal("True"))

	fu := &corev1.Secret{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-user", Namespace: "alice"}, fu)).To(gomega.Succeed())
	g.Expect(fu.Data["GITHUB_LOGIN"]).To(gomega.Equal([]byte("ci-bot")))
	g.Expect(fu.Data["GITHUB_EMAIL"]).To(gomega.Equal([]byte("ci-bot@example.com")))
}

// A board created without a limits block (API-server defaulting does not
// reach absent parent objects) must not silently block every launch: zero
// limits mean the CRD defaults.
func TestMailboxReviewLaunchesWithoutLimitsBlock(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	board := testBoard(map[string]string{AnnotationRequests: `{"review-1163": "alice"}`})
	board.Spec.Limits = boardv1alpha1.LimitsSpec{} // UI-created boards omit limits entirely
	board.Spec.Triggers = boardv1alpha1.TriggersSpec{}

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-pr-1163"))
	g.Expect(launches[0].ReviewOpts.Publish).To(gomega.Equal("draft"))
}

// Review drafts are no longer harvested into annotations — a finished
// invocation either shows the draft-posted marker (pending on GitHub) or
// gets the standard backoff; legacy banner output stores nothing and must
// not hot-loop the agent.
func TestNoDraftHarvestAndNoHotLoop(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	sandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "factory-pr-42", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/42"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/review-pr-42"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "Reading output...\n\n================= CODE REVIEW =================\nreview:\n  body: legacy banner\n===============================================\n",
	}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-pr-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationAgentDraft]).To(gomega.BeEmpty())
	g.Expect(updated.GetAnnotations()[AnnotationReviewState]).To(gomega.BeEmpty())
	// Recent unrecognizable result: back off, no relaunch.
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// Once the member submits their pending review on GitHub, the row settles:
// reviewState flips pending -> submitted and stops demanding attention.
func TestSettleSubmittedReview(t *testing.T) {
	g := gomega.NewWithT(t)

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
	}}})
	reviewsClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/repos/test/repo/pulls/42/reviews?per_page=100": jsonResp(`[
			{"id": 1, "state": "COMMENTED", "user": {"login": "alice"}}
		]`),
	}}})
	prev := newGithubClientFromToken
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client { return reviewsClient }
	defer func() { newGithubClientFromToken = prev }()

	sandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "factory-pr-42", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{
				"htmlURL":                          "https://github.com/test/repo/pull/42",
				"reviewState":                      "pending",
				"board.gemini.google.com/executor": "alice",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(0)},
	}}

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-pr-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewState]).To(gomega.Equal("submitted"))
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A clicked triage's mailbox entry is consumed at sandbox creation, minutes
// before completion — the resume pass must harvest the finished result.
func TestResumeTriageHarvest(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	triageSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "triage-repo-30", "namespace": "alice",
			"labels": map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/30",
				"sandbox.gemini.google.com/last-task-state": "Completed",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/triage-repo-30"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "...\n================= ISSUE TRIAGE =================\ntriage:\n  labels: [bug]\n================================================\n",
	}
	// No mailbox, no intake: only the resume pass can harvest this.
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), triageSandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "triage-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationAgentDraft]).To(gomega.ContainSubstring("labels: [bug]"))
	g.Expect(updated.GetAnnotations()[AnnotationTriagedAt]).NotTo(gomega.BeEmpty())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}
