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
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

const summaryOutput = "================== TASK OUTPUT =================\napiVersion: factory.gemini.google.com/v1alpha1\nkind: Summary\nspec:\n  markdown: It is about a thing.\n================================================\n"

// launchOn is a click on summarize's launch button on an issue or a PR,
// with inputs.
func launchOn(item string, number int, inputs map[string]string) *boardv1alpha1.Request {
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "summarize", Item: item, Number: number, Inputs: inputs})
	req.UID = types.UID("uid-" + item)
	return req
}

func recipeSandbox(name, url string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":        name,
			"namespace":   "alice",
			"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{"htmlURL": url},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

// A recipe the board has no pass for launches as data: in the sandbox
// factory runs it in, with the click's inputs, under a run name the
// click owns, and the click stands while it runs.
func TestRecipeLaunch(t *testing.T) {
	for _, tc := range []struct {
		item, sandbox, url string
	}{
		{"issue", "fix-repo-12", "https://github.com/test/repo/issues/12"},
		{"pr", "fix-repo-12", "https://github.com/test/repo/pull/12"},
	} {
		t.Run(tc.item, func(t *testing.T) {
			g := gomega.NewWithT(t)
			fake := newFakeLauncher()
			req := launchOn(tc.item, 12, map[string]string{"length": "short"})
			r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), req)
			r.reconcileBoard(t)

			launches := fake.launches()
			g.Expect(launches).To(gomega.HaveLen(1))
			opts := launches[0].RecipeOpts
			g.Expect(opts).NotTo(gomega.BeNil())
			g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-summarize-repo-" + tc.item + "-12"))
			g.Expect(opts.Recipe).To(gomega.Equal("summarize"))
			g.Expect(opts.SandboxName).To(gomega.Equal(tc.sandbox))
			g.Expect(opts.URL).To(gomega.Equal(tc.url))
			g.Expect(opts.Namespace).To(gomega.Equal("alice"))
			g.Expect(opts.RunName).To(gomega.Equal("request/uid-" + tc.item))
			g.Expect(opts.Inputs).To(gomega.Equal(map[string]string{"length": "short"}))

			// In flight: running, not launched again.
			fake.running[launches[0].Key] = true
			r.reconcileBoard(t)
			g.Expect(fake.launches()).To(gomega.HaveLen(1))
			g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))
		})
	}
}

// A recipe whose kind its runner applies nothing of runs with no apply.
func TestSummarizeAppliesNothing(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), launchOn("pr", 12, nil))
	r.reconcileBoard(t)
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].RecipeOpts.Apply).To(gomega.BeEmpty())
}

// The run's task output is stored on its sandbox as its run's, which is
// what settles the click.
func TestRecipeStoresItsOutput(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.results["alice/recipe-summarize-repo-pr-12"] = factorycli.Result{FinishedAt: time.Now().Add(time.Minute), Output: summaryOutput}
	req := launchOn("pr", 12, nil)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(),
		recipeSandbox("fix-repo-12", "https://github.com/test/repo/pull/12"), req)
	r.reconcileBoard(t)

	g.Expect(fake.launches()).To(gomega.BeEmpty())
	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-12", Namespace: "alice"}, sb)).To(gomega.Succeed())
	g.Expect(sb.GetAnnotations()["board.gemini.google.com/recipe-summarize-output"]).To(gomega.ContainSubstring("kind: Summary"))
	g.Expect(sb.GetAnnotations()[AnnotationExecutor]).To(gomega.Equal("alice"))
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(status.Sandbox).To(gomega.Equal("fix-repo-12"))
}

