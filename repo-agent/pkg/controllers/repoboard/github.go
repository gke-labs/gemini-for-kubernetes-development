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
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/google/go-github/v39/github"
	"github.com/gregjones/httpcache"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

// Wire contract with the factory CLI (identity secret keys) and the
// existing tenant secrets.
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
	return githubClientFromToken(ctx, token), token, nil
}

// executorToken resolves a member's GitHub token from their namespace
// (namespace == GitHub login by tenancy convention). Errors mean the member
// never onboarded.
func (r *Reconciler) executorToken(ctx context.Context, namespace string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: namespace}, secret); err != nil {
		return "", err
	}
	token := githubTokenFromSecret(secret)
	if token == "" {
		return "", fmt.Errorf("no github token in secret %s/%s", namespace, githubSecretName)
	}
	return token, nil
}

// identityFromSecret reads the member identity recorded alongside the PAT in
// the github-pat secret (schema keys "name" and "email"), for tokens that
// cannot answer GET /user (e.g. CI installation tokens).
func (r *Reconciler) identityFromSecret(ctx context.Context, namespace string) (string, string, bool) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: namespace}, secret); err != nil {
		return "", "", false
	}
	login := string(secret.Data["name"])
	if login == "" {
		return "", "", false
	}
	return login, string(secret.Data["email"]), true
}

// ghConditionalCache backs conditional requests (ETags) for every
// controller-side GitHub read: the reconcile loop polls each board about
// once a minute, and GitHub answers unchanged resources with 304 — which
// costs ZERO rate-limit quota. Steady-state reconciles of a quiet repo
// become nearly free. Responses carry Vary: Authorization, so entries key
// per token and never leak across members.
var ghConditionalCache = httpcache.NewMemoryCache()

func githubClientFromToken(_ context.Context, token string) *github.Client {
	cached := httpcache.NewTransport(ghConditionalCache)
	cached.Transport = &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})}
	return clients.NewGitHubClientFromHTTP(&http.Client{Transport: cached})
}

// ensureFactoryUserSecret materializes a member's identity as the
// factory-user Secret factory invocations consume, refreshed each reconcile
// so token rotations propagate. Email may be empty (noreply fallback).
func (r *Reconciler) ensureFactoryUserSecret(ctx context.Context, namespace, login, email string) error {
	token, err := r.executorToken(ctx, namespace)
	if err != nil {
		return err
	}
	if email == "" {
		email = fmt.Sprintf("%s@users.noreply.github.com", login)
	}
	data := map[string][]byte{
		factoryKeyGithubToken: []byte(token),
		factoryKeyGithubLogin: []byte(login),
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
	err = r.Get(ctx, types.NamespacedName{Name: factoryUserSecretName, Namespace: namespace}, existing)
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
