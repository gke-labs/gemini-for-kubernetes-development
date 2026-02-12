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

func TestReconciler_Reconcile_PR_With_Image(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`[
												{
													"number": 1,
													"head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "main"},
													"html_url": "https://github.com/test/repo/pull/1",
													"title": "Test PR",
													"diff_url": "https://github.com/test/repo/pull/1.diff"
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

	objName := "test-repowatch-pr-image"
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
				MaxActiveSandboxes: 1,
				LLM: reviewv1alpha1.LLMConfig{
					Provider:        "gemini-cli",
					APIKeySecretRef: "llm-secret",
					Prompt:          "Review this",
				},
				Image: "custom-review-image:latest",
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

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	reviewSandboxList := &unstructured.UnstructuredList{}
	reviewSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), reviewSandboxList)).To(gomega.Succeed())
	g.Expect(reviewSandboxList.Items).To(gomega.HaveLen(1))

	containers, found, err := unstructured.NestedSlice(reviewSandboxList.Items[0].Object, "spec", "podTemplate", "spec", "containers")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())
	g.Expect(containers).To(gomega.HaveLen(1))

	container := containers[0].(map[string]interface{})
	image, found, err := unstructured.NestedString(container, "image")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())
	g.Expect(image).To(gomega.Equal("custom-review-image:latest"))
}
