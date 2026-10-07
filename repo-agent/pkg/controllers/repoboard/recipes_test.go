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
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
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
