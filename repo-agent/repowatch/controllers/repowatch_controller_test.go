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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/repowatch/api/v1alpha1"
)

type mockRoundTripper struct {
	responses map[string]*http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, ok := m.responses[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	resp.Request = req
	return resp, nil
}

// TestRepoWatchReconciler_Reconcile is a crucial test that covers the main success path of the RepoWatchReconciler.
// It simulates a RepoWatch resource being created, and it verifies that the reconciler correctly:
// - Fetches the RepoWatch resource.
// - Creates a GitHub client.
// - Lists open pull requests.
// - Creates a ReviewSandbox for an open pull request.
// - Updates the RepoWatch status with the correct information about the active sandbox and watched PR.
func TestRepoWatchReconciler_Reconcile(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]*http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "Test PR", "diff_url": "https://github.com/test/repo/pull/1.diff"}]`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// 4. Define the Reconcile request
	objName := "test-repowatch"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	// 5. Create the object your reconciler will act upon
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 1,
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(1))
	g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(gomega.Equal(1))

	// Check that a ReviewSandbox was created
	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(gomega.Succeed())
	g.Expect(reviewSandboxList.Items).To(gomega.HaveLen(1))
}

// TestRepoWatchReconciler_ReconcileIssues focuses on the success path for handling GitHub issues.
// It ensures that the reconciler can:
// - List open issues.
// - Create an IssueSandbox for an open issue based on the IssueHandler configuration.
// - Updates the RepoWatch status with information about the watched issue.
func TestRepoWatchReconciler_ReconcileIssues(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]*http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
				},
				"https://api.github.com/repos/test/repo/issues?state=open": {
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`[
												{
													"number": 10,
													"title": "Test Issue",
													"html_url": "https://github.com/test/repo/issues/10",
													"repository_url": "https://api.github.com/repos/test/repo"
												}
											]`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			}},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// 4. Define the Reconcile request
	objName := "test-repowatch-issues"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	// 5. Create the object your reconciler will act upon
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			IssueHandlers: []reviewv1alpha1.IssueHandlerSpec{
				{
					Name:               "test-handler",
					MaxActiveSandboxes: 1,
					LLM: reviewv1alpha1.LLMConfig{
						Provider:        "gemini-cli",
						APIKeySecretRef: "llm-secret",
						Prompt:          "You are an expert kubernetes developer who is helping with bug triage. Please look at the issue {{.Number}} linked at {{.HTMLURL}} and provide a triage summary. Please suggest possible causes and solutions.",
					},
				},
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	llmSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llm-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"apiKey": []byte("test-api-key"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), llmSecret)).To(gomega.Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.WatchedIssues).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.WatchedIssues["test-handler"][0].Number).To(gomega.Equal(10))

	// Check that an IssueSandbox was created
	issueSandboxList := &unstructured.UnstructuredList{}
	issueSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	})
	g.Expect(fakeClient.List(context.Background(), issueSandboxList)).To(gomega.Succeed())
	g.Expect(issueSandboxList.Items).To(gomega.HaveLen(1))
}

// TestReconcileReviewSandboxes contains several sub-tests that are highly relevant for success
// conditions related to ReviewSandbox management:
//   - "deletes sandbox for closed PR and creates new for open PR": This test case ensures that the
//     controller correctly cleans up resources for pull requests that are no longer open and
//     creates new resources for new open pull requests. This is a key part of the reconciliation logic.
//   - "does not create new sandbox if max active sandboxes reached": This test verifies that the
//     controller respects the MaxActiveSandboxes limit, which is an important success condition
//     for resource management.
//   - "does not create new sandbox if it already exists": This test ensures that the controller
//     does not create duplicate resources, which is a fundamental aspect of idempotent reconciliation.
func TestReconcileReviewSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	prNumber := 1
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
				MaxActiveSandboxes: 1,
			},
		},
	}

	// PR that is open
	pr := &github.PullRequest{
		Number: &prNumber,
		Head: &github.PullRequestBranch{
			Repo: &github.Repository{
				CloneURL: github.String(repoURL),
			},
			Ref: github.String("main"),
		},
		HTMLURL: github.String("https://github.com/test/repo/pull/1"),
		Title:   github.String("Test PR"),
		DiffURL: github.String("https://github.com/test/repo/pull/1.diff"),
	}

	// Sandbox for a PR that is now closed
	closedPRSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      "repo-pr-2",
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
		},
	}

	// Test case 1: Deleting a sandbox for a closed PR and creating a new one for an open PR.
	t.Run("deletes sandbox for closed PR and creates new for open PR", func(_ *testing.T) {
		// Re-initialize client for this specific test run to ensure a clean state
		// This is important because the client state is modified by the previous test run
		// and we want to start fresh for each subtest.
		// Also, the reconcileReviewSandboxes function calls createReviewSandboxForPR,
		// which needs a working NewGithubClient.
		// For this test, we don't need a real github client, so we can mock it.
		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, closedPRSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Should contain the closedPRSandbox initially

		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr}, sandboxList)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that the sandbox for the closed PR is deleted and a new one for the open PR is created
		sandboxList = &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Should contain only the sandbox for prNumber 1
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-pr-1"))
	})

	// Test case 2: Not creating a new sandbox if the maximum number of active sandboxes has been reached.
	t.Run("does not create new sandbox if max active sandboxes reached", func(_ *testing.T) {
		// Set MaxActiveSandboxes to 1
		repoWatch.Spec.Review.MaxActiveSandboxes = 1

		// Create an existing active sandbox for prNumber 1
		activePRSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "ReviewSandbox",
				"metadata": map[string]interface{}{
					"name":      "repo-pr-1",
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
					"replicas": int64(1), // Mark as active
				},
			},
		}

		// Create a new PR that should become pending
		newPRNumber := 3
		newPR := &github.PullRequest{
			Number: &newPRNumber,
			Head: &github.PullRequestBranch{
				Repo: &github.Repository{
					CloneURL: github.String(repoURL),
				},
				Ref: github.String("main"),
			},
			HTMLURL: github.String("https://github.com/test/repo/pull/3"),
			Title:   github.String("New Pending PR"),
		}

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activePRSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileReviewSandboxes with the active PR and the new PR
		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr, newPR}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activePRSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that no new sandbox was created
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Only the activePRSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-pr-1"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())
		g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(gomega.Equal(prNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Status).To(gomega.Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.PendingPRs[0].Number).To(gomega.Equal(newPRNumber))
		g.Expect(fetchedRepoWatch.Status.PendingPRs[0].Status).To(gomega.Equal("Pending"))
	})

	// Test case 3: Not creating a new sandbox if it already exists.
	t.Run("does not create new sandbox if it already exists", func(_ *testing.T) {
		// Set MaxActiveSandboxes to 1
		repoWatch.Spec.Review.MaxActiveSandboxes = 1

		// Existing sandbox for prNumber 1
		existingPRSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "ReviewSandbox",
				"metadata": map[string]interface{}{
					"name":      "repo-pr-1",
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

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingPRSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileReviewSandboxes with the existing PR
		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*existingPRSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that no new sandbox was created and the existing one is still there
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Only the existingPRSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-pr-1"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())
		g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(gomega.Equal(prNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Status).To(gomega.Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(0))
	})

	// Test case 4: Scales down sandbox if age exceeds ReviewShutdownAfterMinutes
	t.Run("scales down sandbox if age exceeds ReviewShutdownAfterMinutes", func(_ *testing.T) {
		// Set ReviewShutdownAfterMinutes
		repoWatch.Spec.Review.ReviewShutdownAfterMinutes = 60
		repoWatch.Spec.Review.MaxActiveSandboxes = 10

		// Create a sandbox that is older than 60 minutes
		oldCreationTime := time.Now().Add(-61 * time.Minute)

		oldSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "ReviewSandbox",
				"metadata": map[string]interface{}{
					"name":              "repo-pr-1",
					"namespace":         "default",
					"creationTimestamp": oldCreationTime.Format(time.RFC3339),
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

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, oldSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileReviewSandboxes with the PR corresponding to the sandbox
		// We need the PR to be present so it doesn't try to delete the sandbox because the PR is closed
		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*oldSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Fetch the updated sandbox
		updatedSandbox := &unstructured.Unstructured{}
		updatedSandbox.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: "repo-pr-1", Namespace: "default"}, updatedSandbox)).To(gomega.Succeed())

		// Check replicas
		replicas, found, err := unstructured.NestedInt64(updatedSandbox.Object, "spec", "replicas")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(found).To(gomega.BeTrue())
		g.Expect(replicas).To(gomega.Equal(int64(0)))
	})
}

// TestReconcileIssueHandlerSandboxes mirrors the structure of TestReconcileReviewSandboxes but for issues and
// IssueSandboxes. Its sub-tests are also critical for verifying the success conditions of issue handling:
//   - "deletes sandbox for closed issue and creates new for open issue": Similar to the PR counterpart, this
//     test ensures that sandboxes for closed issues are cleaned up and new ones are created for open issues.
//   - "does not create new sandbox if max active sandboxes reached": This verifies that the
//     MaxActiveSandboxes limit is respected for issue handlers.
//   - "does not create new sandbox if it already exists": This ensures idempotency for issue sandbox creation.
func TestReconcileIssueHandlerSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	issueNumber := 1
	repoURL := "https://github.com/test/repo"
	handlerName := "testhandler"

	currentUser := &github.User{
		Login: github.String("test-user"),
	}

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
					MaxActiveSandboxes: 1,
				},
			},
		},
	}
	handler := repoWatch.Spec.IssueHandlers[0]

	// Issue that is open
	issue := &github.Issue{
		Number:        &issueNumber,
		HTMLURL:       github.String("https://github.com/test/repo/issues/1"),
		Title:         github.String("Test Issue"),
		RepositoryURL: github.String("https://api.github.com/repos/test/repo"),
	}

	// Sandbox for an issue that is now closed
	closedIssueSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      "repo-issue-2-testhandler",
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
		},
	}

	// Test case 1: Deleting a sandbox for a closed issue and creating a new one for an open issue.
	t.Run("deletes sandbox for closed issue and creates new for open issue", func(_ *testing.T) {
		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, closedIssueSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Should contain the closedIssueSandbox initially

		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue}, sandboxList)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that the sandbox for the closed issue is deleted and a new one for the open issue is created
		sandboxList = &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Should contain only the sandbox for issueNumber 1
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-issue-1-testhandler"))
	})

	// Test case 2: Not creating a new sandbox if the maximum number of active sandboxes has been reached.
	t.Run("does not create new sandbox if max active sandboxes reached", func(_ *testing.T) {
		// Set MaxActiveSandboxes to 1
		repoWatch.Spec.IssueHandlers[0].MaxActiveSandboxes = 1

		// Create an existing active sandbox for issueNumber 1
		activeIssueSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "IssueSandbox",
				"metadata": map[string]interface{}{
					"name":      "repo-issue-1-testhandler",
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
					"replicas": int64(1), // Mark as active
				},
			},
		}

		// Create a new issue that should become pending
		newIssueNumber := 3
		newIssue := &github.Issue{
			Number:        &newIssueNumber,
			HTMLURL:       github.String("https://github.com/test/repo/issues/3"),
			Title:         github.String("New Pending Issue"),
			RepositoryURL: github.String("https://api.github.com/repos/test/repo"),
		}

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeIssueSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileIssueHandlerSandboxes with the active issue and the new issue
		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue, newIssue}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activeIssueSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that no new sandbox was created
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Only the activeIssueSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-issue-1-testhandler"))
		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName]).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Number).To(gomega.Equal(issueNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Status).To(gomega.Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0].Number).To(gomega.Equal(newIssueNumber))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0].Status).To(gomega.Equal("Pending"))
	})

	// Test case 3: Not creating a new sandbox if it already exists.
	t.Run("does not create new sandbox if it already exists", func(_ *testing.T) {
		// Set MaxActiveSandboxes to 1
		repoWatch.Spec.IssueHandlers[0].MaxActiveSandboxes = 1

		// Existing sandbox for issueNumber 1
		existingIssueSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "IssueSandbox",
				"metadata": map[string]interface{}{
					"name":      "repo-issue-1-testhandler",
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

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingIssueSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileIssueHandlerSandboxes with the existing issue
		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*existingIssueSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that no new sandbox was created and the existing one is still there
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Only the existingIssueSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("repo-issue-1-testhandler"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName]).To(gomega.HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Number).To(gomega.Equal(issueNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Status).To(gomega.Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(gomega.HaveLen(0))
	})

	// Test case 4: Scales down sandbox if age exceeds IssueShutdownAfterMinutes
	t.Run("scales down sandbox if age exceeds IssueShutdownAfterMinutes", func(_ *testing.T) {
		// Set IssueShutdownAfterMinutes
		repoWatch.Spec.IssueHandlers[0].IssueShutdownAfterMinutes = 60
		repoWatch.Spec.IssueHandlers[0].MaxActiveSandboxes = 10
		// Refresh handler copy
		handler = repoWatch.Spec.IssueHandlers[0]

		// Create a sandbox that is older than 60 minutes
		oldCreationTime := time.Now().Add(-61 * time.Minute)

		oldSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "IssueSandbox",
				"metadata": map[string]interface{}{
					"name":              "repo-issue-1-testhandler",
					"namespace":         "default",
					"creationTimestamp": oldCreationTime.Format(time.RFC3339),
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

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, oldSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileIssueHandlerSandboxes with the issue corresponding to the sandbox
		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*oldSandbox}})
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Fetch the updated sandbox
		updatedSandbox := &unstructured.Unstructured{}
		updatedSandbox.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: "repo-issue-1-testhandler", Namespace: "default"}, updatedSandbox)).To(gomega.Succeed())

		// Check replicas
		replicas, found, err := unstructured.NestedInt64(updatedSandbox.Object, "spec", "replicas")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(found).To(gomega.BeTrue())
		g.Expect(replicas).To(gomega.Equal(int64(0)))
	})
}

func TestRepoWatchReconciler_Reconcile_NotFound(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).Build()

	// 3. Create your Reconciler instance
	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
	}

	// 4. Define the Reconcile request
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repowatch",
			Namespace: "default",
		},
	}

	// 5. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestRepoWatchReconciler_Reconcile_GitHubSecretNotFound(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			// In this test, we expect the secret to be missing, so return an error.
			return nil, nil, errors.New("github secret not found")
		},
	}

	// 4. Define the Reconcile request
	objName := "test-repowatch"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	// 5. Create the object your reconciler will act upon
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).To(gomega.HaveOccurred())
}

func TestRepoWatchReconciler_Reconcile_InvalidRepoURL(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]*http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1"}, "title": "Test PR"}]`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)
	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// 4. Define the Reconcile request
	objName := "test-repowatch"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	// 5. Create the object your reconciler will act upon
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "invalid-repo-url", // Invalid URL
			GithubSecretName: "github-secret",
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).To(gomega.HaveOccurred())
}

