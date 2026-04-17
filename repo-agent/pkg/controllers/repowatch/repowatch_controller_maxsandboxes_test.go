/*
Copyright 2025.

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

// TestReconcileReviewSandboxes_MaxSandboxes verifies that the MaxSandboxes limit is respected.
func TestReconcileReviewSandboxes_MaxSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	repoURL := "https://github.com/test/repo"

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 10,
				MaxSandboxes:       2, // Total limit (active + inactive)
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
			},
		},
	}

	// 1. Existing Active Sandbox (PR 1)
	activeSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-pr-1",
				"namespace": "default",
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "review.gemini.google.com/v1alpha1",
						"kind":       "RepoWatch",
						"name":       "test-repowatch",
						"uid":        "test-uid",
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	// 2. Existing Inactive Sandbox (PR 2) - scaled down
	inactiveSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-pr-2",
				"namespace": "default",
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "review.gemini.google.com/v1alpha1",
						"kind":       "RepoWatch",
						"name":       "test-repowatch",
						"uid":        "test-uid",
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	// 3. New PR (PR 3) - should be blocked by MaxSandboxes
	pr3Number := 3
	pr3 := &github.PullRequest{
		Number: &pr3Number,
		Head: &github.PullRequestBranch{
			Repo: &github.Repository{CloneURL: github.String(repoURL)},
			Ref:  github.String("main"),
		},
		HTMLURL: github.String("https://github.com/test/repo/pull/3"),
		Title:   github.String("Test PR 3"),
	}

	// PRs 1 and 2 are needed so sandboxes aren't deleted
	pr1Number := 1
	pr1 := &github.PullRequest{Number: &pr1Number}
	pr2Number := 2
	pr2 := &github.PullRequest{Number: &pr2Number}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeSandbox, inactiveSandbox).WithStatusSubresource(repoWatch).Build(),
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return &github.Client{}, map[string]string{}, nil
		},
	}

	// Call reconcile
	watchedPRs, pendingPRs, activeSandboxes, err := r.reconcileReviewSandboxesInternal(context.Background(), &github.User{Login: github.String("test-user")}, repoWatch, &github.Client{}, "owner", "repo", []*github.PullRequest{}, []*github.PullRequest{pr1, pr2, pr3}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activeSandbox, *inactiveSandbox}}, map[string]*corev1.Pod{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	repoWatch.Status.ReviewSandboxes = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs
	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	g.Expect(r.Status().Update(context.Background(), repoWatch)).To(gomega.Succeed())

	// Verify results
	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	g.Expect(sandboxList.Items).To(gomega.HaveLen(2)) // Should still be 2

	// Verify Status
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	// PR 3 should be pending
	g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingPRs[0]).To(gomega.Equal(3))
}

// TestReconcileIssueHandlerSandboxes_MaxSandboxes verifies that the MaxSandboxes limit is respected for Issues.
func TestReconcileIssueHandlerSandboxes_MaxSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	repoURL := "https://github.com/test/repo"
	handlerName := "testhandler"

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 10,
				MaxSandboxes:       2,
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name: handlerName,
					},
				},
			},
		},
	}

	// 1. Existing Active Sandbox (Issue 1)
	activeSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-issue-1",
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "issue",
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "review.gemini.google.com/v1alpha1",
						"kind":       "RepoWatch",
						"name":       "test-repowatch",
						"uid":        "test-uid",
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	// 2. Existing Inactive Sandbox (Issue 2)
	inactiveSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-issue-2",
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "issue",
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "review.gemini.google.com/v1alpha1",
						"kind":       "RepoWatch",
						"name":       "test-repowatch",
						"uid":        "test-uid",
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[]`)),
					}
				},
				"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
					// Return 3 issues
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`[
												{"number": 1, "html_url": "https://github.com/test/repo/issues/1", "title": "Issue 1", "repository_url": "https://api.github.com/repos/test/repo"},
												{"number": 2, "html_url": "https://github.com/test/repo/issues/2", "title": "Issue 2", "repository_url": "https://api.github.com/repos/test/repo"},
												{"number": 3, "html_url": "https://github.com/test/repo/issues/3", "title": "Issue 3", "repository_url": "https://api.github.com/repos/test/repo"}
											]`)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
			}},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeSandbox, inactiveSandbox).WithStatusSubresource(repoWatch).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// Create Github Secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	// Call Reconcile
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}}
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	g.Expect(sandboxList.Items).To(gomega.HaveLen(2)) // MaxSandboxes = 2, so Issue 3 should not have a sandbox

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	// Check PendingIssues
	g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0]).To(gomega.Equal(3))
}

// TestReconcileDevSandboxes_MaxSandboxes verifies that the MaxSandboxes limit is respected for Dev sandboxes.
func TestReconcileDevSandboxes_MaxSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			Dev: reviewv1alpha1.DevSpec{
				MaxActiveSandboxes: 1,
				MaxSandboxes:       1,
			},
		},
	}

	// 1. Existing Dev Sandbox (feature-1)
	existingSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "feature-1-dev",
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "dev",
				},
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/branch": "feature-1",
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion":         "review.gemini.google.com/v1alpha1",
						"kind":               "RepoWatch",
						"name":               "test-repowatch",
						"uid":                "test-uid",
						"controller":         true,
						"blockOwnerDeletion": true,
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingSandbox).WithStatusSubresource(repoWatch).Build(),
		Scheme: s,
	}

	// Both feature-1 and feature-2 are candidate branches
	branches := []*github.Branch{
		{Name: github.String("feature-1")},
		{Name: github.String("feature-2")},
	}

	watched, pending, err := r.reconcileDevSandboxesInternal(context.Background(), &github.User{Login: github.String("test-user")}, repoWatch, branches, "test-owner", "test-repo", nil)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// feature-1 should be found
	foundExisting := false
	for _, ws := range watched {
		if ws.SandboxName == "feature-1-dev" {
			foundExisting = true
		}
	}
	g.Expect(foundExisting).To(gomega.BeTrue())

	// feature-2 should be pending
	g.Expect(pending).To(gomega.ContainElement("feature-2"))
	g.Expect(watched).To(gomega.HaveLen(1))
}