// A click buys one run: a failed run, one with no output, a recipe
// factory does not list, or one that does not start on the row, fails
// the click rather than retrying.
func TestRecipeFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		req    *boardv1alpha1.Request
		result *factorycli.Result
		reason string
	}{
		"run failed": {launchOn("issue", 12, nil), &factorycli.Result{Err: errors.New("exit status 1")}, "RunFailed"},
		"no output":  {launchOn("issue", 12, nil), &factorycli.Result{Output: "done\n"}, "NoOutput"},
		"unknown recipe": {testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "nope", Item: "issue", Number: 12}),
			nil, "UnknownRecipe"},
		"research on an issue": {testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "research", Item: "issue", Number: 12}),
			nil, "NotLaunchable"},
		"review on an issue": {testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "review", Item: "issue", Number: 12}),
			nil, "NotLaunchable"},
		"care on an issue": {testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "issue", Number: 12}),
			nil, "NotLaunchable"},
	} {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			fake := newFakeLauncher()
			if tc.result != nil {
				res := *tc.result
				res.FinishedAt = time.Now().Add(time.Minute)
				fake.results["alice/recipe-summarize-repo-issue-12"] = res
			}
			r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(),
				recipeSandbox("fix-repo-12", "https://github.com/test/repo/issues/12"), tc.req)
			r.reconcileBoard(t)
			g.Expect(fake.launches()).To(gomega.BeEmpty())
			status := requestStatus(t, r, tc.req)
			g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
			g.Expect(status.Reason).To(gomega.Equal(tc.reason))
		})
	}
}

// A recipe on [my-pr] starts on a PR, in the PR's fix sandbox: the fix's
// that opened it, else one of its own; factory refuses it on a PR that is
// not the member's.
func TestCareLaunchesOnAPR(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), req)
	r.reconcileBoard(t)

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].RecipeOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].RecipeOpts.Recipe).To(gomega.Equal("care"))
	g.Expect(launches[0].RecipeOpts.SandboxName).To(gomega.Equal("fix-repo-12"))
	// Its run pushes, so it posts the replies that answer for the push:
	// the catalog's onFinish, not anything the board knows of care.
	g.Expect(launches[0].RecipeOpts.Apply).To(gomega.Equal("post-replies"))
	// No timeout declared: the runner's default.
	g.Expect(launches[0].RecipeOpts.Timeout).To(gomega.BeZero())
}

// A recipe's run is bounded by the timeout the catalog declares for it,
// and finishes with the action it declares; one with neither gets the
// runner's default and applies nothing.
func TestRecipeFinishesAsTheCatalogSays(t *testing.T) {
	g := gomega.NewWithT(t)
	catalog := []boardv1alpha1.BoardRecipe{
		{Name: "gate", Label: "Gate", On: []string{"pr"}, Kind: "Review", TaskType: "recipe-gate", OnFinish: "post-review", Timeout: "20m"},
		{Name: "odd", Label: "Odd", On: []string{"pr"}, Kind: "Summary", TaskType: "recipe-odd", Timeout: "soon"},
	}
	for _, tc := range []struct {
		recipe  string
		apply   string
		timeout time.Duration
	}{{"gate", "post-review", 20 * time.Minute}, {"odd", "", 0}} {
		fake := newFakeLauncher()
		fake.catalog = catalog
		req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: tc.recipe, Item: "pr", Number: 12})
		r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), req)
		r.reconcileBoard(t)
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1), tc.recipe)
		g.Expect(launches[0].RecipeOpts.Apply).To(gomega.Equal(tc.apply), tc.recipe)
		g.Expect(launches[0].RecipeOpts.Timeout).To(gomega.Equal(tc.timeout), tc.recipe)
	}
}

// A Change's replies were posted by its run: its output is stored with
// post-replies stamped applied, so nothing waits on the member.
func TestCareRepliesAreApplied(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	finished := time.Now().Add(time.Minute)
	fake.results["alice/recipe-care-repo-pr-12"] = factorycli.Result{FinishedAt: finished, Output: "================== TASK OUTPUT =================\napiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\nspec:\n  title: t\n  report: Rebased.\n================================================\n"}
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(),
		recipeSandbox("fix-repo-12", "https://github.com/test/repo/pull/12"), req)
	r.reconcileBoard(t)

	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-12", Namespace: "alice"}, sb)).To(gomega.Succeed())
	run := factorycli.RunAnnotation("recipe-care")
	g.Expect(sb.GetAnnotations()[factorycli.OutputAnnotation(run)]).To(gomega.ContainSubstring("kind: Change"))
	g.Expect(factorycli.IsApplied(sb.GetAnnotations(), factorycli.AppliedAnnotation(run), "post-replies")).To(gomega.BeTrue())
}

