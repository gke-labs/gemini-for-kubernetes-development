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
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// TestReconcileIssues_PRWatchFollowUp verifies that a completed fix task
// whose sandbox is aliased to a PR gets a `factory pr watch` follow-up, and
// that the completed fix is not relaunched.
func TestReconcileIssues_PRWatchFollowUp(t *testing.T) {
	g := gomega.NewWithT(t)

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	mockHTTPClient := &http.Client{
		Transport: &mockRoundTripper{
			responses: map[string]func() *http.Response{
				"https://api.github.com/user": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"login": "test-user"}`))}
				},
				"https://api.github.com/repos/test/repo/pulls?direction=desc&per_page=100&sort=created&state=open": func() *http.Response {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`))}
				},
				"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": func() *http.Response {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`[
							{"number": 10, "html_url": "https://github.com/test/repo/issues/10", "title": "Test Issue", "repository_url": "https://api.github.com/repos/test/repo"}
						]`)),
					}
				},
			},
		},
	}
	ghClient := clients.NewGitHubClientFromHTTP(mockHTTPClient)

	// Completed fix sandbox aliased to PR 77 by factory.
	fixSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      "fix-repo-10",
				"namespace": "default",
				"labels": map[string]interface{}{
					"factory.gemini.google.com/managed": "true",
					"factory.gemini.google.com/pr":      "77",
				},
				"annotations": map[string]interface{}{
					"sandbox.gemini.google.com/last-task-state": "Completed",
					"sandbox.gemini.google.com/completion-time": time.Now().UTC().Format(time.RFC3339),
					"htmlURL": "https://github.com/test/repo/pull/77",
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		},
	}

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-watch", Namespace: "default"},
		Spec: reviewv1alpha1.RepoWatchSpec{
			RepoURL:          "https://github.com/test/repo",
			GithubSecretName: "github-secret",
			Issue: &reviewv1alpha1.IssueSpec{
				MaxActiveSandboxes: 5,
				AssignedToSelf:     false,
				Handlers:           []reviewv1alpha1.IssueHandlerSpec{{Name: "fix"}},
			},
		},
	}
	githubSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-secret", Namespace: "default"},
		Data:       map[string][]byte{"pat": []byte("test-pat")},
	}

	fakeClient := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, githubSecret, fixSandbox).WithStatusSubresource(&reviewv1alpha1.RepoWatch{}).Build()
	fakeFactory := newFakeLauncher()
	r := &Reconciler{
		Client:  fakeClient,
		Factory: fakeFactory,
		Scheme:  s,
		NewGithubClient: func(_ context.Context, _ client.Client, _ *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
			return ghClient, map[string]string{"pat": "test-pat"}, nil
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-watch", Namespace: "default"}})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// The completed fix is not relaunched; the only launch is the PR watch.
	launches := fakeFactory.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("default/prwatch-77"))
	g.Expect(launches[0].PRWatchOpts).NotTo(gomega.BeNil())
	g.Expect(launches[0].PRWatchOpts.PRURL).To(gomega.Equal("https://github.com/test/repo/pull/77"))
	g.Expect(launches[0].PRWatchOpts.GithubToken).To(gomega.Equal("test-pat"))

	// A watch child is not duplicated while one is running.
	fakeFactory.running["default/prwatch-77"] = true
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-watch", Namespace: "default"}})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fakeFactory.launches()).To(gomega.HaveLen(1))

	// After a finished (recent) watch child, relaunch is backed off.
	delete(fakeFactory.running, "default/prwatch-77")
	fakeFactory.results["default/prwatch-77"] = factorycli.Result{FinishedAt: time.Now()}
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-watch", Namespace: "default"}})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fakeFactory.launches()).To(gomega.HaveLen(1))

	// Once the backoff has elapsed, the watch is relaunched.
	fakeFactory.results["default/prwatch-77"] = factorycli.Result{FinishedAt: time.Now().Add(-time.Hour)}
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-watch", Namespace: "default"}})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fakeFactory.launches()).To(gomega.HaveLen(2))
}
