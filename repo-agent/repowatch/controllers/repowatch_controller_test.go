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
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v39/github"
	. "github.com/onsi/gomega"
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

func TestRepoWatchReconciler_Reconcile(t *testing.T) {
	g := NewWithT(t)

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
				"https://api.github.com/repos/test/repo/pulls?state=open": {
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "Test PR"}]`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
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
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(Equal(1))
	g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(Equal(1))

	// Check that a ReviewSandbox was created
	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(Succeed())
	g.Expect(reviewSandboxList.Items).To(HaveLen(1))
}

func TestRepoWatchReconciler_Reconcile_NotFound(t *testing.T) {
	g := NewWithT(t)

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
	g.Expect(err).NotTo(HaveOccurred())
}

func TestRepoWatchReconciler_Reconcile_GitHubSecretNotFound(t *testing.T) {
	g := NewWithT(t)

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
		NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
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
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).To(HaveOccurred())
}

func TestRepoWatchReconciler_Reconcile_InvalidRepoURL(t *testing.T) {
	g := NewWithT(t)

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
				"https://api.github.com/repos/test/repo/pulls?state=open": {
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1"}, "title": "Test PR"}]`)),
				},
				"https://api.github.com/user": {
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			},
		},
	}
	ghClient := github.NewClient(mockHTTPClient)
	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
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
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).To(HaveOccurred())
}

