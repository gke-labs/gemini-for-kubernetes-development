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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
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
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type mockRoundTripper struct {
	responses map[string]func() *http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	respFunc, ok := m.responses[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	resp := respFunc()
	resp.Request = req
	return resp, nil
}

// TestReconciler_Reconcile is a crucial test that covers the main success path of the Reconciler.
// It simulates a RepoWatch resource being created, and it verifies that the reconciler correctly:
// - Fetches the RepoWatch resource.
// - Creates a GitHub client.
// - Lists open pull requests.
// - Creates a ReviewSandbox for an open pull request.
// - Updates the RepoWatch status with the correct information about the active sandbox and watched PR.
func TestReconciler_Reconcile(t *testing.T) {
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
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "Test PR", "diff_url": "https://github.com/test/repo/pull/1.diff"}]`)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	r := &Reconciler{
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
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
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

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(1))

	// Check that a ReviewSandbox was created
	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(gomega.Succeed())
	g.Expect(reviewSandboxList.Items).To(gomega.HaveLen(1))
	// Check that the apiKeySecretName is set correctly in the volume
	volumes, found, err := unstructured.NestedSlice(reviewSandboxList.Items[0].Object, "spec", "podTemplate", "spec", "volumes")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())

	foundSecret := false
	for _, v := range volumes {
		vol := v.(map[string]interface{})
		if vol["name"] == "tokens-secret" {
			secret := vol["secret"].(map[string]interface{})
			if secret["secretName"] == "llm-secret" {
				foundSecret = true
				break
			}
		}
	}
	g.Expect(foundSecret).To(gomega.BeTrue())

	// Check environment variables
	containers, found, err := unstructured.NestedSlice(reviewSandboxList.Items[0].Object, "spec", "podTemplate", "spec", "containers")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())
	g.Expect(containers).To(gomega.HaveLen(1))

	container := containers[0].(map[string]interface{})
	env, found, err := unstructured.NestedSlice(container, "env")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())

	expectedEnv := map[string]string{
		"GOCACHE":    sandbox.GoCachePath,
		"GOMODCACHE": sandbox.GoModCachePath,
		"TMPDIR":     sandbox.TmpDirPath,
		"GOTMPDIR":   sandbox.TmpDirPath,
	}

	for name, value := range expectedEnv {
		found := false
		for _, e := range env {
			envVar := e.(map[string]interface{})
			if envVar["name"] == name {
				found = true
				g.Expect(envVar["value"]).To(gomega.Equal(value))
				break
			}
		}
		g.Expect(found).To(gomega.BeTrue(), fmt.Sprintf("%s env var not found", name))
	}
}

// TestReconciler_ReconcileIssues focuses on the success path for handling GitHub issues.
// It ensures that the reconciler can:
// - List open issues.
// - Create an IssueSandbox for an open issue based on the IssueHandler configuration.
// - Updates the RepoWatch status with information about the watched issue.
func TestReconciler_ReconcileIssues(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
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
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`[
												{
													"number": 10,
													"title": "Test Issue",
													"html_url": "https://github.com/test/repo/issues/10",
													"repository_url": "https://api.github.com/repos/test/repo"
												}
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

	r := &Reconciler{
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
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 1,
				LLM: reviewv1alpha1.LLMConfig{
					Provider:        "gemini-cli",
					APIKeySecretRef: "llm-secret",
				},
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name:   "test-handler",
						Prompt: "You are an expert kubernetes developer who is helping with bug triage. Please look at the issue {{.Number}} linked at {{.HTMLURL}} and provide a triage summary. Please suggest possible causes and solutions.",
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
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["test-handler"][0].Number).To(gomega.Equal(10))

	// Check that an IssueSandbox was created
	issueSandboxList := &unstructured.UnstructuredList{}
	issueSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), issueSandboxList)).To(gomega.Succeed())
	g.Expect(issueSandboxList.Items).To(gomega.HaveLen(1))
	// Check that the apiKeySecretName is set correctly
	// Note: apiKeySecretName is not in IssueSandbox.Spec.LLM anymore with new controller logic?
	// Actually in createIssueSandbox, I set:
	// "apiKeySecretName": "", //
	// Wait, createIssueSandbox logic I wrote:
	// "llm": map[string]interface{}{ "prompt": "", "apiKeySecretName": "" }
	// So it won't be set on the Sandbox. The Task has the LLM config.
	// So I should check if Task is created.
	// But `reconcileIssues` creates tasks using `ensureIssueTask`.
	// `ensureIssueTask` creates a SandboxTask.
	// So I should verify SandboxTask creation.

	task := &sandboxtaskv1alpha1.SandboxTask{}
	taskName := fmt.Sprintf("%s-issue-10-test-handler", repoWatch.Name)
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: taskName, Namespace: objNamespace}, task)).To(gomega.Succeed())
}

