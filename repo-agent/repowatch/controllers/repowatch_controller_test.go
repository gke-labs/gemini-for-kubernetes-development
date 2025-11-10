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