// A care run whose posting failed keeps its output unapplied, so the
// draft waits for Post replies; a run that left none still gets the
// board, so its session can revise it.
func TestCareFailedApplyKeepsTheDraft(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	finished := time.Now().Add(time.Minute)
	fake.results["alice/recipe-care-repo-pr-12"] = factorycli.Result{FinishedAt: finished, Err: errors.New("applying post-replies: exit status 1"),
		Output: "================== TASK OUTPUT =================\napiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\nspec:\n  title: t\n  report: Rebased.\n================================================\n"}
	fake.results["alice/recipe-care-repo-pr-13"] = factorycli.Result{FinishedAt: finished, Err: errors.New("exit status 1")}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(),
		recipeSandbox("fix-repo-12", "https://github.com/test/repo/pull/12"),
		recipeSandbox("fix-repo-13", "https://github.com/test/repo/pull/13"),
		testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12}),
		testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 13}))
	r.reconcileBoard(t)

	run := factorycli.RunAnnotation("recipe-care")
	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-12", Namespace: "alice"}, sb)).To(gomega.Succeed())
	g.Expect(sb.GetAnnotations()[factorycli.OutputAnnotation(run)]).To(gomega.ContainSubstring("report: Rebased."))
	g.Expect(factorycli.IsApplied(sb.GetAnnotations(), factorycli.AppliedAnnotation(run), "post-replies")).To(gomega.BeFalse())
	g.Expect(sb.GetAnnotations()[AnnotationBoard]).To(gomega.Equal("test-board"))

	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-13", Namespace: "alice"}, sb)).To(gomega.Succeed())
	g.Expect(sb.GetAnnotations()).NotTo(gomega.HaveKey(factorycli.OutputAnnotation(run)))
	g.Expect(sb.GetAnnotations()[AnnotationBoard]).To(gomega.Equal("test-board"))
	g.Expect(sb.GetAnnotations()[AnnotationExecutor]).To(gomega.Equal("alice"))
}

// The board publishes factory's catalog on its status, for the API.
func TestBoardPublishesRecipes(t *testing.T) {
	g := gomega.NewWithT(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret())
	r.reconcileBoard(t)
	board := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "test-board", Namespace: "alice"}, board)).To(gomega.Succeed())
	var names []string
	for _, rec := range board.Status.Recipes {
		names = append(names, rec.Name)
	}
	g.Expect(names).To(gomega.Equal([]string{"triage", "plan", "fix", "care", "review", "research", "summarize"}))
}

func (r *Reconciler) reconcileBoard(t *testing.T) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), boardRequest()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// A PR's recipes run in the sandbox of the fix that opened it.
func TestPRRecipeRunsInTheFixSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fix := recipeSandbox("fix-repo-10", "https://github.com/test/repo/pull/12")
	fix.SetLabels(map[string]string{"factory.gemini.google.com/managed": "true", factorycli.LabelPR: "12"})
	fix.SetAnnotations(map[string]string{"htmlURL": "https://github.com/test/repo/pull/12", "repo": "repo"})
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), fix, req)
	r.reconcileBoard(t)

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].RecipeOpts.SandboxName).To(gomega.Equal("fix-repo-10"))
}

// A click for a new conversation opens one, on a recipe in a session;
// any other recipe has no session to open.
func TestRecipeNewSession(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fix := recipeSandbox("fix-repo-10", "https://github.com/test/repo/pull/12")
	fix.SetLabels(map[string]string{"factory.gemini.google.com/managed": "true", factorycli.LabelPR: "12"})
	fix.SetAnnotations(map[string]string{"htmlURL": "https://github.com/test/repo/pull/12", "repo": "repo"})
	care := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12, NewSession: true})
	summarize := launchOn("issue", 7, nil)
	summarize.Spec.NewSession = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), fix, care, summarize)
	r.reconcileBoard(t)

	byRecipe := map[string]bool{}
	for _, l := range fake.launches() {
		if l.RecipeOpts != nil {
			byRecipe[l.RecipeOpts.Recipe] = l.RecipeOpts.NewSession
		}
	}
	g.Expect(byRecipe).To(gomega.Equal(map[string]bool{"care": true, "summarize": false}))
}

