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
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type diagnosticRoundTripper struct {
	t         *testing.T
	responses map[string]func() *http.Response
}

func (d *diagnosticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	d.t.Logf("GitHub request URL: %s", req.URL.String())
	respFunc, ok := d.responses[req.URL.String()]
	if !ok {
		d.t.Logf("No mock response for URL: %s", req.URL.String())
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	resp := respFunc()
	resp.Request = req
	return resp, nil
}

func TestReconciler_Reconcile_ForceGvisor_AllSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(s)).To(gomega.Succeed())
	g.Expect(reviewv1alpha1.AddToScheme(s)).To(gomega.Succeed())
	g.Expect(sandboxv1alpha1.AddToScheme(s)).To(gomega.Succeed())

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	mockHTTPClient := &http.Client{
		Transport: &diagnosticRoundTripper{
			t: t,
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`[
							{
								"number": 5,
								"title": "Test PR",
								"html_url": "https://github.com/test/repo/pull/5",
								"diff_url": "https://github.com/test/repo/pull/5.diff",
								"head": {
									"repo": {
										"clone_url": "https://github.com/test/repo"
									},
									"ref": "feature"
								}
							}
						]`)),
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
								"repository_url": "https://api.github.com/repos/test/repo",
								"labels": [{"name": "bug"}]
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
				"https://api.github.com/repos/test-user/repo": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"default_branch": "main"}`)),
					}
				},
				"https://api.github.com/repos/test-user/repo/branches?per_page=100": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"name": "dev-feature", "commit": {"sha": "1234567890abcdef"}}]`)),
					}
				},
				"https://api.github.com/repos/test-user/repo/compare/main...dev-feature": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"ahead_by": 1}`)),
					}
				},
				"https://api.github.com/repos/test-user/repo/commits/1234567890abcdef": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"commit": {"committer": {"date": "2026-05-04T00:00:00Z"}}}`)),
					}
				},
			}},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	r := &Reconciler{
		Client:           fakeClient,
		Scheme:           s,
		ForceSandboxMode: reviewv1alpha1.DindSupportGvisor,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	objName := "test-repowatch-force-gvisor"
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
				MaxActiveSandboxes: 5,
				LLM: reviewv1alpha1.LLMConfig{
					Provider: "gemini-cli",
				},
				DindSupport: reviewv1alpha1.DindSupportNone,
			},
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 5,
				LLM: reviewv1alpha1.LLMConfig{
					Provider:        "gemini-cli",
					APIKeySecretRef: "llm-secret",
				},
				DindSupport: reviewv1alpha1.DindSupportNone,
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name:   "test-handler",
						Labels: []string{"bug"},
						Prompt: "Triage this",
					},
				},
			},
			Dev: reviewv1alpha1.DevSpec{
				MaxSandboxes:       5,
				MaxActiveSandboxes: 5,
				LLM: reviewv1alpha1.LLMConfig{
					Provider: "gemini-cli",
				},
				DindSupport: reviewv1alpha1.DindSupportNone,
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

	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), sandboxList)).To(gomega.Succeed())

	for _, item := range sandboxList.Items {
		t.Logf("Found sandbox name: %s, type label: %s", item.GetName(), item.GetLabels()["sandbox.gemini.google.com/type"])
	}

	g.Expect(sandboxList.Items).To(gomega.HaveLen(3))

	for _, item := range sandboxList.Items {
		runtimeClassName, found, err := unstructured.NestedString(item.Object, "spec", "podTemplate", "spec", "runtimeClassName")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(found).To(gomega.BeTrue())
		g.Expect(runtimeClassName).To(gomega.Equal("gvisor"), "Sandbox %s type %s did not have gvisor runtime class", item.GetName(), item.GetLabels()["sandbox.gemini.google.com/type"])
	}

	// Verify SandboxModeConsistency condition is set to False due to inconsistent config (None)
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())

	var consistencyCond *metav1.Condition
	for i := range fetchedRepoWatch.Status.Conditions {
		if fetchedRepoWatch.Status.Conditions[i].Type == "SandboxModeConsistency" {
			consistencyCond = &fetchedRepoWatch.Status.Conditions[i]
			break
		}
	}
	g.Expect(consistencyCond).NotTo(gomega.BeNil())
	g.Expect(consistencyCond.Status).To(gomega.Equal(metav1.ConditionFalse))
	g.Expect(consistencyCond.Reason).To(gomega.Equal("Inconsistent"))
}
