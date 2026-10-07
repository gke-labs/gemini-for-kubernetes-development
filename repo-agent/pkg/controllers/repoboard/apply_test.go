// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package repoboard

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

func applyClick(number int, run, action string) *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbApply,
		Sandbox: fmt.Sprintf("fix-repo-%d", number),
		Number:  number,
		Apply:   &boardv1alpha1.ApplyRequest{Run: run, Action: action},
	})
}

// storedOutput is a task output of kind with draft as its spec, as the
// board stores one, naming no task.
func storedOutput(kind, draft string) string {
	doc, err := factorycli.ComposeTaskOutput(kind, "", draft, "", "")
	if err != nil {
		panic(err)
	}
	return doc
}

// triageResult is a triage run's output with a Triage task output whose
// spec is spec's lines.
func triageResult(spec string) string {
	return "...\n================= ISSUE TRIAGE =================\n" +
		"apiVersion: factory.gemini.google.com/v1alpha1\nkind: Triage\nsource:\n  task: recipe-triage-1\nspec:\n" + spec +
		"================================================\n"
}

func draftSandbox(annotations map[string]interface{}) *unstructured.Unstructured {
	annotations["htmlURL"] = "https://github.com/test/repo/issues/42"
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":        "fix-repo-42",
			"namespace":   "alice",
			"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": annotations,
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

// A plan's comment is factory apply's, run with the clicker's token on the
// plan as it is now; done, it stamps the sandbox and settles the click.
func TestApplyPlanComment(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		AnnotationPlannedAt: "2026-10-01T00:00:00Z",
		factorycli.AnnotationPlanOutput: "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
			"target:\n  url: https://github.com/test/repo/issues/42\nsource:\n  task: recipe-plan-1\n" +
			"spec:\n  markdown: |-\n    ## Summary\n    Edited plan.\n",
	})
	req := applyClick(42, "plan", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	res, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res.RequeueAfter).To(gomega.Equal(applyRequeue))
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/apply-fix-repo-42-plan-comment"))
	opts := launches[0].ApplyOpts
	g.Expect(opts).NotTo(gomega.BeNil())
	g.Expect(opts.Action).To(gomega.Equal("comment"))
	g.Expect(opts.GithubToken).To(gomega.Equal("gho_alice"))
	g.Expect(opts.Doc).To(gomega.And(
		gomega.ContainSubstring("task: recipe-plan-1"),
		gomega.ContainSubstring("Edited plan."),
		gomega.ContainSubstring("url: https://github.com/test/repo/issues/42")))
	g.Expect(requestStatus(t, r, req).Phase).NotTo(gomega.Equal(boardv1alpha1.RequestSucceeded))

	// Still writing: no second launch.
	fake.running["alice/apply-fix-repo-42-plan-comment"] = true
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	delete(fake.running, "alice/apply-fix-repo-42-plan-comment")
	fake.results["alice/apply-fix-repo-42-plan-comment"] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(factorycli.IsApplied(getSandbox(t, r, "fix-repo-42").GetAnnotations(), factorycli.AnnotationPlanApplied, "comment")).To(gomega.BeTrue())
}

// A failed write fails the click with what factory said, and is not
// retried by itself.
func TestApplyFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]\n  assessment: A crash."),
		AnnotationTriagedAt:               "2026-10-01T00:00:00Z",
	})
	req := applyClick(42, "recipe-triage", "label")
	fake := newFakeLauncher()
	fake.results["alice/apply-fix-repo-42-recipe-triage-label"] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Second),
		Output:     "applying the Triage for https://github.com/test/repo/issues/42: 403 Resource not accessible\n",
		Err:        errors.New("exit status 1"),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("ApplyFailed"))
	g.Expect(status.Message).To(gomega.ContainSubstring("403 Resource not accessible"))
	g.Expect(getSandbox(t, r, "fix-repo-42").GetAnnotations()).NotTo(gomega.HaveKey(factorycli.AnnotationTriageApplied))
}

// A stored triage that names no task gets its run's, which is what
// factory dedups on; posting its assessment parks the sandbox.
func TestApplyTriageCommentWithoutTask(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationTriageOutput: storedOutput("Triage", "triage:\n  labels: [bug]\n  duplicates: ['#7']\n  assessment: A crash."),
		factorycli.AnnotationTriageRun:    `{"name":"triage/b/42/1","task":"recipe-triage-9","startedAt":"2026-10-01T00:00:00Z","kind":"Triage"}`,
		AnnotationTriagedAt:               "2026-10-01T00:00:00Z",
	})
	req := applyClick(42, "recipe-triage", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ApplyOpts.Doc).To(gomega.And(
		gomega.ContainSubstring("kind: Triage"),
		gomega.ContainSubstring("task: recipe-triage-9"),
		gomega.ContainSubstring("- 7"),
		gomega.ContainSubstring("assessment: A crash.")))

	fake.results[launches[0].Key] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	updated := getSandbox(t, r, "fix-repo-42")
	g.Expect(factorycli.IsApplied(updated.GetAnnotations(), factorycli.AnnotationTriageApplied, "comment")).To(gomega.BeTrue())
	replicas, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.BeZero())
}

// The draft went before the write ran (rejected): nothing to post.
func TestApplyWithoutDraft(t *testing.T) {
	g := gomega.NewWithT(t)
	req := applyClick(42, "plan", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, req).Reason).To(gomega.Equal("NoDraft"))
}

// Any kind's draft applies the same way: a write its output offers, under
// its run's name, stamped in its run's applied annotation.
func TestApplyAnyKind(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationFixRun: `{"name":"fix/b/42/1","task":"recipe-fix-1","startedAt":"2026-10-01T00:00:00Z","kind":"Change"}`,
		"board.gemini.google.com/fix-output": "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\n" +
			"source:\n  task: recipe-fix-1\nspec:\n  title: Fix it\n  body: Fixes #42.\n" +
			"actions:\n  - verb: open-pr\n",
	})
	req := applyClick(42, "fix", "open-pr")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/apply-fix-repo-42-fix-open-pr"))
	g.Expect(launches[0].ApplyOpts.Action).To(gomega.Equal("open-pr"))
	g.Expect(launches[0].ApplyOpts.Doc).To(gomega.And(
		gomega.ContainSubstring("kind: Change"),
		gomega.ContainSubstring("title: Fix it"),
		gomega.ContainSubstring("url: https://github.com/test/repo/issues/42")))

	fake.results[launches[0].Key] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(factorycli.IsApplied(getSandbox(t, r, "fix-repo-42").GetAnnotations(), "board.gemini.google.com/fix-applied", "open-pr")).To(gomega.BeTrue())
}

// A write the stored output does not offer, or a verb that is not a
// write, is refused.
func TestApplyRefusesWhatIsNotOffered(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationPlanOutput: storedOutput("Plan", "## Summary\nA plan."),
	})
	notOffered := applyClick(42, "plan", "label")
	notAWrite := applyClick(42, "plan", "reject")
	notAWrite.Name = "reject-click"
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, notOffered, notAWrite)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, notOffered).Reason).To(gomega.Equal("NoDraft"))
	g.Expect(requestStatus(t, r, notAWrite).Reason).To(gomega.Equal("Malformed"))
}
