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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	Key string
	// One of the recipe's runs, by its name.
	FixOpts      *factorycli.RecipeOptions
	ReviewOpts   *factorycli.RecipeOptions
	TriageOpts   *factorycli.RecipeOptions
	PlanOpts     *factorycli.RecipeOptions
	ResearchOpts *factorycli.RecipeOptions
	// Any other recipe's.
	RecipeOpts  *factorycli.RecipeOptions
	PRWatchOpts *factorycli.PRWatchOptions
	RunOpts     *factorycli.RunOptions
	ApplyOpts   *factorycli.ApplyOptions
}

type fakeLauncher struct {
	mu      sync.Mutex
	calls   []fakeLaunch
	running map[string]bool
	results map[string]factorycli.Result
	// onStartRun runs inside StartRun, before it returns: the only place
	// a test can see the world as it was at the moment money was spent.
	onStartRun func(key string)
	// refuseRun makes StartRun return false without recording anything,
	// which is what the real one does when the sandbox is busy with a
	// task this process did not start. Nothing is spent, so nothing about
	// the click has changed.
	refuseRun bool
	// stopped are the keys Stop was called for.
	stopped []string
	// catalog, when set, is what Recipes lists instead of the built-ins.
	catalog []boardv1alpha1.BoardRecipe
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{running: map[string]bool{}, results: map[string]factorycli.Result{}}
}

// StartRecipe records the run under its recipe's field. A triage or a
// plan refuses a busy key, as the real runner does; the rest record
// unconditionally, like StartRun: the single flight under test is the
// caller's IsRunning check, and a fake that refused a busy key would pass
// whether or not the caller made it.
func (f *fakeLauncher) StartRecipe(key string, opts factorycli.RecipeOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	launch := fakeLaunch{Key: key}
	switch opts.Recipe {
	case "fix":
		launch.FixOpts = &opts
	case "review":
		launch.ReviewOpts = &opts
	case "triage":
		launch.TriageOpts = &opts
	case "plan":
		launch.PlanOpts = &opts
	case "research":
		launch.ResearchOpts = &opts
	default:
		launch.RecipeOpts = &opts
	}
	if (opts.Recipe == "triage" || opts.Recipe == "plan") && f.running[key] {
		return false
	}
	f.calls = append(f.calls, launch)
	return true
}

func (f *fakeLauncher) StartPRWatch(key string, opts factorycli.PRWatchOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLaunch{Key: key, PRWatchOpts: &opts})
	return true
}

func (f *fakeLauncher) StartRun(key string, opts factorycli.RunOptions) bool {
	if f.onStartRun != nil {
		f.onStartRun(key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuseRun {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, RunOpts: &opts})
	// The real runner holds the key for the length of the run, which is
	// what tells the reap pass the click is in flight rather than lost.
	f.running[key] = true
	return true
}

func (f *fakeLauncher) StartApply(key string, opts factorycli.ApplyOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, ApplyOpts: &opts})
	return true
}

// Recipes is the built-ins as factory lists them, in the board's order,
// with what they apply when a run ends and their timeouts, and summarize: a recipe the board has no pass of its own for, on issues
// and PRs, writing a Summary.
func (f *fakeLauncher) Recipes(context.Context) ([]boardv1alpha1.BoardRecipe, error) {
	if f.catalog != nil {
		return f.catalog, nil
	}
	return []boardv1alpha1.BoardRecipe{
		{Name: "triage", Label: "Triage", On: []string{"issue"}, Kind: "Triage", TaskType: "recipe-triage"},
		{Name: "plan", Label: "Plan", On: []string{"issue"}, Kind: "Plan", TaskType: "plan"},
		{Name: "fix", Label: "Fix", On: []string{"issue"}, Kind: "Change", TaskType: "fix", OnFinish: "open-pr", Timeout: "3h"},
		{Name: "care", Label: "Care", On: []string{"my-pr"}, Kind: "Change", TaskType: "recipe-care", Session: "care", OnFinish: "post-replies"},
		{Name: "review", Label: "Review", On: []string{"pr"}, Kind: "Review", TaskType: "recipe-review", Credentials: "clone", OnFinish: "post-review", Timeout: "45m"},
		{Name: "research", Label: "Research", On: []string{"repo"}, Kind: "Notes", TaskType: "research"},
		{Name: "summarize", Label: "Summarize", On: []string{"issue", "pr"}, Kind: "Summary", TaskType: "recipe-summarize"},
	}, nil
}