func TestNewGithubClient(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Create test cases
	testCases := []struct {
		name          string
		secret        *corev1.Secret
		expectErr     bool
		expectedPAT   string
		expectedName  string
		expectedEmail string
	}{
		{
			name: "valid secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"pat":   []byte("test-pat"),
					"name":  []byte("test-user"),
					"email": []byte("test-email"),
				},
			},
			expectErr:     false,
			expectedPAT:   "test-pat",
			expectedName:  "test-user",
			expectedEmail: "test-email",
		},
		{
			name: "secret not found",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-secret-not-found",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"pat": []byte("test-pat"),
				},
			},
			expectErr: true,
		},
		{
			name: "pat not found in secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{},
			},
			expectErr: true,
		},
		{
			name: "name and email are optional",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"pat": []byte("test-pat"),
				},
			},
			expectErr:     false,
			expectedPAT:   "test-pat",
			expectedName:  "",
			expectedEmail: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(_ *testing.T) {
			// 3. Initialize the fake client with any initial objects
			var fakeClient client.Client
			if tc.name == "secret not found" {
				fakeClient = clientfake.NewClientBuilder().WithScheme(s).Build()
			} else {
				fakeClient = clientfake.NewClientBuilder().WithScheme(s).WithObjects(tc.secret).Build()
			}

			// 4. Create a RepoWatch object
			repoWatch := &reviewv1alpha1.RepoWatch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repowatch",
					Namespace: "default",
				},
				Spec: reviewv1alpha1.RepoWatchSpec{
					RepoURL:          "https://github.com/test/repo",
					GithubSecretName: "github-secret",
				},
			}

			// 5. Call NewGithubClient
			_, githubConfig, err := NewGithubClient(context.Background(), fakeClient, repoWatch)

			// 6. Assert expected outcomes
			if tc.expectErr {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(githubConfig["pat"]).To(gomega.Equal(tc.expectedPAT))
				g.Expect(githubConfig["name"]).To(gomega.Equal(tc.expectedName))
				g.Expect(githubConfig["email"]).To(gomega.Equal(tc.expectedEmail))
			}
		})
	}
}

