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

package repoboard

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

// Wire contract with the factory CLI (identity secret keys) and the
// existing tenant secrets. Matches pkg/controllers/repowatch, which retires
// with RepoWatch.
const (
	githubSecretName = "github-pat"
	geminiSecretName = "gemini-vscode-tokens"

	factoryUserSecretName  = "factory-user"
	factoryKeyGithubToken  = "GITHUB_TOKEN"
	factoryKeyGithubLogin  = "GITHUB_LOGIN"
	factoryKeyGithubEmail  = "GITHUB_EMAIL"
	factoryKeyGeminiAPIKey = "GEMINI_API_KEY"
)

func parseRepoURL(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// githubTokenFromSecret resolves a member's GitHub token with the standard
// precedence (manual_pat > oauth_pat > pat).
func githubTokenFromSecret(secret *corev1.Secret) string {
	for _, key := range []string{"manual_pat", "oauth_pat", "pat"} {
		if v, ok := secret.Data[key]; ok && len(v) > 0 {
			return string(v)
		}
	}
	return ""
}

// memberGithubClient builds a GitHub client from the member namespace's
// github-pat secret.
func (r *Reconciler) memberGithubClient(ctx context.Context, namespace string) (*github.Client, string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: namespace}, secret); err != nil {
		return nil, "", err
	}
	token := githubTokenFromSecret(secret)
	if token == "" {
		return nil, "", fmt.Errorf("no github token in secret %s/%s", namespace, githubSecretName)
	}
	tc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	return clients.NewGitHubClientFromHTTP(tc), token, nil
}

// ensureFactoryUserSecret materializes the member's identity as the
// factory-user Secret factory invocations consume, refreshed each reconcile
// so token rotations propagate. (This duty moves to the login/bootstrap path
// when the repowatch controller retires; boards keep their own sync so they
// work standalone meanwhile.)
func (r *Reconciler) ensureFactoryUserSecret(ctx context.Context, namespace string, user *github.User) error {
	ghSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: namespace}, ghSecret); err != nil {
		return err
	}
	token := githubTokenFromSecret(ghSecret)
	if token == "" {
		return fmt.Errorf("no github token in secret %s/%s", namespace, githubSecretName)
	}

	email := user.GetEmail()
	if email == "" {
		email = fmt.Sprintf("%s@users.noreply.github.com", user.GetLogin())
	}
	data := map[string][]byte{
		factoryKeyGithubToken: []byte(token),
		factoryKeyGithubLogin: []byte(user.GetLogin()),
		factoryKeyGithubEmail: []byte(email),
	}
	geminiSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: geminiSecretName, Namespace: namespace}, geminiSecret); err == nil {
		for _, key := range []string{"gemini", factoryKeyGeminiAPIKey} {
			if v, ok := geminiSecret.Data[key]; ok && len(v) > 0 {
				data[factoryKeyGeminiAPIKey] = v
				break
			}
		}
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      factoryUserSecretName,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "repo-agent"},
		},
		Data: data,
	}
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: factoryUserSecretName, Namespace: namespace}, existing)
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