func (f *fakeLauncher) IsRunning(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[key]
}

func (f *fakeLauncher) Running(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key, running := range f.running {
		if running && strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys
}

func (f *fakeLauncher) Stop(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		delete(f.running, key)
		f.stopped = append(f.stopped, key)
	}
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

// nonName is everything a metadata.name may not hold. Test subjects
// include deliberately malformed ones (a session id with a space), and
// the fake client validates names like the apiserver does.
var nonName = regexp.MustCompile(`[^a-z0-9.-]+`)

// testRequest files a standing click on the fixture board the way the
// API does: in the board's namespace, labelled with the board and the
// verb, named after its subject.
func testRequest(spec boardv1alpha1.RequestSpec) *boardv1alpha1.Request {
	spec.Board = "test-board"
	if spec.Member == "" {
		spec.Member = "alice"
	}
	name := nonName.ReplaceAllString(strings.ToLower(spec.Key()), "-")
	return &boardv1alpha1.Request{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.Trim(name, "-."),
			Namespace: "alice",
			// Just clicked, unless a test ages it: the creation stamp is
			// the click time, so a zero one would read as expired.
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				boardv1alpha1.LabelBoard: "test-board",
				boardv1alpha1.LabelVerb:  spec.Verb,
			},
		},
		Spec: spec,
	}
}

// click is the common case: a verb on an issue or PR number, from alice.
// launch is a member's click on a recipe's launch button: a PR's for a
// review, an issue's for the rest.
func launch(recipe string, number int) *boardv1alpha1.Request {
	item := "issue"
	if recipe == "review" {
		item = "pr"
	}
	return testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: recipe, Item: item, Number: number})
}

// requestStatus reads back what the reap pass decided. A Request that
// was collected rather than settled reads as the zero status, which is
// what a caller asserting "gone" wants to see.
func requestStatus(t *testing.T, r *Reconciler, req *boardv1alpha1.Request) boardv1alpha1.RequestStatus {
	t.Helper()
	fetched := &boardv1alpha1.Request{}
	err := r.Get(context.Background(), types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, fetched)
	if apierrors.IsNotFound(err) {
		return boardv1alpha1.RequestStatus{}
	}
	if err != nil {
		t.Fatalf("get request %s: %v", req.Name, err)
	}
	return fetched.Status
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
	builder := clientfake.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&boardv1alpha1.RepoBoard{}, &boardv1alpha1.Request{})
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

// The board's Disclose is factory's --disclose, passed either way:
// factory defaults it on for its other callers, so a board that leaves
// it off has to say so. It is no longer a line of instruction appended
// by the controller — factory's own attribution is what it gates.
func TestFixCarriesTheBoardsDisclose(t *testing.T) {
	for _, disclose := range []bool{false, true} {
		g := gomega.NewWithT(t)
		ghClient := testGithubClient(`[
			{"number": 10, "title": "mine", "html_url": "https://github.com/test/repo/issues/10",
			 "assignees": [{"login": "alice"}]}
		]`)
		board := testBoard(nil)
		board.Spec.Policy.Disclose = disclose
		fake := newFakeLauncher()
		r := newTestReconciler(fake, ghClient, board, githubSecret())

		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1))
		g.Expect(launches[0].FixOpts.Disclose).To(gomega.Equal(disclose))
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
	g.Expect(launches[0].FixOpts.URL).To(gomega.Equal("https://github.com/test/repo/issues/10"))
	g.Expect(launches[0].FixOpts.RunName).To(gomega.HavePrefix("fix/test-board/10/"))
	g.Expect(launches[0].FixOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].FixOpts.GithubToken).To(gomega.Equal("gho_alice"))

	// factory-user secret materialized for the executor namespace.
	secret := &corev1.Secret{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "factory-user", Namespace: "alice"}, secret)).To(gomega.Succeed())
	g.Expect(secret.Data["GITHUB_LOGIN"]).To(gomega.Equal([]byte("alice")))
	g.Expect(secret.Data["GITHUB_TOKEN"]).To(gomega.Equal([]byte("gho_alice")))
}

// A completed fix is not relaunched.
func TestFixTerminal(t *testing.T) {
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
}

