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
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

func applyClick(number int, kind, action string) *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:   boardv1alpha1.VerbApply,
		Number: number,
		Apply:  &boardv1alpha1.ApplyRequest{Kind: kind, Action: action},
	})
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
		AnnotationPlanDraft: "## Summary\nEdited plan.",
		AnnotationPlannedAt: "2026-10-01T00:00:00Z",
		factorycli.AnnotationPlanOutput: "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
			"target:\n  url: https://github.com/test/repo/issues/42\nsource:\n  task: recipe-plan-1\n",
	})
	req := applyClick(42, "Plan", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	res, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res.RequeueAfter).To(gomega.Equal(applyRequeue))
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/apply-repo-42-plan-comment"))
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
	fake.running["alice/apply-repo-42-plan-comment"] = true
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	delete(fake.running, "alice/apply-repo-42-plan-comment")
	fake.results["alice/apply-repo-42-plan-comment"] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(getSandbox(t, r, "fix-repo-42").GetAnnotations()).To(gomega.HaveKey(factorycli.AnnotationPlanCommented))
}

// A failed write fails the click with what factory said, and is not
// retried by itself.
func TestApplyFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationTriageDraft: "triage:\n  labels: [bug]\n  assessment: A crash.",
		AnnotationTriagedAt:              "2026-10-01T00:00:00Z",
	})
	req := applyClick(42, "Triage", "label")
	fake := newFakeLauncher()
	fake.results["alice/apply-repo-42-triage-label"] = factorycli.Result{
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
	g.Expect(getSandbox(t, r, "fix-repo-42").GetAnnotations()).NotTo(gomega.HaveKey(factorycli.AnnotationTriageLabeled))
}

// A triage drafted before task outputs were kept gets a document whose
// task is stable for the draft, which is what factory dedups on; posting
// its assessment parks the sandbox.
func TestApplyTriageCommentWithoutTaskOutput(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := draftSandbox(map[string]interface{}{
		factorycli.AnnotationTriageDraft: "triage:\n  labels: [bug]\n  duplicates: ['#7']\n  assessment: A crash.",
		AnnotationTriagedAt:              "2026-10-01T00:00:00Z",
	})
	req := applyClick(42, "Triage", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ApplyOpts.Doc).To(gomega.And(
		gomega.ContainSubstring("kind: Triage"),
		gomega.ContainSubstring("task: board-fix-repo-42-1790812800"),
		gomega.ContainSubstring("- 7"),
		gomega.ContainSubstring("assessment: A crash.")))

	fake.results[launches[0].Key] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	updated := getSandbox(t, r, "fix-repo-42")
	g.Expect(updated.GetAnnotations()).To(gomega.HaveKey(AnnotationTriagePublished))
	replicas, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.BeZero())
}

// The draft went before the write ran (rejected): nothing to post.
func TestApplyWithoutDraft(t *testing.T) {
	g := gomega.NewWithT(t)
	req := applyClick(42, "Plan", "comment")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, req).Reason).To(gomega.Equal("NoDraft"))
}
