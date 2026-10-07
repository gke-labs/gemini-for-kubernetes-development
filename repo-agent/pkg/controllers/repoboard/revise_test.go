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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

const reviseKeyFor42 = "alice/revise-fix-repo-42"

func reviseClick(number int, revise string) *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbRevise,
		Sandbox: fmt.Sprintf("fix-repo-%d", number),
		Number:  number,
		Revise:  revise,
	})
}

// planRun is a plan run record: run named name, task task, in session
// (empty: its own), offering Update plan.
func planRun(name, task, session string, started time.Time) string {
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: name, Task: task, Session: session, StartedAt: started, Kind: "Plan",
		Revises: []factorycli.RecordedRevise{{ID: "plan", Label: "Update plan"}},
	})
	return string(run)
}

// planDraftSandbox holds a plan draft whose task output came from a revise
// in the session of recipe-plan-1, its plan run recorded.
func planDraftSandbox(extra map[string]interface{}) *unstructured.Unstructured {
	annotations := map[string]interface{}{
		factorycli.AnnotationPlanRun:       planRun("plan/test-board/42/1", "recipe-plan-1", "", time.Unix(1_000_000, 0)),
		AnnotationPlanDraft:                "## Summary\nFirst plan.",
		AnnotationPlannedAt:                "2026-10-01T00:00:00Z",
		factorycli.AnnotationPlanCommented: "2026-10-01T00:05:00Z",
		factorycli.AnnotationPlanOutput: "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
			"source:\n  task: recipe-plan-2\n  session: recipe-plan-1\n" +
			"actions:\n  - verb: revise\n    revise: plan\n    label: Update plan\n",
	}
	for k, v := range extra {
		annotations[k] = v
	}
	return draftSandbox(annotations)
}

// revisedOutput is what StartRevise's harvest leaves: the revise's Plan
// task output between the plan banner and its closer.
const revisedOutput = "================== ISSUE PLAN ==================\n" +
	"apiVersion: factory.gemini.google.com/v1alpha1\nkind: Plan\n" +
	"source:\n  task: recipe-plan-3\n  session: recipe-plan-1\n" +
	"spec:\n  markdown: |-\n    ## Summary\n    Revised plan.\n" +
	"================================================\n"

// A revise runs factory recipe revise in the session the draft came from,
// and its plan becomes the draft, not yet posted.
func TestReviseRewritesThePlanDraft(t *testing.T) {
	g := gomega.NewWithT(t)
	req := reviseClick(42, "plan")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), planDraftSandbox(nil), req)

	res, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res.RequeueAfter).To(gomega.Equal(reviseRequeue))
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal(reviseKeyFor42))
	opts := launches[0].ReviseOpts
	g.Expect(opts).NotTo(gomega.BeNil())
	g.Expect(opts.SandboxName).To(gomega.Equal("fix-repo-42"))
	g.Expect(opts.Namespace).To(gomega.Equal("alice"))
	g.Expect(opts.Revise).To(gomega.Equal("plan"))
	g.Expect(opts.Session).To(gomega.Equal("recipe-plan-1"))
	g.Expect(opts.GithubToken).To(gomega.Equal("gho_alice"))
	g.Expect(opts.RunName).To(gomega.HavePrefix("revise/test-board/fix-repo-42/plan/"))

	fake.running[reviseKeyFor42] = true
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	delete(fake.running, reviseKeyFor42)
	fake.results[reviseKeyFor42] = factorycli.Result{FinishedAt: time.Now().Add(time.Second), Output: revisedOutput}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	a := getSandbox(t, r, "fix-repo-42").GetAnnotations()
	g.Expect(a[AnnotationPlanDraft]).To(gomega.Equal("## Summary\nRevised plan."))
	g.Expect(a[factorycli.AnnotationPlanOutput]).To(gomega.And(
		gomega.ContainSubstring("task: recipe-plan-3"), gomega.Not(gomega.ContainSubstring("Revised plan."))))
	g.Expect(a).NotTo(gomega.HaveKey(factorycli.AnnotationPlanCommented))
	g.Expect(a[AnnotationPlannedAt]).NotTo(gomega.Equal("2026-10-01T00:00:00Z"))
}

