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
	"testing"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

func TestReconcileFactoryUserSecret(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-watch", Namespace: "tenant-a"},
		Spec:       reviewv1alpha1.RepoWatchSpec{GithubSecretName: "github-pat"},
	}
	ghSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "tenant-a"},
		Data:       map[string][]byte{OAuthPATKey: []byte("gho_token")},
	}
	geminiSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: k8s.GeminiSecretName, Namespace: "tenant-a"},
		Data:       map[string][]byte{"gemini": []byte("gemini-key")},
	}

	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, ghSecret, geminiSecret).Build()
	r := &Reconciler{Client: c, Scheme: s}
	user := &github.User{Login: github.String("tenant-a")}

	// Create
	g.Expect(r.reconcileFactoryUserSecret(context.Background(), repoWatch, user)).To(gomega.Succeed())
	created := &corev1.Secret{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: FactoryUserSecretName, Namespace: "tenant-a"}, created)).To(gomega.Succeed())
	g.Expect(created.Data[factoryKeyGithubToken]).To(gomega.Equal([]byte("gho_token")))
	g.Expect(created.Data[factoryKeyGithubLogin]).To(gomega.Equal([]byte("tenant-a")))
	g.Expect(created.Data[factoryKeyGithubEmail]).To(gomega.Equal([]byte("tenant-a@users.noreply.github.com")))
	g.Expect(created.Data[factoryKeyGeminiAPIKey]).To(gomega.Equal([]byte("gemini-key")))

	// Update on token change: manual_pat takes precedence over oauth_pat
	ghSecret.Data[ManualPATKey] = []byte("ghp_manual")
	g.Expect(c.Update(context.Background(), ghSecret)).To(gomega.Succeed())
	g.Expect(r.reconcileFactoryUserSecret(context.Background(), repoWatch, user)).To(gomega.Succeed())
	updated := &corev1.Secret{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: FactoryUserSecretName, Namespace: "tenant-a"}, updated)).To(gomega.Succeed())
	g.Expect(updated.Data[factoryKeyGithubToken]).To(gomega.Equal([]byte("ghp_manual")))

	// User-provided email wins over the noreply fallback
	user.Email = github.String("real@example.com")
	g.Expect(r.reconcileFactoryUserSecret(context.Background(), repoWatch, user)).To(gomega.Succeed())
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: FactoryUserSecretName, Namespace: "tenant-a"}, updated)).To(gomega.Succeed())
	g.Expect(updated.Data[factoryKeyGithubEmail]).To(gomega.Equal([]byte("real@example.com")))
}

func TestReconcileFactoryUserSecretNoToken(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = reviewv1alpha1.AddToScheme(s)

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-watch", Namespace: "tenant-a"},
		Spec:       reviewv1alpha1.RepoWatchSpec{GithubSecretName: "github-pat"},
	}
	ghSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-pat", Namespace: "tenant-a"},
		Data:       map[string][]byte{},
	}

	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(repoWatch, ghSecret).Build()
	r := &Reconciler{Client: c, Scheme: s}

	err := r.reconcileFactoryUserSecret(context.Background(), repoWatch, &github.User{Login: github.String("tenant-a")})
	g.Expect(err).To(gomega.HaveOccurred())
	notCreated := &corev1.Secret{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: FactoryUserSecretName, Namespace: "tenant-a"}, notCreated)).NotTo(gomega.Succeed())
}
