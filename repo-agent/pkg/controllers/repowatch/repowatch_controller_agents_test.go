package repowatch

import (
	"context"
	"encoding/base64"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconciler_ReconcileAgents(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	agentYaml := `---
name: Test Agent
description: A test agent
schedule: " @weekly"
---
Do this and that.
`
	encodedAgentYaml := base64.StdEncoding.EncodeToString([]byte(agentYaml))

	// Mock Responses
	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				// Get .agent directory content
				"https://api.github.com/repos/test/repo/contents/.agent": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"name": "test-agent.yaml", "path": ".agent/test-agent.yaml", "type": "file"}]`)),
					}
				},
				// Get test-agent.yaml content
				"https://api.github.com/repos/test/repo/contents/.agent/test-agent.yaml": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"name": "test-agent.yaml", "path": ".agent/test-agent.yaml", "type": "file", "encoding": "base64", "content": "%s"}`, encodedAgentYaml))),
					}
				},
				// Search issues
				"https://api.github.com/search/issues?per_page=1&q=repo%3Atest%2Frepo+is%3Aissue+label%3A%22agent%3Atest-agent.yaml%22+sort%3Acreated-desc": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"total_count": 0, "items": []}`)),
					}
				},
				// Create issue
				"https://api.github.com/repos/test/repo/issues": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusCreated,
						Body:       io.NopCloser(strings.NewReader(`{"number": 1, "title": "[Agent] Test Agent"}`)),
					}
				},
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"login": "test-user", "name": "Test User", "email": "test@example.com"}`)),
					}
				},
				// List PRs (called by reconcileReviews)
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[]`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()

	r := &Reconciler{
		Client: fakeClient,
		Scheme: s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "default",
		},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Agent: &reviewv1alpha1.AgentSpec{
				Enabled: true,
			},
		},
	}
	g.Expect(fakeClient.Create(context.Background(), repoWatch)).To(gomega.Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"pat": []byte("test-pat"),
		},
	}
	g.Expect(fakeClient.Create(context.Background(), secret)).To(gomega.Succeed())

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repowatch",
			Namespace: "default",
		},
	}

	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}