// A revise that fails — the session was mid-turn — fails the click with
// what factory said, and is not run again by itself.
func TestReviseFailureIsNotRetried(t *testing.T) {
	g := gomega.NewWithT(t)
	req := reviseClick(42, "plan")
	fake := newFakeLauncher()
	fake.results[reviseKeyFor42] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Second),
		Output:     "session recipe-plan-1 is busy: a turn is in flight\n",
		Err:        errors.New("exit status 1"),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), planDraftSandbox(nil), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("ReviseFailed"))
	g.Expect(status.Message).To(gomega.ContainSubstring("a turn is in flight"))
	g.Expect(getSandbox(t, r, "fix-repo-42").GetAnnotations()[AnnotationPlanDraft]).To(gomega.Equal("## Summary\nFirst plan."))
}

// A plan and a revise of one issue never run at once: they would race to
// store the draft.
func TestReviseAndPlanTakeTurns(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.running["alice/plan-repo-42"] = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), planDraftSandbox(nil), reviseClick(42, "plan"))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// A refinement waits for a running revise.
	fake = newFakeLauncher()
	fake.running[reviseKeyFor42] = true
	sb := planDraftSandbox(map[string]interface{}{
		AnnotationPlanFeedback:   "smaller steps",
		AnnotationPlanFeedbackAt: "2026-10-02T00:00:00Z",
	})
	r = newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.PlanOpts).To(gomega.BeNil(), "a plan launched while a revise runs")
	}
}

// factory recipe revise does not wake a paused sandbox; the controller
// does, stamped so the pause pass leaves it to boot.
func TestReviseWakesAPausedSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := planDraftSandbox(nil)
	g.Expect(unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas")).To(gomega.Succeed())
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, reviseClick(42, "plan"))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	updated := getSandbox(t, r, "fix-repo-42")
	replicas, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.Equal(int64(1)))
	g.Expect(updated.GetAnnotations()).To(gomega.HaveKey(AnnotationUnpausedAt))
}

// After a restart, the revise recorded on the sandbox since the click is
// followed by its run name; the plan run before it is not.
func TestReviseResumesItsRecordedRun(t *testing.T) {
	for _, tc := range []struct {
		name, run string
		resumed   bool
	}{
		{"revise since the click", "revise/test-board/fix-repo-42/plan/1", true},
		{"the plan's run", "plan/test-board/42/1", false},
		{"another sandbox's revise", "revise/test-board/fix-repo-43/plan/1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			sb := planDraftSandbox(map[string]interface{}{factorycli.AnnotationPlanRun: planRun(tc.run, "recipe-plan-3", "recipe-plan-1", time.Now().Add(time.Second))})
			fake := newFakeLauncher()
			r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, reviseClick(42, "plan"))
			_, err := r.Reconcile(context.Background(), boardRequest())
			g.Expect(err).NotTo(gomega.HaveOccurred())
			launches := fake.launches()
			g.Expect(launches).To(gomega.HaveLen(1))
			g.Expect(launches[0].ReviseOpts.RunName == tc.run).To(gomega.Equal(tc.resumed), "run name %q", launches[0].ReviseOpts.RunName)
		})
	}
}

// With no task output to say which session, the recorded plan run's is
// revised in: its session, for a revise, else its task.
func TestReviseSessionFallsBackToTheRecordedRun(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := planDraftSandbox(map[string]interface{}{factorycli.AnnotationPlanRun: planRun("revise/test-board/fix-repo-42/plan/1", "recipe-plan-2", "recipe-plan-0", time.Unix(1_000_000, 0))})
	a := sb.GetAnnotations()
	delete(a, factorycli.AnnotationPlanOutput)
	sb.SetAnnotations(a)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, reviseClick(42, "plan"))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ReviseOpts.Session).To(gomega.Equal("recipe-plan-0"))
}

// No run offering the revise (the sandbox is gone), or no draft to revise
// (rejected, or approved and gone): the click fails.
func TestReviseWithoutDraft(t *testing.T) {
	g := gomega.NewWithT(t)
	req := reviseClick(42, "plan")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, req).Reason).To(gomega.Equal("NoRun"))

	req = reviseClick(42, "plan")
	sb := planDraftSandbox(nil)
	a := sb.GetAnnotations()
	delete(a, AnnotationPlanDraft)
	sb.SetAnnotations(a)
	r = newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, req).Reason).To(gomega.Equal("NothingToRevise"))
}

func TestReviseSubjectNamesTheRevise(t *testing.T) {
	spec := boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRevise, Sandbox: "fix-repo-42", Number: 42, Revise: "plan"}
	if got := spec.Key(); !strings.HasSuffix(got, "revise/fix-repo-42/plan") {
		t.Errorf("key = %q", got)
	}
}