// A recipe whose sandbox is running another run waits for it, saying so
// on its Request, instead of launching into factory's refusal; once the
// other run has ended, it launches.
func TestRecipeWaitsForABusySandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := recipeSandbox("fix-repo-12", "https://github.com/test/repo/pull/12")
	sb.SetAnnotations(map[string]string{
		"htmlURL":                       "https://github.com/test/repo/pull/12",
		factorycli.RunAnnotation("fix"): `{"name":"fix/b/12/1","task":"t1","recipe":"fix","state":"Running","startedAt":"2026-10-14T10:00:00Z"}`,
	})
	fake := newFakeLauncher()
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "care", Item: "pr", Number: 12})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), sb, req)
	r.reconcileBoard(t)
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestPending))
	g.Expect(status.Reason).To(gomega.Equal("Waiting"))
	g.Expect(status.Message).To(gomega.ContainSubstring("waiting for fix"))

	// The other run ended: the recipe launches.
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-12", Namespace: "alice"}, sb)).To(gomega.Succeed())
	annotations := sb.GetAnnotations()
	annotations[factorycli.RunAnnotation("fix")] = `{"name":"fix/b/12/1","task":"t1","recipe":"fix","state":"Completed","startedAt":"2026-10-14T10:00:00Z"}`
	sb.SetAnnotations(annotations)
	g.Expect(r.Update(context.Background(), sb)).To(gomega.Succeed())
	r.reconcileBoard(t)
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
}

// A recipe whose output is a Review is not launched over a pending
// review of the member's that factory did not post: it could never be
// posted. One factory posted, or a submitted review, does not stop it.
func TestReviewRecipeRefusedOverTheMembersPendingReview(t *testing.T) {
	g := gomega.NewWithT(t)
	catalog := []boardv1alpha1.BoardRecipe{{Name: "gate", Label: "Gate", On: []string{"pr"}, Kind: "Review", TaskType: "recipe-gate", Credentials: "clone"}}
	for _, tc := range []struct {
		name    string
		reviews string
		refused bool
	}{
		{"own pending", `[{"id":1,"state":"PENDING","body":"mine","user":{"login":"alice"}}]`, true},
		{"factory's pending", `[{"id":1,"state":"PENDING","body":"x <!-- factory:task-output kind=Review task=t -->","user":{"login":"alice"}}]`, false},
		{"submitted", `[{"id":1,"state":"COMMENTED","body":"mine","user":{"login":"alice"}}]`, false},
		{"someone else's pending", `[{"id":1,"state":"PENDING","body":"theirs","user":{"login":"bob"}}]`, false},
	} {
		fake := newFakeLauncher()
		fake.catalog = catalog
		req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "gate", Item: "pr", Number: 12})
		r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), req)
		reviews := tc.reviews
		newGithubClientFromToken = func(_ context.Context, _ string) *github.Client {
			return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{suffix: "/reviews", body: reviews}})
		}
		r.reconcileBoard(t)
		if tc.refused {
			g.Expect(fake.launches()).To(gomega.BeEmpty(), tc.name)
			status := requestStatus(t, r, req)
			g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed), tc.name)
			g.Expect(status.Reason).To(gomega.Equal("OwnPendingReview"), tc.name)
		} else {
			g.Expect(fake.launches()).To(gomega.HaveLen(1), tc.name)
		}
	}
}

// A board's catalog is factory's list for its repository: the built-ins
// and the repository's own recipes (.agents/recipes), published on the
// board's status, read with the board owner's token and kept catalogTTL.
// A click on a repository recipe runs it as recipe run --recipe.
func TestRepositoryRecipesAreOnTheBoard(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.repoRecipes = []boardv1alpha1.BoardRecipe{
		{Name: "explain-pr", Label: "Explain", On: []string{"pr"}, Kind: "Summary", TaskType: "recipe-explain-pr", Source: ".agents/recipes/explain-pr.yaml"},
	}
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: "explain-pr", Item: "pr", Number: 12})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(map[string]string{}), githubSecret(), req)
	r.reconcileBoard(t)

	g.Expect(fake.listed).To(gomega.ContainElement("https://github.com/test/repo"))
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].RecipeOpts.Recipe).To(gomega.Equal("explain-pr"))
	g.Expect(launches[0].RecipeOpts.RepoRecipe).To(gomega.BeTrue())

	board := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), boardRequest().NamespacedName, board)).To(gomega.Succeed())
	names := []string{}
	for _, rec := range board.Status.Recipes {
		names = append(names, rec.Name)
	}
	g.Expect(names).To(gomega.ContainElements("fix", "explain-pr"))

	// Read once per catalogTTL, not on every reconcile.
	before := len(fake.listed)
	r.reconcileBoard(t)
	g.Expect(fake.listed).To(gomega.HaveLen(before))
}
