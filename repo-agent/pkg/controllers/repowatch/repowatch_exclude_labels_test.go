package repowatch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_ExcludeLabels(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	// Mock PRs: 
	// PR 1: has label "needs-triage"
	// PR 2: has labels "needs-triage" and "do-not-merge/work-in-progress"
	// PR 3: has label "other"
	mockPRs := `[
		{"number": 1, "labels": [{"name": "needs-triage"}], "head": {"repo": {"clone_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "PR 1", "diff_url": "https://github.com/test/repo/pull/1.diff"},
		{"number": 2, "labels": [{"name": "needs-triage"}, {"name": "do-not-merge/work-in-progress"}], "head": {"repo": {"clone_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/2", "title": "PR 2", "diff_url": "https://github.com/test/repo/pull/2.diff"},
		{"number": 3, "labels": [{"name": "other"}], "head": {"repo": {"clone_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/3", "title": "PR 3", "diff_url": "https://github.com/test/repo/pull/3.diff"}
	]`

	// Mock Issues:
	// Issue 10: has label "needs-triage"
	// Issue 11: has labels "needs-triage" and "triage/accepted"
	mockIssues := `[
		{"number": 10, "labels": [{"name": "needs-triage"}], "title": "Issue 10", "html_url": "https://github.com/test/repo/issues/10", "repository_url": "https://api.github.com/repos/test/repo"},
		{"number": 11, "labels": [{"name": "needs-triage"}, {"name": "triage/accepted"}], "title": "Issue 11", "html_url": "https://github.com/test/repo/issues/11", "repository_url": "https://api.github.com/repos/test/repo"}
	]`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(mockPRs))}
				},
				"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(mockIssues))}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"login": "test-user"}`))}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	objName := "test-repowatch-exclude"
	objNamespace := "default"
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 10,
				Labels: [][]string{{"needs-triage"}},
				ExcludeLabels: []string{"do-not-merge/work-in-progress"},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
			},
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 10,
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name:   "triage",
						Labels: []string{"needs-triage"},
						ExcludeLabels: []string{"triage/accepted"},
					},
				},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: objNamespace},
		Data:       map[string][]byte{"pat": []byte("test-pat")},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: objName, Namespace: objNamespace}}
	
	// Reconcile
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())

	// Verify PRs
	// PR 1: included (matches needs-triage, not excluded)
	// PR 2: excluded (matches needs-triage, but also has do-not-merge/work-in-progress)
	// PR 3: excluded (does not match needs-triage)
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(1))

	// Verify Issues
	// Issue 10: included (matches needs-triage, not excluded)
	// Issue 11: excluded (matches needs-triage, but also has triage/accepted)
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["triage"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["triage"][0].Number).To(gomega.Equal(10))

	// Check that sandboxes were created only for PR 1 and Issue 10
	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), sandboxList)).To(gomega.Succeed())
	
	sandboxNames := []string{}
	for _, item := range sandboxList.Items {
		sandboxNames = append(sandboxNames, item.GetName())
	}
	
	g.Expect(sandboxNames).To(gomega.ContainElement(gomega.ContainSubstring("pr-1")))
	g.Expect(sandboxNames).To(gomega.ContainElement(gomega.ContainSubstring("issue-10")))
	g.Expect(sandboxNames).ToNot(gomega.ContainElement(gomega.ContainSubstring("pr-2")))
	g.Expect(sandboxNames).ToNot(gomega.ContainElement(gomega.ContainSubstring("issue-11")))
}

func TestReconciler_ExcludeLabelsOnly(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	mockPRs := `[
		{"number": 1, "labels": [{"name": "needs-triage"}], "head": {"repo": {"clone_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "PR 1", "diff_url": "https://github.com/test/repo/pull/1.diff"},
		{"number": 2, "labels": [{"name": "do-not-merge/work-in-progress"}], "head": {"repo": {"clone_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/2", "title": "PR 2", "diff_url": "https://github.com/test/repo/pull/2.diff"}
	]`

	mockIssues := `[
		{"number": 10, "labels": [{"name": "needs-triage"}], "title": "Issue 10", "html_url": "https://github.com/test/repo/issues/10", "repository_url": "https://api.github.com/repos/test/repo"},
		{"number": 11, "labels": [{"name": "triage/accepted"}], "title": "Issue 11", "html_url": "https://github.com/test/repo/issues/11", "repository_url": "https://api.github.com/repos/test/repo"}
	]`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(mockPRs))}
				},
				"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(mockIssues))}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"login": "test-user"}`))}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	objName := "test-repowatch-exclude-only"
	objNamespace := "default"
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 10,
				// No Labels specified
				ExcludeLabels: []string{"do-not-merge/work-in-progress"},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
			},
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 10,
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name: "all-but-accepted",
						// No Labels specified
						ExcludeLabels: []string{"triage/accepted"},
					},
				},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: objNamespace},
		Data:       map[string][]byte{"pat": []byte("test-pat")},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: objName, Namespace: objNamespace}}
	
	// Reconcile
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())

	// PR 1: included (not excluded)
	// PR 2: excluded
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(1))

	// Issue 10: included (not excluded)
	// Issue 11: excluded
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["all-but-accepted"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["all-but-accepted"][0].Number).To(gomega.Equal(10))
}
