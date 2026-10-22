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
	"slices"
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
	// catalog, when set, is what Recipes lists instead of the built-ins;
	// repoRecipes are added to it when a repository is asked for, and
	// listed records each repository asked for ("" for the built-ins).
	catalog     []boardv1alpha1.BoardRecipe
	repoRecipes []boardv1alpha1.BoardRecipe
	listed      []string
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
func (f *fakeLauncher) Recipes(_ context.Context, repoURL, _ string) ([]boardv1alpha1.BoardRecipe, []string, error) {
	f.mu.Lock()
	f.listed = append(f.listed, repoURL)
	f.mu.Unlock()
	var repo []boardv1alpha1.BoardRecipe
	if repoURL != "" {
		repo = f.repoRecipes
	}
	if f.catalog != nil {
		return append(slices.Clone(f.catalog), repo...), nil, nil
	}
	return append([]boardv1alpha1.BoardRecipe{
		{Name: "triage", Label: "Triage", On: []string{"issue"}, Kind: "Triage", TaskType: "recipe-triage"},
		{Name: "plan", Label: "Plan", On: []string{"issue"}, Kind: "Plan", TaskType: "plan"},
		{Name: "fix", Label: "Fix", On: []string{"issue"}, Kind: "Change", TaskType: "fix", OnFinish: "open-pr", Timeout: "3h"},
		{Name: "care", Label: "Care", On: []string{"my-pr"}, Kind: "Change", TaskType: "recipe-care", Session: "care", OnFinish: "post-replies"},
		{Name: "review", Label: "Review", On: []string{"pr"}, Kind: "Review", TaskType: "recipe-review", Credentials: "clone", OnFinish: "post-review", Timeout: "45m"},
		{Name: "research", Label: "Research", On: []string{"repo"}, Kind: "Notes", TaskType: "research"},
		{Name: "summarize", Label: "Summarize", On: []string{"issue", "pr"}, Kind: "Summary", TaskType: "recipe-summarize"},
	}, repo...), nil, nil
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

// RoundTrip answers a request by its URL, or by its URL without the
// listing's narrowing (since, sort, direction), which the fixtures leave
// out.
func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	fn, ok := m.responses[req.URL.String()]
	if !ok {
		u := *req.URL
		q := u.Query()
		for _, k := range []string{"since", "sort", "direction"} {
			q.Del(k)
		}
		u.RawQuery = q.Encode()
		fn, ok = m.responses[u.String()]
	}
	if ok {
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
		"https://api.github.com/user":                                           jsonResp(`{"login": "alice", "email": "alice@example.com"}`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(issuesJSON),
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

func boardRequest() reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-board", Namespace: "alice"}}
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

// A failed run's message is factory's last "Error:" line.
func TestRunErrorLine(t *testing.T) {
	g := gomega.NewWithT(t)
	g.Expect(runErrorLine(factorycli.Result{
		Output: "cloning...\nError: failed to create review on GitHub: 403 restricted\ntrailer\n",
		Err:    fmt.Errorf("exit status 1"),
	})).To(gomega.Equal("failed to create review on GitHub: 403 restricted"))
	g.Expect(runErrorLine(factorycli.Result{Output: "no error banner", Err: fmt.Errorf("exit status 1")})).To(gomega.Equal("exit status 1"))
	g.Expect(runErrorLine(factorycli.Result{})).To(gomega.Equal("the run failed"))
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