// reviewSandboxFixture is the member's review sandbox of PR 42, as `factory
// recipe review` makes it: by name, with no PR label.
func reviewSandboxFixture(annotations map[string]interface{}) *unstructured.Unstructured {
	a := map[string]interface{}{"repo": "repo", "htmlURL": "https://github.com/test/repo/pull/42"}
	for k, v := range annotations {
		a[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "review-repo-42", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"sandbox.gemini.google.com/type":    "recipe",
			},
			"annotations": a,
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

// A finished auto-review posted the pending review on GitHub (the runner
// applies post-review as its harvest): the resume pass reads the result
// and the sandbox records it.
func TestResumeReviewMarksPending(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	fake := newFakeLauncher()
	fake.results["alice/review-repo-42"] = factorycli.Result{FinishedAt: time.Now(), Output: "================== TASK OUTPUT =================\napiVersion: factory.gemini.google.com/v1alpha1\nkind: Review\nspec:\n  body: b\n================================================\nposted\n"}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), reviewSandboxFixture(nil))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "review-repo-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewedAt]).NotTo(gomega.BeEmpty())
	g.Expect(factorycli.IsApplied(updated.GetAnnotations(), factorycli.AppliedAnnotation(factorycli.AnnotationReviewRun), "post-review")).To(gomega.BeTrue())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A controller that restarted mid-review has no result in memory: it
// invokes the recipe again with the auto-review run recorded on the
// sandbox, which follows that run instead of starting another. A member's
// run, recorded under the same annotation, is its Request's: automation
// neither follows it nor reviews again.
func TestResumeReviewByRecordedRun(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	recorded := func(name string) map[string]interface{} {
		return map[string]interface{}{
			factorycli.AnnotationReviewRun: `{"name":"` + name + `","task":"recipe-review-1","startedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`,
		}
	}
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), reviewSandboxFixture(recorded("review/test-board/42/1700000000")))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ReviewOpts.RunName).To(gomega.Equal("review/test-board/42/1700000000"))

	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, ghClient, testBoard(nil), githubSecret(), reviewSandboxFixture(recorded("request/uid-42")))
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
}

// A review click is a recipe Request like any other: it runs the review
// recipe in the clicker's namespace under their identity (the pending
// review must be authored by them to be visible to them), in their review
// sandbox, under its Request's run name, posting the pending review when
// the run ends as the catalog says.
func TestRequestReviewLaunchAsExecutor(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	fake := newFakeLauncher()
	req := launch("review", 42)
	req.UID = "uid-42"
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-review-repo-pr-42"))
	g.Expect(launches[0].ReviewOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].ReviewOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].ReviewOpts.SandboxName).To(gomega.Equal("review-repo-42"))
	g.Expect(launches[0].ReviewOpts.URL).To(gomega.Equal("https://github.com/test/repo/pull/42"))
	g.Expect(launches[0].ReviewOpts.RunName).To(gomega.Equal("request/uid-42"))
	g.Expect(launches[0].ReviewOpts.GithubToken).To(gomega.Equal("gho_alice"))
	g.Expect(launches[0].ReviewOpts.Apply).To(gomega.Equal("post-review"))
	g.Expect(launches[0].ReviewOpts.Timeout).To(gomega.Equal(45 * time.Minute))
}

