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
	PlanOpts    *factorycli.PlanOptions
	PRTaskOpts  *factorycli.PRTaskOptions
	PRTaskKind  string
	ExploreOpts *factorycli.ExploreOptions
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

func (f *fakeLauncher) StartPlan(key string, opts factorycli.PlanOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, PlanOpts: &opts})
	return true
}

func (f *fakeLauncher) StartInvestigate(key string, opts factorycli.PRTaskOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, PRTaskOpts: &opts, PRTaskKind: "investigate"})
	return true
}

func (f *fakeLauncher) StartAddressComments(key string, opts factorycli.PRTaskOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, PRTaskOpts: &opts, PRTaskKind: "address"})
	return true
}

func (f *fakeLauncher) StartIterate(key string, opts factorycli.PRTaskOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, PRTaskOpts: &opts, PRTaskKind: "iterate"})
	return true
}

func (f *fakeLauncher) StartExplore(key string, opts factorycli.ExploreOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, ExploreOpts: &opts})
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
		"https://api.github.com/repos/test/repo/issues?assignee=alice&per_page=100&state=open": jsonResp(issuesJSON),
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
	return &boardv1alpha1.RepoBoard{
		ObjectMeta: metav1.ObjectMeta{Name: "test-board", Namespace: "alice", Annotations: annotations},
		Spec: boardv1alpha1.RepoBoardSpec{
			RepoURL: "https://github.com/test/repo",
			// The fixture board runs the assigned-fix tier so discovery
			// paths are exercised; individual tests override.
			Auto:    boardv1alpha1.AutoSpec{Fix: "assigned"},
			Limits:  boardv1alpha1.LimitsSpec{MaxActive: 5},
			Sandbox: boardv1alpha1.SandboxSpec{DiskSize: "10Gi", IdleMinutes: 60},
		},
	}
}

func githubSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "alice"},
		Data:       map[string][]byte{"oauth_pat": []byte("gho_alice")},
	}
}

// suffixRoundTripper serves one body for any URL path with the suffix.
type suffixRoundTripper struct{ suffix, body string }

func (rt *suffixRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, rt.suffix) {
		resp := jsonResp(rt.body)()
		resp.Header = http.Header{"Content-Type": []string{"application/json"}}
		resp.Request = req
		return resp, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req, Header: http.Header{}}, nil
}

func newTestReconciler(fake *fakeLauncher, ghClient *github.Client, objs ...runtime.Object) *Reconciler {
	// Launch paths consult GitHub for parked pending reviews under the
	// executor token; default to "none" so tests exercise launches. Tests
	// that need review listings override after construction.
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{suffix: "/reviews", body: `[]`}})
	}
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