func TestRepoWatchReconciler_Reconcile_Issues(t *testing.T) {
	g := NewWithT(t)

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
				"https://api.github.com/repos/test/repo/pulls?state=open": {
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`[]`)),
				},
				"https://api.github.com/repos/test/repo/issues?state=open": {
					StatusCode: http.StatusOK,
					Body: ioutil.NopCloser(strings.NewReader(`[
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
					Body:       ioutil.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
				},
			}},
	}
	ghClient := github.NewClient(mockHTTPClient)

	r := &RepoWatchReconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
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
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(Succeed())

	llmSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llm-secret",
			Namespace: objNamespace,
		},
		Data: map[string][]byte{
			"apiKey": []byte("test-api-key"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), llmSecret)).To(Succeed())

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(Succeed())
	g.Expect(fetchedRepoWatch.Status.WatchedIssues).To(HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.WatchedIssues["test-handler"][0].Number).To(Equal(10))

	// Check that an IssueSandbox was created
	issueSandboxList := &unstructured.UnstructuredList{}
	issueSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	})
	g.Expect(fakeClient.List(context.Background(), issueSandboxList)).To(Succeed())
	g.Expect(issueSandboxList.Items).To(HaveLen(1))
}

func TestReconcileReviewSandboxes(t *testing.T) {
	g := NewWithT(t)

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
	t.Run("deletes sandbox for closed PR and creates new for open PR", func(t *testing.T) {
		// Re-initialize client for this specific test run to ensure a clean state
		// This is important because the client state is modified by the previous test run
		// and we want to start fresh for each subtest.
		// Also, the reconcileReviewSandboxes function calls createReviewSandboxForPR,
		// which needs a working NewGithubClient.
		// For this test, we don't need a real github client, so we can mock it.
		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, closedPRSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Should contain the closedPRSandbox initially

		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{pr}, sandboxList)
		g.Expect(err).NotTo(HaveOccurred())

		// Check that the sandbox for the closed PR is deleted and a new one for the open PR is created
		sandboxList = &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Should contain only the sandbox for prNumber 1
		g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-pr-1"))
	})

	// Test case 2: Not creating a new sandbox if the maximum number of active sandboxes has been reached.
	t.Run("does not create new sandbox if max active sandboxes reached", func(t *testing.T) {
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
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileReviewSandboxes with the active PR and the new PR
		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{pr, newPR}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activePRSandbox}})
		g.Expect(err).NotTo(HaveOccurred())

		// Check that no new sandbox was created
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Only the activePRSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-pr-1"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(Succeed())
		g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(Equal(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(Equal(prNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Status).To(Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingPRs).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.PendingPRs[0].Number).To(Equal(newPRNumber))
		g.Expect(fetchedRepoWatch.Status.PendingPRs[0].Status).To(Equal("Pending"))
	})

	// Test case 3: Not creating a new sandbox if it already exists.
	t.Run("does not create new sandbox if it already exists", func(t *testing.T) {
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
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileReviewSandboxes with the existing PR
		err := r.reconcileReviewSandboxes(context.Background(), repoWatch, []*github.PullRequest{pr}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*existingPRSandbox}})
		g.Expect(err).NotTo(HaveOccurred())

		// Check that no new sandbox was created and the existing one is still there
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "ReviewSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Only the existingPRSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-pr-1"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(Succeed())
		g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(Equal(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Number).To(Equal(prNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedPRs[0].Status).To(Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingPRs).To(HaveLen(0))
	})
}
func TestNewGithubClient(t *testing.T) {
	g := NewWithT(t)

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
		t.Run(tc.name, func(t *testing.T) {
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
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(githubConfig["pat"]).To(Equal(tc.expectedPAT))
				g.Expect(githubConfig["name"]).To(Equal(tc.expectedName))
				g.Expect(githubConfig["email"]).To(Equal(tc.expectedEmail))
			}
		})
	}
}


func TestReconcileIssueHandlerSandboxes(t *testing.T) {
	g := NewWithT(t)

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
		Number: &issueNumber,
		HTMLURL: github.String("https://github.com/test/repo/issues/1"),
		Title:   github.String("Test Issue"),
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
	t.Run("deletes sandbox for closed issue and creates new for open issue", func(t *testing.T) {
		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, closedIssueSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Should contain the closedIssueSandbox initially

		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue}, sandboxList)
		g.Expect(err).NotTo(HaveOccurred())

		// Check that the sandbox for the closed issue is deleted and a new one for the open issue is created
		sandboxList = &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Should contain only the sandbox for issueNumber 1
		g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-issue-1-testhandler"))
	})

	// Test case 2: Not creating a new sandbox if the maximum number of active sandboxes has been reached.
	t.Run("does not create new sandbox if max active sandboxes reached", func(t *testing.T) {
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
			Number: &newIssueNumber,
			HTMLURL: github.String("https://github.com/test/repo/issues/3"),
			Title:   github.String("New Pending Issue"),
			RepositoryURL: github.String("https://api.github.com/repos/test/repo"),
		}

		r := &RepoWatchReconciler{
			Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, activeIssueSandbox).WithStatusSubresource(repoWatch).Build(),
			Scheme: s,
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileIssueHandlerSandboxes with the active issue and the new issue
		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue, newIssue}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*activeIssueSandbox}})
		g.Expect(err).NotTo(HaveOccurred())

		// Check that no new sandbox was created
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		        g.Expect(sandboxList.Items).To(HaveLen(1)) // Only the activeIssueSandbox should exist
				g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-issue-1-testhandler"))
		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(Succeed())
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName]).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Number).To(Equal(issueNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Status).To(Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0].Number).To(Equal(newIssueNumber))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName][0].Status).To(Equal("Pending"))
	})

	// Test case 3: Not creating a new sandbox if it already exists.
	t.Run("does not create new sandbox if it already exists", func(t *testing.T) {
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
			NewGithubClient: func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return &github.Client{}, map[string]string{}, nil
			},
		}

		// Call reconcileIssueHandlerSandboxes with the existing issue
		err := r.reconcileIssueHandlerSandboxes(context.Background(), currentUser, handler, repoWatch, []*github.Issue{issue}, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*existingIssueSandbox}})
		g.Expect(err).NotTo(HaveOccurred())

		// Check that no new sandbox was created and the existing one is still there
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "custom.agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "IssueSandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(Succeed())
		g.Expect(sandboxList.Items).To(HaveLen(1)) // Only the existingIssueSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(Equal("repo-issue-1-testhandler"))

		// Check that the RepoWatch status is updated correctly
		fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
		g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(Succeed())
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName]).To(HaveLen(1))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Number).To(Equal(issueNumber))
		g.Expect(fetchedRepoWatch.Status.WatchedIssues[handlerName][0].Status).To(Equal("Active"))
		g.Expect(fetchedRepoWatch.Status.PendingIssues[handlerName]).To(HaveLen(0))
	})
}
