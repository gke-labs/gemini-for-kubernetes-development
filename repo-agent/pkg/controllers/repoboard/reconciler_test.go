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
			Triggers: boardv1alpha1.TriggersSpec{Label: "agent", Discreet: &discreet},
			Limits:   boardv1alpha1.LimitsSpec{MaxActive: 5},
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
func TestAutoFixTwoKeyConsent(t *testing.T) {
	g := gomega.NewWithT(t)

	falseVal := false
	mkBoard := func() *boardv1alpha1.RepoBoard {
		b := testBoard(nil)
		b.Spec.Intake.AutoFix = boardv1alpha1.AutoFixSpec{Enabled: true, Require: []string{"assigned", "label"}}
		b.Spec.Policy = boardv1alpha1.PolicySpec{DraftPR: &falseVal} // rails override policy
		return b
	}
	optIn := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-preferences", Namespace: "alice"},
		Data:       map[string]string{AutoFixPreferenceKey("alice", "test-board"): "true"},
	}

	ghClient := testGithubClient(`[
		{"number": 20, "title": "assigned to alice", "html_url": "https://github.com/test/repo/issues/20",
		 "labels": [{"name": "agent"}], "assignees": [{"login": "alice"}]}
	]`)

	// With the opt-in: executes as the owner with the forced draft-PR rail.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, mkBoard(), githubSecret(), optIn)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("alice/fix-repo-20"))
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

// A finished sandbox idling toward its pause holds no launch slot: with
// maxActive 1 and a completed review sandbox still running, a fresh click
// launches; a genuinely Running sandbox still blocks.
func TestSettledSandboxFreesLaunchSlot(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	occupant := func(state string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "factory-pr-repo-90",
				"namespace": "alice",
				"labels": map[string]interface{}{
					"factory.gemini.google.com/managed": "true",
					"factory.gemini.google.com/pr":      "90",
				},
				"annotations": map[string]interface{}{
					"htmlURL": "https://github.com/test/repo/pull/90",
					"sandbox.gemini.google.com/last-task-state": state,
					"sandbox.gemini.google.com/completion-time": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
					"reviewState": "pending",
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	for _, tc := range []struct {
		state      string
		wantLaunch bool
	}{
		{"Completed", true},
		{"Running", false},
	} {
		fake := newFakeLauncher()
		board := testBoard(map[string]string{AnnotationRequests: `{"review-42": "alice"}`})
		board.Spec.Limits.MaxActive = 1
		r := newTestReconciler(fake, ghClient, board, githubSecret(), occupant(tc.state))
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if tc.wantLaunch {
			g.Expect(fake.launches()).To(gomega.HaveLen(1), "state=%s", tc.state)
		} else {
			g.Expect(fake.launches()).To(gomega.BeEmpty(), "state=%s", tc.state)
		}
	}
}

func TestSandboxSettled(t *testing.T) {
	g := gomega.NewWithT(t)
	base := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	newer := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)

	g.Expect(sandboxSettled(map[string]string{
		"sandbox.gemini.google.com/last-task-state": "Completed",
		"sandbox.gemini.google.com/completion-time": base,
	})).To(gomega.BeTrue())
	g.Expect(sandboxSettled(map[string]string{
		"sandbox.gemini.google.com/last-task-state": "Running",
	})).To(gomega.BeFalse())
	// A rerun marker newer than completion means it is waking: active.
	g.Expect(sandboxSettled(map[string]string{
		"sandbox.gemini.google.com/last-task-state": "Failed",
		"sandbox.gemini.google.com/completion-time": base,
		AnnotationRereviewRequested:                 newer,
	})).To(gomega.BeFalse())
	// No completion stamp at all: provisioning, counts as active.
	g.Expect(sandboxSettled(map[string]string{
		"sandbox.gemini.google.com/last-task-state": "Completed",
	})).To(gomega.BeFalse())
}

// maxActive caps pods: when the budget is full, the oldest settled
// sandbox yields its keep-warm window immediately; working sandboxes are
// never evicted.
func TestReclaimPodSlots(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	pod := func(name, state string, completedAgo time.Duration) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "alice",
				"labels":    map[string]interface{}{"factory.gemini.google.com/managed": "true"},
				"annotations": map[string]interface{}{
					"htmlURL": "https://github.com/test/repo/pull/1",
					"sandbox.gemini.google.com/last-task-state": state,
					"sandbox.gemini.google.com/last-task-type":  "review",
					"sandbox.gemini.google.com/completion-time": time.Now().Add(-completedAgo).UTC().Format(time.RFC3339),
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	fake := newFakeLauncher()
	board := testBoard(nil)
	board.Spec.Limits.MaxActive = 2
	// 3 running pods over a budget of 2: two settled (old + newer), one
	// genuinely working. Only the OLDEST settled one yields.
	r := newTestReconciler(fake, ghClient, board, githubSecret(),
		pod("factory-pr-repo-1", "Completed", 4*time.Minute),
		pod("factory-pr-repo-2", "Completed", 1*time.Minute),
		pod("factory-pr-repo-3", "Running", time.Minute))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	replicasOf := func(name string) int64 {
		sb := &unstructured.Unstructured{}
		sb.SetGroupVersionKind(sandboxGVK)
		g.Expect(r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "alice"}, sb)).To(gomega.Succeed())
		replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
		return replicas
	}
	g.Expect(replicasOf("factory-pr-repo-1")).To(gomega.Equal(int64(0)), "oldest settled yields")
	g.Expect(replicasOf("factory-pr-repo-2")).To(gomega.Equal(int64(1)), "newer settled keeps its window")
	g.Expect(replicasOf("factory-pr-repo-3")).To(gomega.Equal(int64(1)), "working pod never evicted")
}
