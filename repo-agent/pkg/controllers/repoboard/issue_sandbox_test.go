package repoboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// issueSandbox is issue n's sandbox as factory makes it now: labelled, with
// the repo recorded, under whatever name.
func issueSandbox(name string, n string, annotations map[string]interface{}) *unstructured.Unstructured {
	annotations["repo"] = "repo"
	annotations["htmlURL"] = "https://github.com/test/repo/issues/" + n
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				factorycli.LabelIssue:               n,
			},
			"annotations": annotations,
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

func getSandbox(t *testing.T, r *Reconciler, name string) *unstructured.Unstructured {
	t.Helper()
	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "alice"}, sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

// A triage that ran in the issue's sandbox leaves its draft there under its
// own key: agentDraft in that sandbox is a review's.
func TestTriageHarvestIntoIssueSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := issueSandbox("repo-30", "30", map[string]interface{}{
		factorycli.AnnotationRecipeTriageTaskState: "Completed",
	})
	fake := newFakeLauncher()
	fake.results["alice/triage-repo-30"] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     triageResult("  labels: [bug]\n"),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	a := getSandbox(t, r, "repo-30").GetAnnotations()
	g.Expect(a[factorycli.AnnotationTriageOutput]).To(gomega.ContainSubstring("labels: [bug]"))
	g.Expect(a[AnnotationAgentDraft]).To(gomega.BeEmpty())
	g.Expect(a[AnnotationTriagedAt]).NotTo(gomega.BeEmpty())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// A Fix click on an issue whose sandbox has only been triaged in launches
// the fix: the sandbox existing is not the fix having started.
func TestFixClickOnTriagedIssueLaunches(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := issueSandbox("fix-repo-77", "77", map[string]interface{}{
		factorycli.AnnotationRecipeTriageTaskState: "Completed",
		factorycli.AnnotationTriageOutput:          storedOutput("Triage", "triage:\n  assessment: x"),
		AnnotationTriagedAt:                        time.Now().UTC().Format(time.RFC3339),
	})
	fake := newFakeLauncher()
	req := click(boardv1alpha1.VerbFix, 77)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/fix-repo-77"))
	g.Expect(launches[0].FixOpts).NotTo(gomega.BeNil())
}

// A finished fix and a running triage share the issue's sandbox: idle
// pause waits for the triage.
func TestPauseWaitsForTriage(t *testing.T) {
	g := gomega.NewWithT(t)
	long := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	running := issueSandbox("fix-repo-5", "5", map[string]interface{}{
		factorycli.AnnotationTaskState:             "Completed",
		factorycli.AnnotationCompletionTime:        long,
		factorycli.AnnotationRecipeTriageTaskState: "Running",
	})
	finished := issueSandbox("fix-repo-6", "6", map[string]interface{}{
		factorycli.AnnotationTaskState:             "Completed",
		factorycli.AnnotationCompletionTime:        long,
		factorycli.AnnotationRecipeTriageTaskState: "Completed",
	})
	triagedOnly := issueSandbox("fix-repo-7", "7", map[string]interface{}{
		factorycli.AnnotationCompletionTime:        long,
		factorycli.AnnotationRecipeTriageTaskState: "Completed",
	})
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(), running, finished, triagedOnly)
	r.pauseFinished(context.Background(), &workState{sandboxes: []*unstructured.Unstructured{running, finished, triagedOnly}}, time.Hour)

	for name, want := range map[string]int64{"fix-repo-5": 1, "fix-repo-6": 0, "fix-repo-7": 0} {
		replicas, _, _ := unstructured.NestedInt64(getSandbox(t, r, name).Object, "spec", "replicas")
		g.Expect(replicas).To(gomega.Equal(want), name)
	}
}

// A Triage click runs in the clicker's namespace, under their token, as a
// Fix click does: not in the board owner's.
func TestTriageClickRunsInClickersNamespace(t *testing.T) {
	g := gomega.NewWithT(t)
	bob := githubSecret()
	bob.Namespace = "bob"
	bob.Data = map[string][]byte{"oauth_pat": []byte("gho_bob")}
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbTriage, Number: 40, Member: "bob"})
	req.UID = "uid-40"
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), bob, req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("bob/triage-repo-40"))
	g.Expect(launches[0].TriageOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].TriageOpts.Namespace).To(gomega.Equal("bob"))
	g.Expect(launches[0].TriageOpts.SandboxName).To(gomega.Equal("fix-repo-40"))
	g.Expect(launches[0].TriageOpts.GithubToken).To(gomega.Equal("gho_bob"))
	g.Expect(launches[0].TriageOpts.RunName).To(gomega.Equal("request/uid-40"))
}

// A click's triage is recorded under its Request; auto-triage under the
// board, the issue and the launch, so that a failed one is retried.
func TestTriageRunName(t *testing.T) {
	if got := triageRunName("b", 5, triageClick{issue: 5, member: "bob", request: "uid-1"}); got != "request/uid-1" {
		t.Errorf("click: %q", got)
	}
	if got := triageRunName("b", 5, triageClick{}); !strings.HasPrefix(got, "auto/b/5/") {
		t.Errorf("auto: %q", got)
	}
}