// TestNewGithubClient_WaitingForLogin verifies that NewGithubClient returns a specific error
// when the PAT is missing or empty, but OAuth credentials are configured.
func TestNewGithubClient_WaitingForLogin(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).Build()

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"pat": []byte(""), // Empty PAT
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	// Set env vars for the test
	t.Setenv("GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "test-client-secret")

	_, _, err := NewGithubClient(context.Background(), fakeClient, repoWatch)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("waiting for user login"))
}

// TestPersistingTokenSource verifies that PersistingTokenSource updates the Kubernetes secret
// when a new token is obtained.
func TestPersistingTokenSource(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	secretName := "github-secret"
	namespace := "default"
	oldToken := "old-token"
	newToken := "new-token"
	newRefreshToken := "new-refresh-token"

	// 1. Create initial secret with old token
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"pat": []byte(oldToken),
		},
	}
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()

	// 2. Create a mock TokenSource that returns the NEW token
	mockSource := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken:  newToken,
		RefreshToken: newRefreshToken,
		Expiry:       time.Now().Add(1 * time.Hour),
	})

	// 3. Create PersistingTokenSource
	pts := &PersistingTokenSource{
		Source:     mockSource,
		K8sClient:  fakeClient,
		SecretName: secretName,
		Namespace:  namespace,
	}

	// 4. Call Token() to trigger the refresh and persist logic
	token, err := pts.Token()
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(token.AccessToken).To(gomega.Equal(newToken))

	// 5. Wait for the async goroutine to update the secret
	g.Eventually(func() string {
		updatedSecret := &corev1.Secret{}
		err := fakeClient.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: namespace}, updatedSecret)
		if err != nil {
			return ""
		}
		return string(updatedSecret.Data["pat"])
	}, 2*time.Second, 100*time.Millisecond).Should(gomega.Equal(newToken))

	// 6. Verify refresh token was also updated
	updatedSecret := &corev1.Secret{}
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: namespace}, updatedSecret)).To(gomega.Succeed())
	g.Expect(string(updatedSecret.Data["refresh_token"])).To(gomega.Equal(newRefreshToken))
}

