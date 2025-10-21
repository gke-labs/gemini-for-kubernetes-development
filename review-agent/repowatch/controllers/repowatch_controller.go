/*
Copyright 2025.

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

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/review-agent/repowatch/api/v1alpha1"
)

// RepoWatchReconciler reconciles a RepoWatch object
type RepoWatchReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/finalizers,verbs=update
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=reviewsandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *RepoWatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	repoWatch := &reviewv1alpha1.RepoWatch{}
	repoWatchObject := unstructured.Unstructured{}
	repoWatchGVK := schema.GroupVersionKind{
		Group:   "review.gemini.google.com",
		Version: "v1alpha1",
		Kind:    "RepoWatch",
	}
	repoWatchObject.SetGroupVersionKind(repoWatchGVK)
	if err := r.Get(ctx, req.NamespacedName, repoWatch); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch RepoWatch")
		return ctrl.Result{}, err
	}

	// Get GitHub token
	token, err := r.getGitHubToken(ctx, repoWatch)
	if err != nil {
		log.Error(err, "unable to get github token")
		return ctrl.Result{}, err
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		log.Error(err, "unable to parse repo url")
		return ctrl.Result{}, err
	}

	// Get open PRs
	prs, _, err := client.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open"})
	if err != nil {
		log.Error(err, "unable to list pull requests")
		return ctrl.Result{}, err
	}

	// Get existing sandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)

	if err := r.List(ctx, sandboxList); err != nil {
		log.Error(err, "unable to list ReviewSandboxes")
		return ctrl.Result{}, err
	}

	// Reconcile
	if err := r.reconcileSandboxes(ctx, repoWatch, prs, sandboxList); err != nil {
		log.Error(err, "unable to reconcile sandboxes")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Second * time.Duration(repoWatch.Spec.PollIntervalSeconds)}, nil
}

func (r *RepoWatchReconciler) getGitHubToken(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch) (string, error) {
	secret := &corev1.Secret{}
	secretName := repoWatch.Spec.GithubSecretRef.Name
	secretKey := repoWatch.Spec.GithubSecretRef.Key
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: repoWatch.Namespace}, secret); err != nil {
		return "", err
	}
	token, ok := secret.Data[secretKey]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s", secretKey, secretName)
	}
	return string(token), nil
}

func parseRepoURL(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	return parts[0], parts[1], nil
}

func (r *RepoWatchReconciler) reconcileSandboxes(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, prs []*github.PullRequest, sandboxes *unstructured.UnstructuredList) error {
	log := log.FromContext(ctx)
	activeSandboxes := 0
	watchedPRs := []reviewv1alpha1.WatchedPR{}
	pendingPRs := []reviewv1alpha1.PendingPR{}

	// Cleanup closed PRs
	for _, sandbox := range sandboxes.Items {
		isOwned := false
		for _, ownerRef := range sandbox.GetOwnerReferences() {
			if ownerRef.UID == repoWatch.UID {
				isOwned = true
				break
			}
		}
		if !isOwned {
			continue
		}

		prNumber, err := strconv.Atoi(strings.Split(sandbox.GetName(), "-pr-")[1])
		if err != nil {
			log.Error(err, "unable to parse pr number from sandbox name", "sandbox", sandbox.GetName())
			continue
		}

		found := false
		for _, pr := range prs {
			if *pr.Number == prNumber {
				found = true
				break
			}
		}

		if !found {
			log.Info("deleting sandbox for closed pr", "pr", prNumber)
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			}
		}
	}

	// Create new sandboxes
	for _, pr := range prs {
		sandboxName := fmt.Sprintf("%s-pr-%d", strings.Split(repoWatch.Spec.RepoURL, "/")[len(strings.Split(repoWatch.Spec.RepoURL, "/"))-1], *pr.Number)
		sandboxExists := false
		for _, sandbox := range sandboxes.Items {
			if sandbox.GetName() == sandboxName {
				sandboxExists = true
				// Check if replica count > 0
				replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
				if err != nil || !found {
					log.Error(err, "unable to get replicas for sandbox", "sandbox", sandbox.GetName())
					break
				}
				if replicas > 0 {
					activeSandboxes++
				}
				watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
					Number:      *pr.Number,
					SandboxName: sandboxName,
					Status:      "Active",
				})
				break
			}
		}

		if !sandboxExists {
			if activeSandboxes < repoWatch.Spec.MaxActiveSandboxes {
				log.Info("creating sandbox for pr", "pr", *pr.Number)
				if err := r.createSandboxForPR(ctx, repoWatch, pr); err != nil {
					log.Error(err, "unable to create sandbox for pr", "pr", *pr.Number)
				} else {
					activeSandboxes++
					watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
						Number:      *pr.Number,
						SandboxName: sandboxName,
						Status:      "Creating",
					})
				}
			} else {
				pendingPRs = append(pendingPRs, reviewv1alpha1.PendingPR{
					Number: *pr.Number,
					Status: "Pending",
				})
			}
		}
	}

	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	repoWatch.Status.WatchedPRs = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs

	return r.Status().Update(ctx, repoWatch)
}

func (r *RepoWatchReconciler) generatePullRequestPrompt(repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) (string, error) {
	promptTmpl := "You are an expert kubernetes developer who is helping with code reviews. Please look at the PR {{.Number}} linked at {{.HTMLURL}} provide a review feedback."
	if repoWatch.Spec.Gemini.Prompt != "" {
		promptTmpl = repoWatch.Spec.Gemini.Prompt
	}
	tmpl, err := template.New("myTemplate").Parse(promptTmpl)
	if err != nil {
		return "", err
	}

	// Create a bytes.Buffer to capture the template output
	var buf bytes.Buffer
	// Execute the template, writing the output to the buffer
	err = tmpl.Execute(&buf, pr)
	if err != nil {
		return "", err
	}

	// Get the rendered string from the buffer
	prompt := buf.String()

	return prompt, nil
}

func (r *RepoWatchReconciler) createSandboxForPR(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) error {
	log := log.FromContext(ctx)
	repoName := strings.Split(repoWatch.Spec.RepoURL, "/")[len(strings.Split(repoWatch.Spec.RepoURL, "/"))-1]
	sandboxName := fmt.Sprintf("%s-pr-%d", repoName, *pr.Number)

	prompt, err := r.generatePullRequestPrompt(repoWatch, pr)
	if err != nil {
		return err
	}

	log.Info("Generated sandbox for PR", "pr", *pr)
	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"review.gemini.google.com/repowatch": repoWatch.Name,
				},
			},
			"spec": map[string]interface{}{
				"gemini": map[string]interface{}{
					"configdirRef": repoWatch.Spec.Gemini.ConfigdirRef,
					"prompt":       prompt,
				},
				"source": map[string]interface{}{
					"cloneURL": fmt.Sprintf("%s#refs/heads/%s", *pr.Head.Repo.CloneURL, *pr.Head.Ref),
					"htmlURL":  *pr.HTMLURL,
					"pr":       fmt.Sprintf("%d", *pr.Number),
					"title":    *pr.Title,
					"repo":     repoWatch.GetName(),
				},
				"gateway": map[string]interface{}{
					"httpEnabled": true,
				},
				"replicas": int64(1),
			},
		},
	}

	if err := controllerutil.SetControllerReference(repoWatch, sandbox, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, sandbox)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RepoWatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&reviewv1alpha1.RepoWatch{}).
		// Owns(&reviewv1alpha1.ReviewSandbox{}).
		Complete(r)
}