// A Fix click is a recipe Request like any other: it launches the fix in
// the issue's sandbox, opening the draft PR when the run ends as the
// catalog says, and stands until its output is stored.
func TestRequestFix(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)
	fake := newFakeLauncher()
	req := launch("fix", 77)
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-fix-repo-issue-77"))
	g.Expect(launches[0].FixOpts.URL).To(gomega.Equal("https://github.com/test/repo/issues/77"))
	g.Expect(launches[0].FixOpts.SandboxName).To(gomega.Equal("fix-repo-77"))
	g.Expect(launches[0].FixOpts.Apply).To(gomega.Equal("open-pr"))
	g.Expect(launches[0].FixOpts.Timeout).To(gomega.Equal(3 * time.Hour))

	// No output yet: the click stays standing for the next reconcile.
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.BeEmpty())
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
	// Personal board: the member is the executor — attributed.
	g.Expect(launches[0].ReviewOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].ReviewOpts.SandboxName).To(gomega.Equal("review-repo-5"))
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
	g.Expect(launches[0].TriageOpts.URL).To(gomega.Equal("https://github.com/test/repo/issues/30"))

	// Phase 2: harvest after completion.
	triageSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-30", "namespace": "alice",
			"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{"htmlURL": "https://github.com/test/repo/issues/30"},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	fake2 := newFakeLauncher()
	fake2.results["alice/triage-repo-30"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     triageResult("  labels: [bug]\n  priority: high\n  assessment: broken\n"),
	}
	r2 := newTestReconciler(fake2, ghClient, testBoardWithTriage(), githubSecret(), triageSandbox)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r2.Get(context.Background(), types.NamespacedName{Name: "fix-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[factorycli.AnnotationTriageOutput]).To(gomega.ContainSubstring("priority: high"))
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
func TestReviewLaunchesWithoutLimitsBlock(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	board := testBoard(nil)
	board.Spec.Limits = boardv1alpha1.LimitsSpec{} // UI-created boards omit limits entirely

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, board, githubSecret(), launch("review", 1163))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-review-repo-pr-1163"))
}

// An auto-review that failed before its sandbox existed backs off rather
// than hot-looping the agent.
func TestReviewFailureWithoutSandboxBacksOff(t *testing.T) {
	g := gomega.NewWithT(t)

	fake := newFakeLauncher()
	fake.results["alice/review-repo-42"] = factorycli.Result{FinishedAt: time.Now(), Err: fmt.Errorf("exit status 1")}
	r := newTestReconciler(fake, autoReviewClient(42), autoReviewBoard(), githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// An auto-triage the controller lost sight of (a restart) is harvested by
// the resume pass when it finishes.
func TestResumeTriageHarvest(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	triageSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-30", "namespace": "alice",
			"labels": map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/30",
				"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/triage-repo-30"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     triageResult("  labels: [bug]\n"),
	}
	// No click, no intake: only the resume pass can harvest this.
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), triageSandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[factorycli.AnnotationTriageOutput]).To(gomega.ContainSubstring("labels: [bug]"))
	g.Expect(updated.GetAnnotations()[AnnotationTriagedAt]).NotTo(gomega.BeEmpty())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A triage sandbox the factory CLI created is not the board's: it is left
// unharvested and unrelaunched, however much it looks like an unfinished
// board triage.
func TestResumeTriageSkipsOtherLaunchers(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	cliSandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-31", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed":  "true",
				"factory.gemini.google.com/launcher": "factory",
			},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/31",
				"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	fake.results["alice/triage-repo-31"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     triageResult("  labels: [bug]\n"),
	}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), cliSandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-31", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationTriagedAt]).To(gomega.BeEmpty())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A failed auto-review parks the review: the error lands on the sandbox
// and automation does not relaunch it. Auto-retrying re-runs the whole
// agent against failures (org OAuth restrictions, dead tokens) that only
// a human can clear; a member clicks Review, a recipe Request.
func TestReviewErrorParks(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	prSandbox := reviewSandboxFixture(nil)

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
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "review-repo-42", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationReviewError]).To(gomega.ContainSubstring("OAuth App access restrictions"))
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// Still parked on the next reconcile — no auto-retry.
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
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

// A pending review already parked on GitHub blocks an auto-review of the
// same PR/executor: GitHub allows one pending review per author, so the
// duplicate run would burn tokens and 422 at the post step. This is also
// how a clean-slate cluster avoids re-reviewing saved work.
func TestPendingReviewOnGitHubBlocksLaunch(t *testing.T) {
	g := gomega.NewWithT(t)

	fake := newFakeLauncher()
	r := newTestReconciler(fake, autoReviewClient(42), autoReviewBoard(), githubSecret())
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{
			suffix: "/reviews",
			body:   `[{"id": 7, "state": "PENDING", "user": {"login": "alice"}}]`,
		}})
	}

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// A pending one factory posted does not block (post-review replaces
	// it).
	newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
		return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{
			suffix: "/reviews",
			body:   `[{"id": 7, "state": "PENDING", "user": {"login": "alice"}, "body": "x\n<!-- factory:task-output kind=Review task=t -->"}]`,
		}})
	}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(fake.launches()[0].Key).To(gomega.Equal("alice/review-repo-42"))
}

// autoReviewBoard reviews every open PR (auto.review all), and fixes
// nothing.
func autoReviewBoard() *boardv1alpha1.RepoBoard {
	b := testBoard(nil)
	b.Spec.Auto.Fix = "off"
	b.Spec.Auto.Review = "all"
	return b
}

// autoReviewClient lists one open PR, n, for auto-review to find.
func autoReviewClient(n int) *github.Client {
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user":                                          jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/pulls?per_page=100&state=open": jsonResp(fmt.Sprintf(`[{"number": %d, "title": "pr", "updated_at": %q, "labels": []}]`, n, fresh)),
	}}})
}

