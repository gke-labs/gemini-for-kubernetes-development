/*
Copyright 2026.

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
	"fmt"
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
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_ReconcileReviewConflicts(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	repoURL := "https://github.com/test/repo"
	owner := "test"
	repoName := "repo"
	prNumber := 1

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
				ResolveConflicts:   true,
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "llm-secret",
				},
				Models: []string{"gemini-1.5-pro"},
			},
		},
	}

	existingSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-pr-1",
				"namespace": "default",
				"labels": map[string]interface{}{
					"review.gemini.google.com/repowatch": "test-repowatch",
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

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`[{"number": %d, "head": {"repo": {"clone_url": "%s", "html_url": "%s"}, "ref": "feature", "sha": "head-sha"}, "base": {"ref": "main"}, "html_url": "%s/pull/%d", "title": "Test PR"}]`, prNumber, repoURL, repoURL, repoURL, prNumber))),
					}
				},
				fmt.Sprintf("https://api.github.com/repos/%s/%s/branches/main", owner, repoName): func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"name": "main", "commit": {"sha": "base-sha"}}`)),
					}
				},
				fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repoName, prNumber): func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{
							"number": %d,
							"mergeable": false,
							"head": {"repo": {"clone_url": "%s", "html_url": "%s"}, "ref": "feature", "sha": "head-sha"},
							"base": {"ref": "main"},
							"html_url": "%s/pull/%d",
							"title": "Test PR"
						}`, prNumber, repoURL, repoURL, repoURL, prNumber))),
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

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, existingSandbox, secret).WithStatusSubresource(repoWatch, &sandboxtaskv1alpha1.SandboxTask{}).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      repoWatch.Name,
			Namespace: repoWatch.Namespace,
		},
	}

	// 1. First reconcile - should create the task
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify task creation
	taskList := &sandboxtaskv1alpha1.SandboxTaskList{}
	g.Expect(fakeClient.List(context.Background(), taskList, client.InNamespace("default"))).To(gomega.Succeed())

	// There might be a "review" task created by createReviewSandboxForPR if the sandbox didn't exist.
	// But here the sandbox already exists.
	// Wait, reconcileReviewSandboxesInternal doesn't create tasks if sandbox exists, UNLESS it's a conflict task.

	foundResolveTask := false
	for _, task := range taskList.Items {
		if task.Spec.Type == "resolve-conflicts" {
			foundResolveTask = true
			g.Expect(task.Spec.Params["HEAD_SHA"]).To(gomega.Equal("head-sha"))
			g.Expect(task.Spec.Params["BASE_SHA"]).To(gomega.Equal("base-sha"))
			g.Expect(task.Spec.Params["model"]).To(gomega.Equal("gemini-1.5-pro"))
			break
		}
	}
	g.Expect(foundResolveTask).To(gomega.BeTrue(), "Expected resolve-conflicts task to be created")

	// 2. Second reconcile - should NOT create a duplicate task
	_, err = r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(fakeClient.List(context.Background(), taskList, client.InNamespace("default"))).To(gomega.Succeed())

	resolveTaskCount := 0
	for _, task := range taskList.Items {
		if task.Spec.Type == "resolve-conflicts" {
			resolveTaskCount++
		}
	}
	g.Expect(resolveTaskCount).To(gomega.Equal(1), "Expected only one resolve-conflicts task")

	// 3. Mark task as Completed
	resolveTask := &sandboxtaskv1alpha1.SandboxTask{}
	for i := range taskList.Items {
		if taskList.Items[i].Spec.Type == "resolve-conflicts" {
			resolveTask = &taskList.Items[i]
			break
		}
	}
	resolveTask.Status.TaskState = "Completed"
	g.Expect(fakeClient.Status().Update(context.Background(), resolveTask)).To(gomega.Succeed())

	// 4. Third reconcile - should STILL NOT create a duplicate task because it was already attempted for this SHA
	_, err = r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(fakeClient.List(context.Background(), taskList, client.InNamespace("default"))).To(gomega.Succeed())
	resolveTaskCount = 0
	for _, task := range taskList.Items {
		if task.Spec.Type == "resolve-conflicts" {
			resolveTaskCount++
		}
	}
	g.Expect(resolveTaskCount).To(gomega.Equal(1))

	// 5. Simulate PR being mergeable now (after resolution)
	mockHTTPClient.Transport.(*mockRoundTripper).responses[fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repoName, prNumber)] = func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{
				"number": %d,
				"mergeable": true,
				"head": {"repo": {"clone_url": "%s", "html_url": "%s"}, "ref": "feature", "sha": "head-sha"},
				"base": {"ref": "main"},
				"html_url": "%s/pull/%d",
				"title": "Test PR"
			}`, prNumber, repoURL, repoURL, repoURL, prNumber))),
		}
	}

	// 6. Fourth reconcile - should update the annotation to record successful check
	_, err = r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	updatedSandbox := &unstructured.Unstructured{}
	updatedSandbox.SetGroupVersionKind(existingSandbox.GroupVersionKind())
	g.Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: existingSandbox.GetName(), Namespace: existingSandbox.GetNamespace()}, updatedSandbox)).To(gomega.Succeed())

	g.Expect(updatedSandbox.GetAnnotations()["sandbox.gemini.google.com/last-conflict-check-key"]).To(gomega.Equal("head-sha:base-sha"))
}