// TestRepoWatchReconciler_Reconcile_ExplicitAndListedPRs verifies that the reconciler correctly handles
// both explicitly requested PRs (via Spec.Review.PullRequests) and listed open PRs.
// It ensures that sandboxes are created for both types.
func TestRepoWatchReconciler_Reconcile_ExplicitAndListedPRs(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]*http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "Test PR 1", "diff_url": "https://github.com/test/repo/pull/1.diff"}]`)),
				},
				"https://api.github.com/repos/test/repo/pulls/42": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"number": 42, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "feature"}, "html_url": "https://github.com/test/repo/pull/42", "title": "Explicit PR 42", "diff_url": "https://github.com/test/repo/pull/42.diff"}`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// 4. Define the Reconcile request
	objName := "test-repowatch-explicit"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	// 5. Create the object your reconciler will act upon
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
				PullRequests:       []int{42}, // Explicit PR
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(2)) // Both 1 and 42
	g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(gomega.HaveLen(2))

	// Verify PR 1
	foundPR1 := false
	for _, pr := range fetchedRepoWatch.Status.WatchedPRs {
		if pr.Number == 1 {
			foundPR1 = true
			break
		}
	}
	g.Expect(foundPR1).To(gomega.BeTrue())

	// Verify PR 42
	foundPR42 := false
	for _, pr := range fetchedRepoWatch.Status.WatchedPRs {
		if pr.Number == 42 {
			foundPR42 = true
			break
		}
	}
	g.Expect(foundPR42).To(gomega.BeTrue())

	// Check that ReviewSandboxes were created
	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(gomega.Succeed())
	g.Expect(reviewSandboxList.Items).To(gomega.HaveLen(2))
}