// TestReconcileIssueHandlerSandboxes verifies issue sandbox lifecycle:
//   - "deletes sandbox for closed issue and creates new for open issue"
//   - "does not create new sandbox if max active sandboxes reached" -> Covered by maxsandboxes_test
//   - "does not create new sandbox if it already exists"
func TestReconcileIssueHandlerSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

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
				MaxActiveSandboxes: 1,
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
				},
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name: handlerName,
					},
				},
			},
		},
	}

	// Sandbox for an issue that is now closed (Issue 2)
	closedIssueSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name": "test-repowatch-issue-2", // Note: missing prefix? If controller expects prefix for parsing?
				// The controller uses `getOwnedSandboxes` which filters by OwnerRef.
				// Then it splits by `-issue-` or `-pr-`.
				// If name is `test-repowatch-issue-2`, split by `-issue-` works.
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

	// Test case 1: Deleting a sandbox for a closed issue and creating a new one for an open issue.
	t.Run("deletes sandbox for closed issue and creates new for open issue", func(_ *testing.T) {
		// Mock HTTP to return only Issue 1 (open)
		mockHTTPClient := &http.Client{
			Transport: &mockRoundTripper{
				responses: map[string]func() *http.Response{
					"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`))}
					},
					"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
						return &http.Response{
							StatusCode: http.StatusOK,
							Body: io.NopCloser(strings.NewReader(`[
												{"number": 1, "html_url": "https://github.com/test/repo/issues/1", "title": "Issue 1", "repository_url": "https://api.github.com/repos/test/repo"}
											]`)),
						}
					},
					"https://api.github.com/user": func() *http.Response {
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"login": "test-user"}`))}
					},
				},
			},
		}
		ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

		fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, closedIssueSandbox).WithStatusSubresource(repoWatch).Build()

		r := &Reconciler{
			Client: fakeClient,
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return ghClient, map[string]string{"pat": "test-pat"}, nil
			},
		}

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"}, Data: map[string][]byte{"pat": []byte("test-pat")}}
		g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}}
		_, err := r.Reconcile(context.Background(), req)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Second Reconcile to process creation after deletion
		_, err = r.Reconcile(context.Background(), req)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that the sandbox for the closed issue (Issue 2) is deleted
		// And a new one for Issue 1 is created.
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "Sandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1))
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal(fmt.Sprintf("%s-issue-1", repoWatch.Name)))
	})

	// Test case 3: Not creating a new sandbox if it already exists.
	t.Run("does not create new sandbox if it already exists", func(_ *testing.T) {
		// Existing sandbox for issueNumber 1
		existingIssueSandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      "test-repowatch-issue-1", // Must match controller naming
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

		// Mock HTTP to return Issue 1 (open)
		mockHTTPClient := &http.Client{
			Transport: &mockRoundTripper{
				responses: map[string]func() *http.Response{
					"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`))}
					},
					"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
						return &http.Response{
							StatusCode: http.StatusOK,
							Body: io.NopCloser(strings.NewReader(`[
												{"number": 1, "html_url": "https://github.com/test/repo/issues/1", "title": "Issue 1", "repository_url": "https://api.github.com/repos/test/repo"}
											]`)),
						}
					},
					"https://api.github.com/user": func() *http.Response {
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"login": "test-user"}`))}
					},
				},
			},
		}
		ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

		fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingIssueSandbox).WithStatusSubresource(repoWatch).Build()

		r := &Reconciler{
			Client: fakeClient,
			Scheme: s,
			NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
				return ghClient, map[string]string{"pat": "test-pat"}, nil
			},
		}

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"}, Data: map[string][]byte{"pat": []byte("test-pat")}}
		g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}}
		_, err := r.Reconcile(context.Background(), req)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Check that no new sandbox was created and the existing one is still there
		sandboxList := &unstructured.UnstructuredList{}
		sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "agents.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "Sandbox",
		})
		g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
		g.Expect(sandboxList.Items).To(gomega.HaveLen(1)) // Only the existingIssueSandbox should exist
		g.Expect(sandboxList.Items[0].GetName()).To(gomega.Equal("test-repowatch-issue-1"))
	})
}

func TestReconciler_Reconcile_NotFound(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).Build()

	// 3. Create your Reconciler instance
	r := &Reconciler{
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

func TestReconciler_Reconcile_GitHubSecretNotFound(t *testing.T) {
	g := gomega.NewWithT(t)

	// 1. Create a Scheme and add your API types to it
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	// 2. Initialize the fake client with any initial objects
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// 3. Create your Reconciler instance
	r := &Reconciler{
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

func TestReconciler_Reconcile_InvalidRepoURL(t *testing.T) {
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
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1"}, "title": "Test PR"}]`)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)
	r := &Reconciler{
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
					"email": []byte("test@example.com"),
				},
			},
			expectErr:     false,
			expectedPAT:   "test-pat",
			expectedName:  "test-user",
			expectedEmail: "test@example.com",
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

	// 5. Wait for the logic to update the secret
	g.Eventually(func() string {
		updatedSecret := &corev1.Secret{}
		err := fakeClient.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: namespace}, updatedSecret)
		if err != nil {
			return ""
		}
		return string(updatedSecret.Data[OAuthPATKey])
	}, 2*time.Second, 100*time.Millisecond).Should(gomega.Equal(newToken))

	// 6. Verify refresh token was also updated
	updatedSecret := &corev1.Secret{}
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: namespace}, updatedSecret)).To(gomega.Succeed())
	g.Expect(string(updatedSecret.Data["refresh_token"])).To(gomega.Equal(newRefreshToken))
}

