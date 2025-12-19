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

package controllers

import (
	"context"
	"testing"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/repowatch/api/v1alpha1"
)

// TestReconcileReviewSandboxes_MaxSandboxes verifies that the MaxSandboxes limit is respected.
func TestReconcileReviewSandboxes_MaxSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

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
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
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
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
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

	r := &RepoWatchReconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeSandbox, inactiveSandbox).WithStatusSubresource(repoWatch).Build(),
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return &github.Client{}, map[string]string{}, nil
		},
	}

	// Call reconcile
	watchedPRs, pendingPRs, activeSandboxes := r.reconcileReviewSandboxesInternal(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr1, pr2, pr3}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activeSandbox, *inactiveSandbox}})
	repoWatch.Status.ReviewSandboxes = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs
	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	g.Expect(r.Status().Update(context.Background(), repoWatch)).To(gomega.Succeed())

	// Verify results
	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
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

	repoURL := "https://github.com/test/repo"
	handlerName := "testhandler"

	currentUser := &github.User{Login: github.String("test-user")}

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			IssueHandlers: []reviewv1alpha1.IssueHandlerSpec{
				{
					Name:               handlerName,
					MaxActiveSandboxes: 10,
					MaxSandboxes:       2,
					LLM: reviewv1alpha1.LLMConfig{
						APIKeySecretRef: "llm-secret",
					},
				},
			},
		},
	}
	handler := repoWatch.Spec.IssueHandlers[0]

	// 1. Existing Active Sandbox (Issue 1)
	activeSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-issue-1-testhandler",
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

	// 2. Existing Inactive Sandbox (Issue 2)
	inactiveSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-issue-2-testhandler",
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

	// 3. New Issue (Issue 3) - should be blocked
	issue3Number := 3
	issue3 := &github.Issue{
		Number:        &issue3Number,
		HTMLURL:       github.String("https://github.com/test/repo/issues/3"),
		Title:         github.String("Test Issue 3"),
		RepositoryURL: github.String("https://api.github.com/repos/test/repo"),
	}

	issue1Number := 1
	issue1 := &github.Issue{Number: &issue1Number}
	issue2Number := 2
	issue2 := &github.Issue{Number: &issue2Number}

	r := &RepoWatchReconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeSandbox, inactiveSandbox).WithStatusSubresource(repoWatch).Build(),
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return &github.Client{}, map[string]string{}, nil
		},
	}

	err := r.reconcileIssueHandlerSandboxesInternal(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue1, issue2, issue3}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activeSandbox, *inactiveSandbox}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	g.Expect(sandboxList.Items).To(gomega.HaveLen(2))

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0]).To(gomega.Equal(3))
}