func TestRepoWatchReconciler_Reconcile_FilteredAndSortedPRs(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// Mock Response
	// PR 4: #4, Label: bug, Assignee: test-user
	// PR 3: #3, Label: enhancement, Assignee: test-user
	// PR 2: #2, Label: bug, Assignee: other-user
	// PR 1: #1, Label: bug, Assignee: test-user

	responseBody := `[
        {"number": 4, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "bug"}], "assignees": [{"login": "test-user"}]},
        {"number": 3, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "enhancement"}], "assignees": [{"login": "test-user"}]},
        {"number": 2, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "bug"}], "assignees": [{"login": "other-user"}]},
        {"number": 1, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "bug"}], "assignees": [{"login": "test-user"}]}
    ]`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]*http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(responseBody)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	objName := "test-repowatch-filtered"
	objNamespace := "default"
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      objName,
			Namespace: objNamespace,
		},
	}

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: objNamespace,
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes:   2,
				Labels:               [][]string{{"bug"}},
				PreferAssignedToSelf: true,
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(2))
	// Expected order of processing: PR 4, PR 1. PR 2 is pending. PR 3 is filtered.
	g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(gomega.HaveLen(2))

	// Because WatchedPRs are appended as sandboxes are created, and we passed a sorted list to reconcileReviewSandboxes,
	// they should be in order of the passed list (PR 4, PR 1, PR 2).
	// However, createReviewSandboxForPR is called sequentially.
	// So WatchedPRs should reflect that order.

	g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(gomega.Equal(4))
	g.Expect(fetchedRepoWatch.Status.WatchedPRs[1].Number).To(gomega.Equal(1))

	g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingPRs[0].Number).To(gomega.Equal(2))
}
