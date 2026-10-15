package repoboard

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// recipeRequests are the recipe Requests filed on the fixture board.
func recipeRequests(t *testing.T, r *Reconciler) []boardv1alpha1.Request {
	t.Helper()
	list := &boardv1alpha1.RequestList{}
	if err := r.List(context.Background(), list, client.InNamespace("alice"),
		client.MatchingLabels{boardv1alpha1.LabelVerb: boardv1alpha1.VerbRecipe}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// subjects are what the Requests are for: recipe, item and number, and as
// whom.
func subjects(reqs []boardv1alpha1.Request) []string {
	var out []string
	for _, req := range reqs {
		out = append(out, req.Spec.Member+":"+req.Spec.Key())
	}
	return out
}

// auto.fix "assigned" files a fix Request, as the board's owner, for an
// issue assigned to them, once; the Request is then launched as a click's
// is: under its own run name, opening the draft PR when the run ends.
func TestAutoFixFilesARequest(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := testGithubClient(`[
		{"number": 20, "title": "assigned to alice", "html_url": "https://github.com/test/repo/issues/20",
		 "assignees": [{"login": "alice"}]}
	]`)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, ghClient, testBoard(nil), githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(subjects(recipeRequests(t, r))).To(gomega.ConsistOf("alice:recipe/issue-20/fix"))
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	// The next reconcile (the Request's creation wakes the board)
	// launches it, and files nothing more while it stands.
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	reqs := recipeRequests(t, r)
	g.Expect(reqs).To(gomega.HaveLen(1))
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/recipe-fix-repo-issue-20"))
	g.Expect(launches[0].FixOpts.Namespace).To(gomega.Equal("alice"))
	g.Expect(launches[0].FixOpts.GithubToken).To(gomega.Equal("gho_alice"))
	g.Expect(launches[0].FixOpts.RunName).To(gomega.Equal("request/" + string(reqs[0].UID)))
	g.Expect(launches[0].FixOpts.Apply).To(gomega.Equal("open-pr"))
}

// auto.triage "unclaimed" files a triage Request for each unassigned
// open issue; auto off files none.
func TestAutoTriageFilesRequests(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 30, "title": "untriaged", "html_url": "https://github.com/test/repo/issues/30", "labels": []},
			{"number": 31, "title": "claimed", "html_url": "https://github.com/test/repo/issues/31", "assignees": [{"login": "bob"}]}
		]`),
	}}})
	board := testBoard(nil)
	board.Spec.Auto.Fix = "off"
	board.Spec.Auto.Triage = "unclaimed"
	r := newTestReconciler(newFakeLauncher(), ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(subjects(recipeRequests(t, r))).To(gomega.ConsistOf("alice:recipe/issue-30/triage"))

	off := testBoard(nil)
	off.Spec.Auto.Fix = "off"
	r2 := newTestReconciler(newFakeLauncher(), ghClient, off, githubSecret())
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(recipeRequests(t, r2)).To(gomega.BeEmpty())
}

// auto.review files a review Request for an eligible PR, but not over a
// review of the owner's own on GitHub: pending (one factory did not
// post) or submitted.
func TestAutoReviewFilesRequests(t *testing.T) {
	reviews := func(body string) func(context.Context, string) *github.Client {
		return func(context.Context, string) *github.Client {
			return clients.NewGitHubClientFromHTTP(&http.Client{Transport: &suffixRoundTripper{suffix: "/reviews", body: body}})
		}
	}
	for _, tc := range []struct {
		name    string
		reviews string
		filed   bool
	}{
		{"no review", `[]`, true},
		{"factory's pending review", `[{"id": 7, "state": "PENDING", "user": {"login": "alice"}, "body": "x\n<!-- factory:task-output kind=Review task=t -->"}]`, true},
		{"the owner's pending review", `[{"id": 7, "state": "PENDING", "user": {"login": "alice"}}]`, false},
		{"the owner's submitted review", `[{"id": 9, "state": "APPROVED", "user": {"login": "alice"}}]`, false},
		{"someone else's review", `[{"id": 9, "state": "APPROVED", "user": {"login": "bob"}}]`, true},
	} {
		g := gomega.NewWithT(t)
		r := newTestReconciler(newFakeLauncher(), autoReviewClient(42), autoReviewBoard(), githubSecret())
		newGithubClientFromToken = reviews(tc.reviews)
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if tc.filed {
			g.Expect(subjects(recipeRequests(t, r))).To(gomega.ConsistOf("alice:recipe/pr-42/review"), tc.name)
		} else {
			g.Expect(recipeRequests(t, r)).To(gomega.BeEmpty(), tc.name)
		}
	}
}

// An item gets one automation Request, ever: none once the recipe has a
// run recorded on its sandbox, whoever ran it and however it ended, nor
// once its draft was rejected there, nor while a Request for it stands or
// was settled lately (a failed auto run is not retried; a member clicks).
func TestAutomationFilesOnce(t *testing.T) {
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 30, "title": "an issue", "html_url": "https://github.com/test/repo/issues/30", "labels": []}
		]`),
	}}})
	board := func() *boardv1alpha1.RepoBoard {
		b := testBoard(nil)
		b.Spec.Auto.Fix = "off"
		b.Spec.Auto.Triage = "all"
		return b
	}
	run := `{"name":"request/uid-1","task":"recipe-triage-1","state":"Failed","startedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	failed := settled("triage-30-failed", boardv1alpha1.RequestFailed, 30, time.Hour)
	failed.Spec.Member = "bob"
	for name, objs := range map[string][]*unstructured.Unstructured{
		"a recorded run": {issueSandbox("fix-repo-30", "30", map[string]interface{}{factorycli.AnnotationTriageRun: run})},
		"a tombstone":    {issueSandbox("fix-repo-30", "30", map[string]interface{}{factorycli.RejectedAnnotation(factorycli.AnnotationTriageRun): time.Now().UTC().Format(time.RFC3339)})},
		"a failed click": nil,
	} {
		g := gomega.NewWithT(t)
		extra := []runtime.Object{board(), githubSecret()}
		for _, sb := range objs {
			extra = append(extra, sb)
		}
		if objs == nil {
			extra = append(extra, failed)
		}
		r := newTestReconciler(newFakeLauncher(), ghClient, extra...)
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		for _, req := range recipeRequests(t, r) {
			g.Expect(req.Spec.Member).NotTo(gomega.Equal("alice"), "%s: automation filed %s", name, req.Spec.Key())
		}
	}

	// With none of them, it does: the same issue, its sandbox bare.
	g := gomega.NewWithT(t)
	r := newTestReconciler(newFakeLauncher(), ghClient, board(), githubSecret(), issueSandbox("fix-repo-30", "30", map[string]interface{}{}))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(subjects(recipeRequests(t, r))).To(gomega.ConsistOf("alice:recipe/issue-30/triage"))
}

// A review run recorded on the PR's review sandbox, a member's included,
// is the PR's review: automation files no other.
func TestAutoReviewLeavesARecordedReview(t *testing.T) {
	g := gomega.NewWithT(t)
	run := `{"name":"request/uid-1","task":"recipe-review-1","state":"Completed","startedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	reviewed := reviewSandboxFixture(map[string]interface{}{factorycli.AnnotationReviewRun: run})
	r := newTestReconciler(newFakeLauncher(), autoReviewClient(42), autoReviewBoard(), githubSecret(), reviewed)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(recipeRequests(t, r)).To(gomega.BeEmpty())
}
