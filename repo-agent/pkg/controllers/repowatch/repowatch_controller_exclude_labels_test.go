// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package repowatch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

func TestReconcile_ExcludeLabels(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(reviewv1alpha1.AddToScheme(s))

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).
		Build()

	// PR 1: #1, Label: bug, do-not-merge
	// PR 2: #2, Label: bug
	// Issue 3: #3, Label: sig/api-machinery, triage/accepted
	// Issue 4: #4, Label: sig/api-machinery

	prResponseBody := `[
        {"number": 2, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "bug"}], "assignees": [{"login": "test-user"}]},
        {"number": 1, "head": {"repo": {"clone_url": "https://github.com/test/repo", "html_url": "https://github.com/test/repo"}, "ref": "m"}, "html_url": "u", "diff_url": "d", "title": "t", "labels": [{"name": "bug"}, {"name": "do-not-merge"}], "assignees": [{"login": "test-user"}]}
    ]`

	issueResponseBody := `[
		{"number": 4, "repository_url": "https://api.github.com/repos/test/repo", "html_url": "u", "title": "t", "labels": [{"name": "sig/api-machinery"}], "assignees": [{"login": "test-user"}]},
		{"number": 3, "repository_url": "https://api.github.com/repos/test/repo", "html_url": "u", "title": "t", "labels": [{"name": "sig/api-machinery"}, {"name": "triage/accepted"}], "assignees": [{"login": "test-user"}]}
	]`

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(prResponseBody)),
					}
				},
				"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(issueResponseBody)),
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

	objName := "test-repowatch-exclude"
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
				Labels:             [][]string{{"bug"}},
				ExcludeLabels:      []string{"do-not-merge"},
				LLM: reviewv1alpha1.LLMConfig{
					APIKeySecretRef: "dummy-secret",
				},
			},
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 10,
				Handlers: []reviewv1alpha1.IssueHandlerSpec{
					{
						Name:          "triage",
						Labels:        []string{"sig/api-machinery"},
						ExcludeLabels: []string{"triage/accepted"},
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

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	fetchedRepoWatch := &reviewv1alpha1.RepoWatch{}
	g.Expect(fakeClient.Get(context.Background(), req.NamespacedName, fetchedRepoWatch)).To(gomega.Succeed())

	// PR #1 should be excluded by "do-not-merge" label.
	// PR #2 should be included.
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.ReviewSandboxes[0].Number).To(gomega.Equal(2))

	// Issue #3 should be excluded by "triage/accepted" label.
	// Issue #4 should be included.
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["triage"]).To(gomega.HaveLen(1))
	g.Expect(fetchedRepoWatch.Status.IssueSandboxes["triage"][0].Number).To(gomega.Equal(4))
}