// auto.fix "assigned": issues assigned to the owner launch as them (with
// the draft-PR rail).
func TestLabelDiscovery_ExecutorConsent(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[
		{"number": 10, "title": "mine", "html_url": "https://github.com/test/repo/issues/10",
		 "assignees": [{"login": "alice"}]}
	]`)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/fix-repo-10"))
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
	g.Expect(fake2.launches()[0].Key).To(gomega.Equal("alice/fix-repo-10"))
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
	fake.results["alice/review-repo-42"] = factorycli.Result{Output: "Posting review as a draft (pending) review to GitHub PR...\n"}
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
	fake.results["alice/review-repo-42"] = factorycli.Result{
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
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-repo-42"))
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
	g.Expect(launches[0].Key).To(gomega.Equal("alice/fix-repo-77"))
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

// On a personal board a labeled issue assigned to the owner is direct
// consent — it executes as them without the auto-tier draft rail.
func TestAutoFixBoardConsent(t *testing.T) {
	g := gomega.NewWithT(t)

	falseVal := false
	mkBoard := func(enabled bool) *boardv1alpha1.RepoBoard {
		b := testBoard(nil)
		if enabled {
			b.Spec.Auto.Fix = "assigned"
		} else {
			b.Spec.Auto.Fix = "off"
		}
		b.Spec.Policy = boardv1alpha1.PolicySpec{DraftPR: &falseVal} // rails override policy
		return b
	}

	ghClient := testGithubClient(`[
		{"number": 20, "title": "assigned to alice", "html_url": "https://github.com/test/repo/issues/20",
		 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]}
	]`)

	// Boards are personal: intake.autoFix.enabled on the spec is the whole
	// consent — no side ConfigMap. Executes as the owner with the forced
	// draft-PR rail.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, mkBoard(true), githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("alice/fix-repo-20"))

	// Disabled, and no trigger label in play: assignment alone does not
	// consent an auto run. (A trigger-labeled owner-assigned issue would
	// still launch via the label tier — tiers are independent.)
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, testGithubClient(`[]`), mkBoard(false), githubSecret())
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
}

// Draft-review intake prepares a review for every inbound PR.
func TestDraftReviewIntake(t *testing.T) {
	g := gomega.NewWithT(t)

	board := testBoard(nil)
	board.Spec.Auto.Fix = "off"
	board.Spec.Auto.Review = "all"

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": jsonResp(`[
			{"number": 5, "title": "a", "labels": []},
			{"number": 6, "title": "b", "labels": [{"name": "no-agent"}]}
		]`),
	}}})

	board.Spec.Auto.ExcludeLabels = []string{"no-agent"}
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-repo-5"))
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
	board.Spec.Auto.Fix = "off"
	board.Spec.Auto.Triage = "unclaimed"

	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?labels=agent&per_page=100&state=open": jsonResp(`[]`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 30, "title": "untriaged", "html_url": "https://github.com/test/repo/issues/30", "labels": []},
			{"number": 31, "title": "claimed", "html_url": "https://github.com/test/repo/issues/31", "assignees": [{"login": "bob"}]}
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
	b.Spec.Auto.Fix = "off"
	b.Spec.Auto.Triage = "unclaimed"
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

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/review-repo-1163"))
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
	fake.results["alice/review-repo-42"] = factorycli.Result{
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
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client { return reviewsClient }
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

// A failed review invocation parks the review: the error lands on the
// sandbox and no relaunch happens until a member's re-review click is
// newer than the recorded failure. Auto-retrying re-runs the whole agent
// against failures (org OAuth restrictions, dead tokens) that only a
// human can clear.
func TestReviewErrorParksUntilRetryClick(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	prSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "factory-pr-42",
			"namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/pull/42"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/review-repo-42"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "checking out PR #42\nError: failed to create review on GitHub: 403 the org has enabled OAuth App access restrictions\n",
		Err:        fmt.Errorf("exit status 1"),
	}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), prSandbox)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-pr-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewError]).To(gomega.ContainSubstring("OAuth App access restrictions"))
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// Still parked on the next reconcile — no auto-retry.
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// A fresh Review click (re-review marker newer than the failure)
	// re-arms it.
	annotations := updated.GetAnnotations()
	annotations[AnnotationRereviewRequested] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	updated.SetAnnotations(annotations)
	g.Expect(r.Update(context.Background(), updated)).To(gomega.Succeed())

	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("alice/review-repo-42"))
}

func TestReviewErrorLine(t *testing.T) {
	g := gomega.NewWithT(t)
	g.Expect(reviewErrorLine(factorycli.Result{
		Output: "cloning...\nError: failed to create review on GitHub: 403 restricted\ntrailer\n",
		Err:    fmt.Errorf("exit status 1"),
	})).To(gomega.Equal("failed to create review on GitHub: 403 restricted"))
	g.Expect(reviewErrorLine(factorycli.Result{Output: "no error banner", Err: fmt.Errorf("exit status 1")})).To(gomega.Equal("exit status 1"))
	g.Expect(reviewErrorLine(factorycli.Result{})).To(gomega.Equal("review failed"))
}

// A pending review already parked on GitHub blocks a new launch for the
// same PR/executor: GitHub allows one pending review per author, so the
// duplicate run would burn tokens and 422 at the post step. This is also
// how a clean-slate cluster avoids re-reviewing saved work.
func TestPendingReviewOnGitHubBlocksLaunch(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"review-42": "alice"}`})
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{
			suffix: "/reviews",
			body:   `[{"id": 7, "state": "PENDING", "user": {"login": "alice"}}]`,
		}})
	}

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// Someone else's pending review is invisible anyway, but a submitted
	// one by the executor must not block.
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{
			suffix: "/reviews",
			body:   `[{"id": 7, "state": "COMMENTED", "user": {"login": "alice"}}]`,
		}})
	}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("alice/review-repo-42"))
}

