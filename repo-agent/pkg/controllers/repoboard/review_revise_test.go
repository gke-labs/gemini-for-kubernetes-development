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

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

const reviewReviseKeyAt = "alice/revise-review-repo-42"

// postedReviewSandbox is a review sandbox whose review is on GitHub, its
// run (task recipe-review-1) recorded.
func postedReviewSandbox(state string) map[string]interface{} {
	return map[string]interface{}{
		AnnotationReviewState: state,
		factorycli.AnnotationReviewRun: `{"name":"review/test-board/42/1","task":"recipe-review-1","startedAt":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) +
			`","kind":"Review","revises":[{"id":"review","label":"Update review"}]}`,
	}
}

func reviewReviseClick() *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbRevise,
		Sandbox: "review-repo-42",
		Revise:  "review",
	})
}

// Update review revises in the review's session, keyed by its sandbox, and
// the runner posts what it writes: the review is pending again, even after
// the member submitted the one before.
func TestUpdateReviewRevisesAndPosts(t *testing.T) {
	g := gomega.NewWithT(t)
	req := reviewReviseClick()
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), reviewSandboxFixture(postedReviewSandbox("submitted")), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal(reviewReviseKeyAt))
	opts := launches[0].ReviseOpts
	g.Expect(opts).NotTo(gomega.BeNil())
	g.Expect(opts.SandboxName).To(gomega.Equal("review-repo-42"))
	g.Expect(opts.Revise).To(gomega.Equal("review"))
	g.Expect(opts.Session).To(gomega.Equal("recipe-review-1"))
	g.Expect(opts.Apply).To(gomega.Equal("post-review"))
	g.Expect(opts.RunName).To(gomega.HavePrefix("revise/test-board/review-repo-42/review/"))

	fake.running[reviewReviseKeyAt] = true
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	delete(fake.running, reviewReviseKeyAt)
	fake.results[reviewReviseKeyAt] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Second),
		Output: "================== TASK OUTPUT =================\n" +
			"apiVersion: factory.gemini.google.com/v1alpha1\nkind: Review\nspec:\n  body: Looks good.\n" +
			"================================================\nposted\n",
	}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	a := sandboxAnnotations(t, r, "review-repo-42")
	// The Review it posted is kept, as posted.
	g.Expect(a["board.gemini.google.com/recipe-review-output"]).To(gomega.ContainSubstring("body: Looks good."))
	g.Expect(factorycli.IsApplied(a, "board.gemini.google.com/recipe-review-applied", "post-review")).To(gomega.BeTrue())
	g.Expect(a[AnnotationReviewState]).To(gomega.Equal("pending"))
	g.Expect(a).To(gomega.HaveKey(AnnotationReviewedAt))
	// The review itself is not relaunched by any of this.
	for _, l := range fake.launches() {
		g.Expect(l.ReviewOpts).To(gomega.BeNil())
	}
}

// An Update review that fails fails the click with what factory said, and
// leaves the review as it was.
func TestUpdateReviewFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	req := reviewReviseClick()
	fake := newFakeLauncher()
	fake.results[reviewReviseKeyAt] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Second),
		Output:     "posting the review: a pending review the member wrote is in the way\n",
		Err:        errors.New("exit status 1"),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), reviewSandboxFixture(postedReviewSandbox("pending")), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Message).To(gomega.ContainSubstring("pending review the member wrote"))
}

// Not while the review itself runs.
func TestUpdateReviewWaitsForTheReview(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.running["alice/review-repo-42"] = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), reviewSandboxFixture(postedReviewSandbox("pending")), reviewReviseClick())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}
