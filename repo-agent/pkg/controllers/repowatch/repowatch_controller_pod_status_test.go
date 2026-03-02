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
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_ReconcileIssues_PodEvicted(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)

	// Setup Mock GitHub
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

	// Objects
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-repowatch", Namespace: "default", UID: "uid-1"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				Handlers: []reviewv1alpha1.IssueHandlerSpec{{Name: "default"}},
			},
		},
	}

	// Existing Sandbox
	sandboxName := "devc-test-repowatch-issue-1"
	issueSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "issue",
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
				"replicas": int64(1),
			},
		},
	}

	// Failed Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName + "-pod",
			Namespace: "default",
			Labels: map[string]string{
				"sandbox":      sandboxName,
				"sandbox-type": "issue",
			},
		},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Reason:  "Evicted",
			Message: "The node was low on resource: ephemeral-storage",
		},
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"}, Data: map[string][]byte{"pat": []byte("test-pat")}}

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, issueSandbox, pod, secret).WithStatusSubresource(repoWatch).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify RepoWatch status
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"][0].Status).To(gomega.Equal("Evicted: The node was low on resource: ephemeral-storage"))

	// Verify Annotation
	fetchedSandbox := &unstructured.Unstructured{}
	fetchedSandbox.SetGroupVersionKind(schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"})
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: sandboxName, Namespace: "default"}, fetchedSandbox)).To(gomega.Succeed())

	ann := fetchedSandbox.GetAnnotations()
	g.Expect(ann).NotTo(gomega.BeNil())
	g.Expect(ann["sandbox.gemini.google.com/pod-status"]).To(gomega.Equal("Evicted: The node was low on resource: ephemeral-storage"))
}

func TestReconciler_ReconcileIssues_PodFailedOOM(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)

	// Setup Mock GitHub
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

	// Objects
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-repowatch-oom", Namespace: "default", UID: "uid-2"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				Handlers: []reviewv1alpha1.IssueHandlerSpec{{Name: "default"}},
			},
		},
	}

	// Existing Sandbox
	sandboxName := "devc-test-repowatch-oom-issue-1"
	issueSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "issue",
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
				"replicas": int64(1),
			},
		},
	}

	// Failed Pod OOM
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName + "-pod",
			Namespace: "default",
			Labels: map[string]string{
				"sandbox":      sandboxName,
				"sandbox-type": "issue",
			},
		},
		Status: corev1.PodStatus{
			Phase:  corev1.PodFailed,
			Reason: "OOMKilled",
		},
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"}, Data: map[string][]byte{"pat": []byte("test-pat")}}

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, issueSandbox, pod, secret).WithStatusSubresource(repoWatch).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify RepoWatch status
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"]).To(gomega.HaveLen(1))
	// Should match "fail: OOMKilled"
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"][0].Status).To(gomega.Equal("fail: OOMKilled"))

	// Verify Annotation
	fetchedSandbox := &unstructured.Unstructured{}
	fetchedSandbox.SetGroupVersionKind(schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"})
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: sandboxName, Namespace: "default"}, fetchedSandbox)).To(gomega.Succeed())

	ann := fetchedSandbox.GetAnnotations()
	g.Expect(ann).NotTo(gomega.BeNil())
	g.Expect(ann["sandbox.gemini.google.com/pod-status"]).To(gomega.Equal("fail: OOMKilled"))
}

func TestReconciler_ReconcileIssues_PodPendingScheduled(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)

	// Setup Mock GitHub
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

	// Objects
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-repowatch-pending", Namespace: "default", UID: "uid-3"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				Handlers: []reviewv1alpha1.IssueHandlerSpec{{Name: "default"}},
			},
		},
	}

	// Existing Sandbox
	sandboxName := "devc-test-repowatch-pending-issue-1"
	issueSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": "default",
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type": "issue",
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
				"replicas": int64(1),
			},
		},
	}

	// Pending Pod with scheduling error
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName + "-pod",
			Namespace: "default",
			Labels: map[string]string{
				"sandbox":      sandboxName,
				"sandbox-type": "issue",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  "Unschedulable",
					Message: "0/1 nodes are available: 1 insufficient cpu.",
				},
			},
		},
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"}, Data: map[string][]byte{"pat": []byte("test-pat")}}

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, issueSandbox, pod, secret).WithStatusSubresource(repoWatch).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify RepoWatch status
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: repoWatch.Name, Namespace: repoWatch.Namespace}, fetchedRepoWatch)).To(gomega.Succeed())

	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["default"][0].Status).To(gomega.Equal("Pending: 0/1 nodes are available: 1 insufficient cpu."))

	// Verify Annotation
	fetchedSandbox := &unstructured.Unstructured{}
	fetchedSandbox.SetGroupVersionKind(schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"})
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: sandboxName, Namespace: "default"}, fetchedSandbox)).To(gomega.Succeed())

	ann := fetchedSandbox.GetAnnotations()
	g.Expect(ann).NotTo(gomega.BeNil())
	g.Expect(ann["sandbox.gemini.google.com/pod-status"]).To(gomega.Equal("Pending: 0/1 nodes are available: 1 insufficient cpu."))
}