// The plan loop, controller side: a Plan click launches `factory plan` in
// the member's fix sandbox; the finished run's banner output is stored as
// the draft; feedback newer than the draft re-launches with --feedback;
// approval makes the eventual fix run --with-plan.
func TestPlanLifecycle(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	// 1. Click: mailbox plan-42, no sandbox yet -> fresh plan launch.
	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"plan-42": "alice"}`})
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/plan-repo-42"))
	g.Expect(launches[0].PlanOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].PlanOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].PlanOpts.IssueURL).To(gomega.Equal("https://github.com/test/repo/issues/42"))
	g.Expect(launches[0].PlanOpts.Feedback).To(gomega.BeEmpty())

	// 2. Harvest: finished run + fix sandbox -> draft stored, mailbox kept
	// until stored, then trimmed.
	fixSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "fix-repo-42",
			"namespace": "alice",
			"labels":    map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/42",
				"sandbox.gemini.google.com/last-task-type":  "plan",
				"sandbox.gemini.google.com/last-task-state": "Completed",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	fake2 := newFakeLauncher()
	fake2.results["alice/plan-repo-42"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "banner\n================== ISSUE PLAN ==================\n## Summary\nDo the thing.\n================================================\ntrailer",
	}
	board2 := testBoard(map[string]string{AnnotationRequests: `{"plan-42": "alice"}`})
	r2 := newTestReconciler(fake2, ghClient, board2, githubSecret(), fixSandbox)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Name: "fix-repo-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationPlanDraft]).To(gomega.ContainSubstring("Do the thing."))
	g.Expect(updated.GetAnnotations()[AnnotationPlannedAt]).NotTo(gomega.BeEmpty())

	// Draft stored: the next reconcile trims the mailbox entry and does
	// not relaunch.
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
	fetched := &boardv1alpha1.RepoBoard{}
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Name: "test-board", Namespace: "alice"}, fetched)).To(gomega.Succeed())
	g.Expect(fetched.GetAnnotations()[AnnotationRequests]).NotTo(gomega.ContainSubstring("plan-42"))

	// 3. Refine: feedback newer than the draft relaunches with --feedback,
	// even with no mailbox entry (resume pass drives it).
	annotations := updated.GetAnnotations()
	annotations[AnnotationPlanFeedback] = "merge steps 2 and 3"
	annotations[AnnotationPlanFeedbackAt] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	updated.SetAnnotations(annotations)
	g.Expect(r2.Update(context.Background(), updated)).To(gomega.Succeed())
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches = fake2.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].PlanOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].PlanOpts.Feedback).To(gomega.Equal("merge steps 2 and 3"))
}

// An approved plan rides into the fix (--with-plan); a merely drafted or
// rejected plan does not.
func TestFixWithApprovedPlan(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	sbWithPlan := func(approved bool) *unstructured.Unstructured {
		annotations := map[string]interface{}{
			"htmlURL":           "https://github.com/test/repo/issues/7",
			AnnotationPlanDraft: "## Summary\nplanned",
			AnnotationPlannedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"review.gemini.google.com/refix-requested-at": time.Now().UTC().Format(time.RFC3339),
		}
		if approved {
			annotations[AnnotationPlanApproved] = time.Now().UTC().Format(time.RFC3339)
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":        "fix-repo-7",
				"namespace":   "alice",
				"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
				"annotations": annotations,
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	for _, approved := range []bool{true, false} {
		fake := newFakeLauncher()
		board := testBoard(map[string]string{AnnotationRequests: `{"fix-7": "alice"}`})
		r := newTestReconciler(fake, ghClient, board, githubSecret(), sbWithPlan(approved))
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1), "approved=%v", approved)
		g.Expect(launches[0].FixOpts).NotTo(gomega.BeNil())
		g.Expect(launches[0].FixOpts.WithPlan).To(gomega.Equal(approved), "approved=%v", approved)
	}
}

// The live #1529 regression, both halves. (1) Type-blind terminal: the
// plan's Completed stamp on the shared fix sandbox must not read as "the
// fix already ran" — that bailed every Approve & Fix after a plan. (2)
// resumeFixes: the mailbox claim is consumed at kickoff, so with no
// request standing, an approved-but-never-fixed sandbox must still
// launch (controller restarts between consumption and task start).
func TestApprovedPlanFixLaunchesAfterPlanCompleted(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	sb := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "fix-repo-7",
				"namespace": "alice",
				"labels":    map[string]interface{}{"factory.gemini.google.com/managed": "true"},
				"annotations": map[string]interface{}{
					"htmlURL":              "https://github.com/test/repo/issues/7",
					AnnotationExecutor:     "alice",
					AnnotationPlanDraft:    "## Summary\nplanned",
					AnnotationPlannedAt:    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
					AnnotationPlanApproved: time.Now().UTC().Format(time.RFC3339),
					// the PLAN's completion stamps — not the fix's
					factorycli.AnnotationTaskType:  "plan",
					factorycli.AnnotationTaskState: factorycli.TaskStateCompleted,
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	// (1) with the mailbox claim standing, (2) with it already consumed.
	for _, requests := range []map[string]string{{AnnotationRequests: `{"fix-7": "alice"}`}, {}} {
		fake := newFakeLauncher()
		r := newTestReconciler(fake, ghClient, testBoard(requests), githubSecret(), sb())
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1), "requests=%v", requests)
		g.Expect(launches[0].FixOpts).NotTo(gomega.BeNil(), "requests=%v", requests)
		g.Expect(launches[0].FixOpts.WithPlan).To(gomega.BeTrue())
	}
}

// A rejected plan's stale invocation result must not resurrect the draft.
func TestPlanResultStale(t *testing.T) {
	g := gomega.NewWithT(t)
	now := time.Now()
	g.Expect(planResultStale(map[string]string{
		AnnotationPlanRejected: now.UTC().Format(time.RFC3339),
	}, now.Add(-time.Minute))).To(gomega.BeTrue())
	g.Expect(planResultStale(map[string]string{}, now)).To(gomega.BeFalse())
	g.Expect(planResultStale(map[string]string{
		AnnotationPlanFeedbackAt: now.Add(-time.Hour).UTC().Format(time.RFC3339),
	}, now)).To(gomega.BeFalse())
}

// Automation defers to a review the executor already submitted (GitHub is
// the record — survives lost sandbox breadcrumbs); a member's click still
// deliberately reviews again.
func TestAutoReviewDefersToSubmitted(t *testing.T) {
	g := gomega.NewWithT(t)
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": jsonResp(`[
			{"number": 42, "title": "pr", "updated_at": "` + fresh + `",
			 "requested_reviewers": [{"login": "alice"}], "labels": []}
		]`),
	}}})
	submittedReviews := func() {
		newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
			return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{
				suffix: "/reviews",
				body:   `[{"id": 9, "state": "APPROVED", "user": {"login": "alice"}}]`,
			}})
		}
	}

	// Auto plan (review: requested): the submitted review is done-is-done.
	board := testBoard(nil)
	board.Spec.Auto.Fix = "off"
	board.Spec.Auto.Review = "requested"
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	submittedReviews()
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// A click (mailbox) deliberately reviews again despite it.
	board2 := testBoard(map[string]string{AnnotationRequests: `{"review-42": "alice"}`})
	board2.Spec.Auto.Fix = "off"
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, ghClient, board2, githubSecret())
	submittedReviews()
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.HaveLen(1))
	g.Expect(fake2.launches()[0].Key).To(gomega.Equal("alice/review-repo-42"))
}

// Relaunching into a paused sandbox stamps unpaused-at so pauseFinished
// cannot kill the pod mid-boot (the #1423 race, generalized to marker-less
// wakes like triage clicks).
func TestWakeStampsUnpaused(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	paused := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "triage-repo-30",
			"namespace": "alice",
			"labels":    map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/30",
				"sandbox.gemini.google.com/last-task-state":  "Completed",
				"sandbox.gemini.google.com/completion-time":  time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				"board.gemini.google.com/triage-rejected-at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		"spec": map[string]interface{}{"replicas": int64(0)},
	}}

	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"triage-30": "alice"}`})
	board.Spec.Auto.Fix = "off"
	r := newTestReconciler(fake, ghClient, board, githubSecret(), paused)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "triage-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationUnpausedAt]).NotTo(gomega.BeEmpty())
}