// A Plan click is a recipe Request like any other: it launches `factory
// recipe plan` in the member's issue sandbox under its Request's run name;
// the finished run's task output is stored as the run's draft, and the
// click settles.
func TestPlanClickIsARecipe(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	fake := newFakeLauncher()
	req := launch("plan", 42)
	req.UID = "uid-42"
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-plan-repo-issue-42"))
	g.Expect(launches[0].PlanOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].PlanOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].PlanOpts.SandboxName).To(gomega.Equal("fix-repo-42"))
	g.Expect(launches[0].PlanOpts.URL).To(gomega.Equal("https://github.com/test/repo/issues/42"))
	g.Expect(launches[0].PlanOpts.RunName).To(gomega.Equal("request/uid-42"))
	g.Expect(launches[0].PlanOpts.Apply).To(gomega.BeEmpty())

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
	fake2.results["alice/recipe-plan-repo-issue-42"] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Minute),
		Output:     "banner\n================== TASK OUTPUT =================\napiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\nactions:\n  - verb: comment\nspec:\n  markdown: |-\n    ## Summary\n    Do the thing.\n================================================\n",
	}
	r2 := newTestReconciler(fake2, ghClient, testBoard(nil), githubSecret(), fixSandbox, req)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
	updated := getSandbox(t, r2, "fix-repo-42")
	g.Expect(factorycli.PlanDraft(updated.GetAnnotations())).To(gomega.ContainSubstring("Do the thing."))

	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r2, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
}

// The plan a fix follows is the click's: a plan's run: fix files the fix
// with the plan as its input. A plan on the sandbox, applied or not, does
// not ride into a plain Fix click.
func TestFixTakesTheClicksPlan(t *testing.T) {
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
					"htmlURL":                        "https://github.com/test/repo/issues/7",
					factorycli.AnnotationPlanOutput:  storedOutput("Plan", "## Summary\nplanned"),
					factorycli.AnnotationPlanApplied: `{"run":"` + time.Now().UTC().Format(time.RFC3339) + `"}`,
					// The plan's completion stamps, not a fix's: the fix
					// has not run.
					factorycli.AnnotationTaskType:  "plan",
					factorycli.AnnotationTaskState: factorycli.TaskStateCompleted,
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		}}
	}

	for _, plan := range []string{"## Summary\nplanned", ""} {
		click := launch("fix", 7)
		if plan != "" {
			click.Spec.Inputs = map[string]string{"plan": plan}
		}
		fake := newFakeLauncher()
		r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb(), click)
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1), "plan=%q", plan)
		g.Expect(launches[0].FixOpts).NotTo(gomega.BeNil())
		g.Expect(launches[0].FixOpts.Inputs["plan"]).To(gomega.Equal(plan))
		g.Expect(launches[0].FixOpts.Inputs).NotTo(gomega.HaveKey("with_plan"))
	}

	// With no click standing, an applied plan launches nothing: the click
	// stands until a fix runs, so a restart does not lose it.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.FixOpts).To(gomega.BeNil())
	}
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

	// A click deliberately reviews again despite it.
	board2 := testBoard(nil)
	board2.Spec.Auto.Fix = "off"
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, ghClient, board2, githubSecret(), launch("review", 42))
	submittedReviews()
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.HaveLen(1))
	g.Expect(fake2.launches()[0].Key).To(gomega.Equal("alice/recipe-review-repo-pr-42"))
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
			"name":      "fix-repo-30",
			"namespace": "alice",
			"labels":    map[string]interface{}{"factory.gemini.google.com/managed": "true", factorycli.LabelIssue: "30"},
			"annotations": map[string]interface{}{
				"repo":    "repo",
				"htmlURL": "https://github.com/test/repo/issues/30",
				factorycli.AnnotationRecipeTriageTaskState:          "Completed",
				"sandbox.gemini.google.com/completion-time":         time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				"board.gemini.google.com/recipe-triage-rejected-at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		"spec": map[string]interface{}{"replicas": int64(0)},
	}}

	fake := newFakeLauncher()
	board := testBoard(nil)
	board.Spec.Auto.Fix = "off"
	r := newTestReconciler(fake, ghClient, board, githubSecret(), paused, launch("triage", 30))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-30", Namespace: "alice"}, updated)).To(gomega.Succeed())
	g.Expect(updated.GetAnnotations()[AnnotationUnpausedAt]).NotTo(gomega.BeEmpty())
}