// TestReconciler_Reconcile_ExplicitAndListedPRs verifies that the reconciler correctly handles
// both explicitly requested PRs (via Spec.Review.PullRequests) and listed open PRs.
// It ensures that sandboxes are created for both types.
func TestReconciler_Reconcile_ExplicitAndListedPRs(t *testing.T) {
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
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"}, "html_url": "https://github.com/test/repo/pull/1", "title": "Test PR 1", "diff_url": "https://github.com/test/repo/pull/1.diff"}]`)),
					}
				},
				"https://api.github.com/repos/test/repo/pulls/42": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"number": 42, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "feature"}, "html_url": "https://github.com/test/repo/pull/42", "title": "Explicit PR 42", "diff_url": "https://github.com/test/repo/pull/42.diff"}`)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	r := &Reconciler{
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
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
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

	// 6. Call the Reconcile method
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 7. Assert expected outcomes
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(2)) // Both 1 and 42
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(2))

	// Verify PR 1
	foundPR1 := false
	for _, pr := range fetchedRepoWatch.Status.ReviewSandboxes {
		if pr.Number == 1 {
			foundPR1 = true
			break
		}
	}
	g.Expect(foundPR1).To(gomega.BeTrue())

	// Verify PR 42
	foundPR42 := false
	for _, pr := range fetchedRepoWatch.Status.ReviewSandboxes {
		if pr.Number == 42 {
			foundPR42 = true
			break
		}
	}
	g.Expect(foundPR42).To(gomega.BeTrue())

	// Check that ReviewSandboxes were created
	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(gomega.Succeed())
	g.Expect(reviewSandboxList.Items).To(gomega.HaveLen(2))
}

func TestReconciler_Reconcile_FilteredAndSortedPRs(t *testing.T) {
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
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(responseBody)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	r := &Reconciler{
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
				MaxActiveSandboxes: 2,
				Labels:             [][]string{{"bug"}},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
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

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(2))
	// Expected order of processing: PR 4, PR 1. PR 2 is pending. PR 3 is filtered.
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(2))

	// Because ReviewSandboxes are appended as sandboxes are created, and we passed a sorted list to reconcileReviewSandboxes,
	// they should be in order of the passed list (PR 4, PR 1, PR 2).
	// However, createReviewSandboxForPR is called sequentially.
	// So ReviewSandboxes should reflect that order.

	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(4))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[1].Number).To(gomega.Equal(1))

	g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingPRs[0]).To(gomega.Equal(2))
}

// TestReconcileReviewSandboxes_RespectsExistingActiveSandboxes verifies that the MaxActiveSandboxes limit
// is respected by taking pre-existing active sandboxes into account before creating new ones.
func TestReconcileReviewSandboxes_RespectsExistingActiveSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	repoURL := "https://github.com/test/repo"

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch-maxactive",
			Namespace: "default",
			UID:       "test-uid-maxactive",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 1, // Strict limit
				MaxSandboxes:       10,
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
				},
			},
		},
	}

	// 1. Pre-existing Active Sandbox for PR #1
	existingActiveSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-maxactive-pr-1",
				"namespace": "default",
				"labels": map[string]interface{}{
					"review.gemini.google.com/repowatch": repoWatch.Name,
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "review.gemini.google.com/v1alpha1",
						"kind":       "RepoWatch",
						"name":       repoWatch.Name,
						"uid":        string(repoWatch.UID),
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1), // It's active
			},
		},
	}

	// 2. New PR (PR 2) that should be made pending
	pr2Number := 2
	pr2 := &github.PullRequest{
		Number: &pr2Number,
		Head: &github.PullRequestBranch{
			Repo: &github.Repository{CloneURL: github.String(repoURL)},
			Ref:  github.String("feature-branch"),
		},
		HTMLURL: github.String("https://github.com/test/repo/pull/2"),
		Title:   github.String("Test PR 2"),
		DiffURL: github.String("https://github.com/test/repo/pull/2.diff"),
	}

	// PR #1 is also "open" so the existing sandbox is not deleted.
	pr1Number := 1
	pr1 := &github.PullRequest{Number: &pr1Number}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingActiveSandbox).WithStatusSubresource(repoWatch).Build(),
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return &github.Client{}, map[string]string{}, nil
		},
	}

	// The list of sandboxes passed to the function contains the pre-existing one.
	existingSandboxList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*existingActiveSandbox}}

	// The list of open PRs includes the one with an existing sandbox and the new one.
	openPRs := []*github.PullRequest{pr2, pr1}

	// Call reconcile
	watchedPRs, pendingPRs, activeSandboxes := r.reconcileReviewSandboxesInternal(context.Background(), &github.User{Login: github.String("test-user")}, repoWatch, []*github.PullRequest{}, openPRs, existingSandboxList, map[string]*corev1.Pod{})
	repoWatch.Status.ReviewSandboxes = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs
	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	g.Expect(r.Status().Update(context.Background(), repoWatch)).To(gomega.Succeed())

	// Verify results: No new sandbox should be created
	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	// The buggy code will fail here, creating a second sandbox.
	g.Expect(sandboxList.Items).To(gomega.HaveLen(1), "No new sandbox should have been created because the active limit of 1 was already met")

	// Verify Status
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	// The active count in the status should reflect the running total.
	g.Expect(fetchedRepoWatch.Status.ActiveSandboxCount).To(gomega.Equal(1))

	// PR 1 should be watched, PR 2 should be pending.
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(1))
	g.Expect(fetchedRepoWatch.Status.PendingPRs).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.PendingPRs[0]).To(gomega.Equal(2))
}

// TestReconcile_MultipleRepoWatchesSameRepo verifies that two RepoWatches
// for the same repository but different LLMs can both create their own distinct
// sandboxes when reconciled.
func TestReconcile_MultipleRepoWatchesSameRepo(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	prNumber := 101
	repoURL := "https://github.com/test/multi-repo"

	// 1. Setup mock GitHub API
	prListJSON := fmt.Sprintf(`[{"number": %d, "head": {"repo": {"clone_url": "%s", "html_url": "%s"}, "ref": "main"}, "html_url": "%s/pull/%d", "title": "Test PR", "diff_url": "%s/pull/%d.diff"}]`, prNumber, repoURL, repoURL, repoURL, prNumber, repoURL, prNumber)
	userJSON := `{"login": "test-user", "name": "Test User", "email": "test@example.com"}`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/multi-repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(prListJSON)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(userJSON)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	// 2. Create RepoWatch resources
	repoWatchA := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "repowatch-a", Namespace: "default", UID: "uid-a"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 1,
				LLM:                reviewv1alpha1.LLMConfig{Provider: "gemini-cli", APIKeySecretRef: "dummy-secret"},
			},
		},
	}
	repoWatchB := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "repowatch-b", Namespace: "default", UID: "uid-b"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          repoURL,
			GithubSecretName: "github-secret",
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes: 1,
				LLM:                reviewv1alpha1.LLMConfig{Provider: "claude", APIKeySecretRef: "dummy-secret"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"},
		Data:       map[string][]byte{"pat": []byte("test-pat")},
	}

	// 3. Setup fake client and reconciler
	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatchA, repoWatchB, secret).WithStatusSubresource(repoWatchA, repoWatchB).Build()
	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// 4. Reconcile for RepoWatch-A
	reqA := reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatchA.Name, Namespace: repoWatchA.Namespace}}
	_, err := r.Reconcile(context.Background(), reqA)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 5. Reconcile for RepoWatch-B
	reqB := reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatchB.Name, Namespace: repoWatchB.Namespace}}
	_, err = r.Reconcile(context.Background(), reqB)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// 6. Verification
	// Assert Total Sandbox Count
	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	g.Expect(sandboxList.Items).To(gomega.HaveLen(2), "Expected two sandboxes to be created, one for each RepoWatch")

	// Validate Sandbox A
	sandboxA := &unstructured.Unstructured{}
	sandboxA.SetGroupVersionKind(sandboxList.GroupVersionKind())
	sandboxAName := types.NamespacedName{Name: fmt.Sprintf("%s-pr-%d", repoWatchA.Name, prNumber), Namespace: "default"}
	g.Expect(r.Client.Get(context.Background(), sandboxAName, sandboxA)).To(gomega.Succeed())

	// Check AGENT_NAME env var for provider
	containersA, foundA, errA := unstructured.NestedSlice(sandboxA.Object, "spec", "podTemplate", "spec", "containers")
	g.Expect(errA).NotTo(gomega.HaveOccurred())
	g.Expect(foundA).To(gomega.BeTrue())
	containerA := containersA[0].(map[string]interface{})
	envA := containerA["env"].([]interface{})

	foundAgentNameA := false
	for _, e := range envA {
		envVar := e.(map[string]interface{})
		if envVar["name"] == "AGENT_NAME" {
			g.Expect(envVar["value"]).To(gomega.Equal("gemini-cli"))
			foundAgentNameA = true
			break
		}
	}
	g.Expect(foundAgentNameA).To(gomega.BeTrue())

	// Validate Sandbox B
	sandboxB := &unstructured.Unstructured{}
	sandboxB.SetGroupVersionKind(sandboxList.GroupVersionKind())
	sandboxBName := types.NamespacedName{Name: fmt.Sprintf("%s-pr-%d", repoWatchB.Name, prNumber), Namespace: "default"}
	g.Expect(r.Client.Get(context.Background(), sandboxBName, sandboxB)).To(gomega.Succeed())

	containersB, foundB, errB := unstructured.NestedSlice(sandboxB.Object, "spec", "podTemplate", "spec", "containers")
	g.Expect(errB).NotTo(gomega.HaveOccurred())
	g.Expect(foundB).To(gomega.BeTrue())
	containerB := containersB[0].(map[string]interface{})
	envB := containerB["env"].([]interface{})

	foundAgentNameB := false
	for _, e := range envB {
		envVar := e.(map[string]interface{})
		if envVar["name"] == "AGENT_NAME" {
			g.Expect(envVar["value"]).To(gomega.Equal("claude"))
			foundAgentNameB = true
			break
		}
	}
	g.Expect(foundAgentNameB).To(gomega.BeTrue())

	// Validate Status of RepoWatches
	fetchedA := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), reqA.NamespacedName, fetchedA)).To(gomega.Succeed())
	g.Expect(fetchedA.Status.ActiveSandboxCount).To(gomega.Equal(1))
	g.Expect(fetchedA.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedA.Status.ReviewSandboxes[0].Number).To(gomega.Equal(prNumber))

	fetchedB := &reviewv1alpha1.RepoWatch{}
	g.Expect(r.Client.Get(context.Background(), reqB.NamespacedName, fetchedB)).To(gomega.Succeed())
	g.Expect(fetchedB.Status.ActiveSandboxCount).To(gomega.Equal(1))
	g.Expect(fetchedB.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedB.Status.ReviewSandboxes[0].Number).To(gomega.Equal(prNumber))
}

func TestReconciler_Reconcile_AssigneeFilteredPRs(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	// Mock Response
	// PR 3: #3, Assignee: user1
	// PR 2: #2, Assignee: user2
	// PR 1: #1, No Assignee
	responseBody := `[
        {"number": 3, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "assignees": [{"login": "user1"}]},
        {"number": 2, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "assignees": [{"login": "user2"}]},
        {"number": 1, "head": {"repo": {"clone_url": "u", "html_url": "u"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "assignees": []}
    ]`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(responseBody)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user"}`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	objName := "test-repowatch-assignees"
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
				MaxActiveSandboxes: 10,
				Assignees:          []string{"user1"},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
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

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())

	// Only PR 3 should be watched
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(3))
}