// The PR follow-up verbs (Iterate / Address / Investigate) are driven by
// sandbox annotations — durable consent, no mailbox claim to strand. A
// request newer than the last completion launches (with instruction and
// engine); a completion newer than the request means it was served.
func TestPRTaskClicks(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	sb := func(requestedAt time.Time) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "fix-repo-9",
				"namespace": "alice",
				"labels": map[string]interface{}{
					"factory.gemini.google.com/managed": "true",
					"factory.gemini.google.com/pr":      "42",
				},
				"annotations": map[string]interface{}{
					"htmlURL":                                   "https://github.com/test/repo/pull/42",
					AnnotationIterateRequested:                  requestedAt.UTC().Format(time.RFC3339),
					AnnotationIterateInstruction:                "tighten the error handling",
					factorycli.AnnotationTaskType:               "fix-issue",
					factorycli.AnnotationTaskState:              factorycli.TaskStateCompleted,
					"sandbox.gemini.google.com/completion-time": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	// Request newer than completion: launches with instruction.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb(time.Now()))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var prTasks []fakeLaunch
	for _, l := range fake.launches() {
		if l.PRTaskOpts != nil {
			prTasks = append(prTasks, l)
		}
	}
	g.Expect(prTasks).To(gomega.HaveLen(1))
	g.Expect(prTasks[0].PRTaskKind).To(gomega.Equal("iterate"))
	g.Expect(prTasks[0].PRTaskOpts.Instruction).To(gomega.Equal("tighten the error handling"))
	g.Expect(prTasks[0].PRTaskOpts.Engine).To(gomega.Equal("gemini"))
	g.Expect(prTasks[0].PRTaskOpts.PRURL).To(gomega.ContainSubstring("/pull/42"))

	// Request older than completion: served, no launch.
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, ghClient, testBoard(nil), githubSecret(), sb(time.Now().Add(-2*time.Hour)))
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake2.launches() {
		g.Expect(l.PRTaskOpts).To(gomega.BeNil())
	}
}

