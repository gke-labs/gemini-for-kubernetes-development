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
	"reflect"

	"github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// Wire contract with the factory CLI: factory resolves task identity from the
// `factory-user` Secret in its namespace (see factory/pkg/constants). We do not
// import factory Go packages — process invocation and these key names are the
// only contract, matching how overseer integrates factory.
const (
	FactoryUserSecretName = "factory-user"

	factoryKeyGithubToken  = "GITHUB_TOKEN"
	factoryKeyGithubLogin  = "GITHUB_LOGIN"
	factoryKeyGithubEmail  = "GITHUB_EMAIL"
	factoryKeyGeminiAPIKey = "GEMINI_API_KEY"
)

// reconcileFactoryUserSecret materializes the tenant's identity as the
// `factory-user` Secret consumed by factory CLI invocations. The GitHub token
// is re-read from the RepoWatch's github secret (rather than taken from the
// client config) so a token refreshed by PersistingTokenSource during this
// reconcile is picked up.
func (r *Reconciler) reconcileFactoryUserSecret(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, user *github.User) error {
	ghSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: repoWatch.Spec.GithubSecretName, Namespace: repoWatch.Namespace}, ghSecret); err != nil {
		return err
	}

	var token []byte
	for _, key := range []string{ManualPATKey, OAuthPATKey, "pat"} {
		if v, ok := ghSecret.Data[key]; ok && len(v) > 0 {
			token = v
			break
		}
	}
	if len(token) == 0 {
		return fmt.Errorf("no github token in secret %s/%s", repoWatch.Namespace, repoWatch.Spec.GithubSecretName)
	}

	email := user.GetEmail()
	if email == "" {
		email = fmt.Sprintf("%s@users.noreply.github.com", user.GetLogin())
	}

	data := map[string][]byte{
		factoryKeyGithubToken: token,
		factoryKeyGithubLogin: []byte(user.GetLogin()),
		factoryKeyGithubEmail: []byte(email),
	}

	geminiSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: k8s.GeminiSecretName, Namespace: repoWatch.Namespace}, geminiSecret); err == nil {
		for _, key := range []string{"gemini", factoryKeyGeminiAPIKey} {
			if v, ok := geminiSecret.Data[key]; ok && len(v) > 0 {
				data[factoryKeyGeminiAPIKey] = v
				break
			}
		}
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      FactoryUserSecretName,
			Namespace: repoWatch.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "repo-agent"},
		},
		Data: data,
	}

	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: FactoryUserSecretName, Namespace: repoWatch.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if reflect.DeepEqual(existing.Data, desired.Data) {
		return nil
	}
	existing.Data = desired.Data
	return r.Update(ctx, existing)
}