// The board's reviews live in review sandboxes: a factory-pr sandbox (a
// follow-up verb's, or the watch's) never resumes as a review.
func TestResumeReviewsSkipsPRSandboxes(t *testing.T) {
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
				"htmlURL":                     "https://github.com/test/repo/pull/88",
				factorycli.AnnotationTaskType: "review",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sb)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.ReviewOpts).To(gomega.BeNil(), "a factory-pr sandbox must not resume a review")
	}
}

// A controller that restarted mid-triage has no result in memory: it
// invokes factory again with the run recorded on the sandbox, which
// follows that run instead of starting another.
func TestResumeTriageByRecordedRun(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[]`)

	run := `{"name":"auto/test-board/32/1700000000","task":"recipe-triage-20261004-1","startedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	sandbox := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-32", "namespace": "alice",
			"labels": map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/32",
				"sandbox.gemini.google.com/recipe-triage-task-state": "Running",
				factorycli.AnnotationTriageRun:                       run,
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}

	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret(), sandbox)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].TriageOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].TriageOpts.RunName).To(gomega.Equal("auto/test-board/32/1700000000"))
}

// The recorded run is taken only while it is the one the board waits on:
// not once this controller has a result for the key (a failed resume
// retries fresh), nor when it started before a marker.
func TestResumableRun(t *testing.T) {
	start := time.Date(2026, 10, 4, 12, 0, 0, 500, time.UTC)
	recorded := `{"name":"auto/b/5/1","task":"recipe-triage-1","startedAt":"` + start.Format(time.RFC3339Nano) + `"}`
	for name, tc := range map[string]struct {
		annotations map[string]string
		result      bool
		want        string
	}{
		"recorded":          {map[string]string{factorycli.AnnotationTriageRun: recorded}, false, "auto/b/5/1"},
		"none recorded":     {map[string]string{}, false, "fresh"},
		"result in memory":  {map[string]string{factorycli.AnnotationTriageRun: recorded}, true, "fresh"},
		"after the reject":  {map[string]string{factorycli.AnnotationTriageRun: recorded, AnnotationTriageRejected: start.Add(-time.Minute).Format(time.RFC3339)}, false, "auto/b/5/1"},
		"before the reject": {map[string]string{factorycli.AnnotationTriageRun: recorded, AnnotationTriageRejected: start.Add(time.Minute).Format(time.RFC3339)}, false, "fresh"},
		"same second":       {map[string]string{factorycli.AnnotationTriageRun: recorded, AnnotationTriagedAt: start.Format(time.RFC3339)}, false, "fresh"},
		"unreadable":        {map[string]string{factorycli.AnnotationTriageRun: "{"}, false, "fresh"},
	} {
		fake := newFakeLauncher()
		if tc.result {
			fake.results["alice/triage-repo-5"] = factorycli.Result{FinishedAt: time.Now()}
		}
		r := &Reconciler{Factory: fake}
		got := r.resumableRun("alice/triage-repo-5", tc.annotations, factorycli.AnnotationTriageRun, "fresh",
			AnnotationTriagedAt, AnnotationTriageRejected)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// Automation leaves an item a member ran the recipe on: their run is
// recorded under a name automation does not give (their Request's), so
// auto-triage does not triage the issue again, nor auto-review review the
// PR again.
func TestAutomationLeavesAMembersRun(t *testing.T) {
	g := gomega.NewWithT(t)
	memberRun := func(task string) string {
		return `{"name":"request/uid-1","task":"` + task + `","startedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	}

	triaged := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-30", "namespace": "alice",
			"labels": map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"htmlURL": "https://github.com/test/repo/issues/30",
				"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
				factorycli.AnnotationTriageRun:                       memberRun("recipe-triage-1"),
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 30, "title": "triaged by a member", "html_url": "https://github.com/test/repo/issues/30", "labels": []}
		]`),
	}}})
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoardWithTriage(), githubSecret(), triaged)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	reviewed := reviewSandboxFixture(map[string]interface{}{factorycli.AnnotationReviewRun: memberRun("recipe-review-1")})
	fake2 := newFakeLauncher()
	r2 := newTestReconciler(fake2, autoReviewClient(42), autoReviewBoard(), githubSecret(), reviewed)
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake2.launches()).To(gomega.BeEmpty())
}