// The per-PR annotation overrides the board's autoIterate in either
// direction; absent inherits.
func TestAutoIterateOverride(t *testing.T) {
	sb := func(override string) *unstructured.Unstructured {
		annotations := map[string]interface{}{}
		if override != "" {
			annotations[AnnotationAutoIterate] = override
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": annotations},
		}}
	}
	cases := []struct {
		override     string
		boardDefault bool
		want         bool
	}{
		{"", true, true}, {"", false, false},
		{"off", true, false}, {"on", false, true},
		{"on", true, true}, {"off", false, false},
	}
	for _, tc := range cases {
		if got := autoIterateEnabled(sb(tc.override), tc.boardDefault); got != tc.want {
			t.Errorf("override=%q default=%v: got %v want %v", tc.override, tc.boardDefault, got, tc.want)
		}
	}
}

// One task per sandbox: a fix or plan child still provisioning the
// sandbox defers a follow-up click (the prober cannot see a task that
// has not landed in the pod yet).
func TestPRTaskClickDefersToRunningFix(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)
	fake := newFakeLauncher()
	fake.running["alice/fix-repo-9"] = true

	sb := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "fix-repo-9",
			"namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "42",
			},
			"annotations": map[string]interface{}{
				"htmlURL":                  "https://github.com/test/repo/pull/42",
				AnnotationIterateRequested: time.Now().UTC().Format(time.RFC3339),
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.PRTaskOpts).To(gomega.BeNil(), "follow-up must defer to the running fix")
	}
}

