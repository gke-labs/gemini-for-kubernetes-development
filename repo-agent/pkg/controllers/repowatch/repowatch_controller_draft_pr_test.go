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
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_IssueDraftPR(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name          string
		robotAccount  string
		draftPRSpec   *bool
		expectedDraft string
	}{
		{
			name:          "no robot account, draftPR not set -> draft",
			robotAccount:  "",
			draftPRSpec:   nil,
			expectedDraft: "true",
		},
		{
			name:          "no robot account, draftPR set to false -> not draft",
			robotAccount:  "",
			draftPRSpec:   boolPtr(false),
			expectedDraft: "",
		},
		{
			name:          "no robot account, draftPR set to true -> draft",
			robotAccount:  "",
			draftPRSpec:   boolPtr(true),
			expectedDraft: "true",
		},
		{
			name:          "robot account set, draftPR not set -> not draft",
			robotAccount:  "bot-user",
			draftPRSpec:   nil,
			expectedDraft: "",
		},
		{
			name:          "robot account set, draftPR set to true -> not draft (forced false)",
			robotAccount:  "bot-user",
			draftPRSpec:   boolPtr(true),
			expectedDraft: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)

			s := runtime.NewScheme()
			_ = clientgoscheme.AddToScheme(s)
			_ = reviewv1alpha1.AddToScheme(s)
			_ = sandboxtaskv1alpha1.AddToScheme(s)
			_ = sandboxv1alpha1.AddToScheme(s)

			repoWatch := &reviewv1alpha1.RepoWatch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repowatch",
					Namespace: "default",
				},
				Spec: reviewv1alpha1.RepoWatchSpec{
					RepoURL: "https://github.com/test/repo",
					Issue: &reviewv1alpha1.IssueSpec{
						RobotAccount:       tt.robotAccount,
						DraftPR:            tt.draftPRSpec,
						MaxActiveSandboxes: 1,
						MaxSandboxes:       1,
						Handlers: []reviewv1alpha1.IssueHandlerSpec{
							{
								Name: "fixer",
							},
						},
					},
					GithubSecretName: "test-secret",
				},
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"token": []byte("test-token"),
				},
			}

			botSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bot-user",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"token": []byte("bot-token"),
				},
			}

			fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, secret, botSecret).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

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
								Body:       io.NopCloser(strings.NewReader(`[{"number": 1, "title": "Test Issue", "html_url": "https://github.com/test/repo/issues/1", "repository_url": "https://api.github.com/repos/test/repo", "user": {"login": "user1"}}]`)),
							}
						},
						"https://api.github.com/repos/test/repo/issues/1/comments?per_page=100": func() *http.Response {
							return &http.Response{
								StatusCode: http.StatusOK,
								Body:       io.NopCloser(strings.NewReader(`[]`)),
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

			r := &Reconciler{
				Client:           fakeClient,
				Scheme:           s,
				RepoSandboxImage: "repo-sandbox:latest",
				ConfigDirImage:   "configdir:latest",
				NewGithubClient: func(ctx context.Context, k8sClient client.Client, rw *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
					return github.NewClient(mockHTTPClient), nil, nil
				},
			}

			_, err := r.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-repowatch",
					Namespace: "default",
				},
			})
			g.Expect(err).NotTo(gomega.HaveOccurred())

			// Check SandboxTask
			taskName := "test-repowatch-issue-1-fixer"
			task := &sandboxtaskv1alpha1.SandboxTask{}
			err = fakeClient.Get(context.Background(), types.NamespacedName{Name: taskName, Namespace: "default"}, task)
			g.Expect(err).NotTo(gomega.HaveOccurred())

			g.Expect(task.Spec.Params["DRAFT_PR"]).To(gomega.Equal(tt.expectedDraft))
		})
	}
}
