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
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_ReconcileExplicitIssues(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)

	// Mock current user and issues
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "agent-user"}`)),
					}
				},
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
								"repository_url": "https://api.github.com/repos/test/repo",
								"assignees": [],
								"labels": [{"name": "bug"}]
							}
						]`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	objName := "test-repowatch-explicit-issue"
	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: "default",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "test-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 5,
				AssignedToSelf:     true,      // THIS SHOULD NORMALLY FILTER OUT ISSUE 10
				Issues:             []int{10}, // BUT IT IS EXPLICITLY ADDED HERE
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name:   "test-handler",
						Labels: []string{"bug"},
					},
					{
						Name:   "other-handler",
						Labels: []string{"feature"}, // THIS DOES NOT MATCH ISSUE 10 LABELS
					},
				},
			},
		},
	}

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	// Run reconcile
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: objName, Namespace: "default"}})
	g.Expect(err).To(gomega.Succeed())

	// Check if a sandbox was created for Issue 10
	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), client.ObjectKey{Name: objName, Namespace: "default"}, fetchedRepoWatch)).To(gomega.Succeed())

	// Both handlers should have matched Issue 10 because it is explicit
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes).To(gomega.HaveKey("test-handler"))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes).To(gomega.HaveKey("other-handler"))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["test-handler"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["other-handler"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["test-handler"][0].Number).To(gomega.Equal(10))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["other-handler"][0].Number).To(gomega.Equal(10))

	// Check that an IssueSandbox was created
	issueSandboxList := &unstructured.UnstructuredList{}
	issueSandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})
	g.Expect(fakeClient.List(context.Background(), issueSandboxList)).To(gomega.Succeed())
	g.Expect(issueSandboxList.Items).To(gomega.HaveLen(1), "Only one sandbox should have been created for Issue 10")
}