// Hand-made PR attach: a mailbox claim with no sandbox launches factory
// directly (it creates the factory-pr sandbox and checks out the
// branch); once a sandbox carries the PR label, the claim converts to
// the durable request annotation instead.
func TestPRTaskClaims(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	// No sandbox: direct launch with constructed PR URL and PRSandboxName.
	fake := newFakeLauncher()
	board := testBoard(map[string]string{
		AnnotationRequests: `{"iterate-77": "alice"}`,
		"board.gemini.google.com/iterate-instruction-77": "tighten the docs",
	})
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var prTasks []fakeLaunch
	for _, l := range fake.launches() {
		if l.PRTaskOpts != nil {
			prTasks = append(prTasks, l)
		}
	}
	g.Expect(prTasks).To(gomega.HaveLen(1))
	g.Expect(prTasks[0].PRTaskKind).To(gomega.Equal("iterate"))
	g.Expect(prTasks[0].PRTaskOpts.PRURL).To(gomega.Equal("https://github.com/test/repo/pull/77"))
	g.Expect(prTasks[0].PRTaskOpts.SandboxName).To(gomega.Equal("factory-pr-repo-77"))
	g.Expect(prTasks[0].PRTaskOpts.Instruction).To(gomega.Equal("tighten the docs"))

	// Sandbox exists (factory created it): the claim converts to the
	// annotation, no duplicate direct launch.
	prSB := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "factory-pr-repo-77",
			"namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "77",
			},
			"annotations": map[string]interface{}{
				"htmlURL":                      "https://github.com/test/repo/pull/77",
				factorycli.AnnotationTaskType:  "iterate",
				factorycli.AnnotationTaskState: "Running",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	fake2 := newFakeLauncher()
	board2 := testBoard(map[string]string{
		AnnotationRequests: `{"iterate-77": "alice"}`,
		"board.gemini.google.com/iterate-instruction-77": "tighten the docs",
	})
	r2 := newTestReconciler(fake2, ghClient, board2, githubSecret(), prSB)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake2.launches() {
		g.Expect(l.PRTaskOpts).To(gomega.BeNil(), "claim must convert, not double-launch")
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(sandboxGVK)
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Namespace: "alice", Name: "factory-pr-repo-77"}, got)).To(gomega.Succeed())
	g.Expect(got.GetAnnotations()[AnnotationIterateRequested]).NotTo(gomega.BeEmpty())
	g.Expect(got.GetAnnotations()[AnnotationIterateInstruction]).To(gomega.Equal("tighten the docs"))
	g.Expect(got.GetAnnotations()[AnnotationExecutor]).To(gomega.Equal("alice"))
}

// A factory-pr sandbox created by a follow-up verb (hand-made PR attach)
// must not read as an interrupted review: the live race launched a
// review into a mid-clone sandbox and both tasks died.
func TestResumeReviewsSkipsFollowUpOwnedSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)
	fake := newFakeLauncher()
	sb := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      "factory-pr-repo-88",
			"namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "88",
			},
			"annotations": map[string]interface{}{
				"htmlURL":                  "https://github.com/test/repo/pull/88",
				AnnotationIterateRequested: time.Now().UTC().Format(time.RFC3339),
				AnnotationExecutor:         "alice",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.ReviewOpts).To(gomega.BeNil(), "follow-up-owned sandbox must not resume a review")
	}
}

// Exploration claims v2: the timestamped mailbox claim is the durable
// request; the runner's per-kind result decides served-ness. No sandbox
// annotations, no conversion — factory ensures the sandbox itself.
func TestExploreClaims(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)

	// Standing claim with no result yet → launch (sandbox or not).
	fake := newFakeLauncher()
	board := testBoard(map[string]string{
		AnnotationRequests:     `{"explore-topic": "alice|` + claimAt + `"}`,
		AnnotationExploreTopic: "compare with gVisor",
	})
	r := newTestReconciler(fake, ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var explores []fakeLaunch
	for _, l := range fake.launches() {
		if l.ExploreOpts != nil {
			explores = append(explores, l)
		}
	}
	g.Expect(explores).To(gomega.HaveLen(1))
	g.Expect(explores[0].Key).To(gomega.Equal("alice/explore-repo-topic"))
	g.Expect(explores[0].ExploreOpts.Kind).To(gomega.Equal("topic"))
	g.Expect(explores[0].ExploreOpts.Topic).To(gomega.Equal("compare with gVisor"))
	g.Expect(explores[0].ExploreOpts.SandboxName).To(gomega.Equal("explore-repo"))
	g.Expect(explores[0].ExploreOpts.RepoURL).To(gomega.Equal("https://github.com/test/repo"))

	// A successful run newer than the click: served — no relaunch, and
	// the trim pass drops the claim (this is the anti-loop property).
	fake2 := newFakeLauncher()
	fake2.results["alice/explore-repo-topic"] = factorycli.Result{FinishedAt: time.Now()}
	board2 := testBoard(map[string]string{
		AnnotationRequests:     `{"explore-topic": "alice|` + claimAt + `"}`,
		AnnotationExploreTopic: "compare with gVisor",
	})
	r2 := newTestReconciler(fake2, ghClient, board2, githubSecret())
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake2.launches() {
		g.Expect(l.ExploreOpts).To(gomega.BeNil(), "served claim must not relaunch")
	}
	gotBoard := &boardv1alpha1.RepoBoard{}
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Namespace: "alice", Name: "test-board"}, gotBoard)).To(gomega.Succeed())
	g.Expect(gotBoard.GetAnnotations()[AnnotationRequests]).NotTo(gomega.ContainSubstring("explore-topic"), "served claim must be trimmed")

	// A result OLDER than the click is a re-click → launch again.
	fake3 := newFakeLauncher()
	fake3.results["alice/explore-repo-topic"] = factorycli.Result{FinishedAt: time.Now().Add(-time.Hour)}
	board3 := testBoard(map[string]string{
		AnnotationRequests:     `{"explore-topic": "alice|` + claimAt + `"}`,
		AnnotationExploreTopic: "compare with gVisor",
	})
	r3 := newTestReconciler(fake3, ghClient, board3, githubSecret())
	_, err = r3.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	found := false
	for _, l := range fake3.launches() {
		if l.ExploreOpts != nil {
			found = true
		}
	}
	g.Expect(found).To(gomega.BeTrue(), "re-click after an old run must relaunch")

	// Mid-run: the claim waits and survives the trim.
	fake4 := newFakeLauncher()
	fake4.running["alice/explore-repo-topic"] = true
	board4 := testBoard(map[string]string{
		AnnotationRequests:     `{"explore-topic": "alice|` + claimAt + `"}`,
		AnnotationExploreTopic: "compare with gVisor",
	})
	r4 := newTestReconciler(fake4, ghClient, board4, githubSecret())
	_, err = r4.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake4.launches() {
		g.Expect(l.ExploreOpts).To(gomega.BeNil())
	}
	gotBoard4 := &boardv1alpha1.RepoBoard{}
	g.Expect(r4.Get(context.Background(), types.NamespacedName{Namespace: "alice", Name: "test-board"}, gotBoard4)).To(gomega.Succeed())
	g.Expect(gotBoard4.GetAnnotations()[AnnotationRequests]).To(gomega.ContainSubstring("explore-topic"), "claim must survive while the runner is busy")

	// Legacy claim value without a timestamp still launches (zero time).
	fake5 := newFakeLauncher()
	board5 := testBoard(map[string]string{AnnotationRequests: `{"explore-onboard": "alice"}`})
	r5 := newTestReconciler(fake5, ghClient, board5, githubSecret())
	_, err = r5.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	legacy := false
	for _, l := range fake5.launches() {
		if l.ExploreOpts != nil && l.ExploreOpts.Kind == "onboard" {
			legacy = true
		}
	}
	g.Expect(legacy).To(gomega.BeTrue())
}
