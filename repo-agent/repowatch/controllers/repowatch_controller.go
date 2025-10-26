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
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/repowatch/api/v1alpha1"
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
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=issuesandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *RepoWatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	repoWatch := &reviewv1alpha1.RepoWatch{}
	if err := r.Get(ctx, req.NamespacedName, repoWatch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch RepoWatch")
		return ctrl.Result{}, err
	}

	// Get GitHub token
	config, err := r.getGitHubConfig(ctx, repoWatch)
	if err != nil {
		log.Error(err, "unable to get github token")
		return ctrl.Result{}, err
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: config["pat"]},
	)
	tc := oauth2.NewClient(ctx, ts)
	ghClient := github.NewClient(tc)

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		log.Error(err, "unable to parse repo url")
		return ctrl.Result{}, err
	}

	var reconcileErr error
	// Reconcile Reviews for Pull Requests
	if err := r.reconcileReviews(ctx, repoWatch, ghClient, owner, repo); err != nil {
		log.Error(err, "unable to reconcile reviews")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	// Reconcile Issues
	for _, handler := range repoWatch.Spec.IssueHandlers {
		if err := r.reconcileIssuesForHandler(ctx, config, handler, repoWatch, ghClient, owner, repo); err != nil {
			log.Error(err, "unable to reconcile issues for handler: "+handler.Name)
			reconcileErr = errors.Join(reconcileErr, err)
			// Continue to next reconciliation
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * time.Duration(repoWatch.Spec.PollIntervalSeconds)}, reconcileErr
}

func (r *RepoWatchReconciler) reconcileReviews(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, client *github.Client, owner string, repo string) error {
	log := log.FromContext(ctx)

	// Get open PRs
	prs, _, err := client.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open"})
	if err != nil {
		log.Error(err, "unable to list pull requests")
		return err
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
		return err
	}

	// Reconcile
	if err := r.reconcileReviewSandboxes(ctx, repoWatch, prs, sandboxList); err != nil {
		log.Error(err, "unable to reconcile sandboxes")
		return err
	}

	return nil
}

func (r *RepoWatchReconciler) reconcileIssuesForHandler(ctx context.Context, githubConfig map[string]string, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, client *github.Client, owner string, repo string) error {
	log := log.FromContext(ctx)

	listOptions := &github.IssueListByRepoOptions{
		State: "open",
	}
	if len(handler.Labels) == 0 {
		listOptions.Labels = handler.Labels
	}

	// Get open issues with specified labels
	issues, _, err := client.Issues.ListByRepo(ctx, owner, repo, listOptions)
	if err != nil {
		log.Error(err, "unable to list issues")
		return err
	}

	// filter issues that are pullrequests
	var repoIssues []*github.Issue
	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}
		repoIssues = append(repoIssues, issue)
	}

	// Get existing sandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)

	// TODO filter by handler and or namespace
	if err := r.List(ctx, sandboxList); err != nil {
		log.Error(err, "unable to list ReviewSandboxes for triage")
		return err
	}

	// Get the github user name and email for the given token
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		log.Error(err, "unable to get current user")
		return err
	}
	if githubConfig["name"] != "" {
		user.Name = github.String(githubConfig["name"])
	}
	if githubConfig["email"] != "" {
		user.Email = github.String(githubConfig["email"])
	}
	log.Info("Obtained current user", "user", *user)

	// Reconcile
	if err := r.reconcileIssueHandlerSandboxes(ctx, user, handler, repoWatch, repoIssues, sandboxList); err != nil {
		log.Error(err, "unable to reconcile triage sandboxes")
		return err
	}

	return nil
}

func (r *RepoWatchReconciler) getGitHubConfig(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch) (map[string]string, error) {
	secret := &corev1.Secret{}
	secretName := repoWatch.Spec.GithubSecretName
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: repoWatch.Namespace}, secret); err != nil {
		return nil, err
	}
	githubConfig := map[string]string{
		"name":  "",
		"email": "",
	}
	_, ok := secret.Data["pat"]
	if !ok {
		return nil, fmt.Errorf("'pat' not found in secret %s", secretName)
	}
	githubConfig["pat"] = string(secret.Data["pat"])

	_, ok = secret.Data["name"]
	if ok {
		githubConfig["name"] = string(secret.Data["name"])
	}

	_, ok = secret.Data["email"]
	if ok {
		githubConfig["email"] = string(secret.Data["email"])
	}
	return githubConfig, nil
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

func (r *RepoWatchReconciler) reconcileReviewSandboxes(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, prs []*github.PullRequest, sandboxes *unstructured.UnstructuredList) error {
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
			if activeSandboxes < repoWatch.Spec.Review.MaxActiveSandboxes {
				log.Info("creating sandbox for pr", "pr", *pr.Number)
				if err := r.createReviewSandboxForPR(ctx, repoWatch, pr); err != nil {
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

func (r *RepoWatchReconciler) reconcileIssueHandlerSandboxes(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issues []*github.Issue, sandboxes *unstructured.UnstructuredList) error {
	log := log.FromContext(ctx)
	activeSandboxes := 0
	watchedIssues := []reviewv1alpha1.WatchedIssue{}
	pendingIssues := []reviewv1alpha1.PendingIssue{}

	// Cleanup closed issues
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

		// split the sandbox name by "-issue-" and get the second part
		parts := strings.Split(sandbox.GetName(), "-issue-")
		if len(parts) < 2 {
			log.Error(fmt.Errorf("invalid sandbox name format"), "unable to parse issue number from sandbox name", "sandbox", sandbox.GetName())
			continue
		}

		// parts[1] may contain additional "-" if handler name is included, so split again by "-" and take the first part
		parts = strings.Split(parts[1], "-")
		if len(parts) < 2 {
			log.Error(fmt.Errorf("invalid sandbox name format"), "unable to parse handler name from sandbox name", "sandbox", sandbox.GetName())
			continue
		}

		issueNumber, err := strconv.Atoi(parts[0])
		if err != nil {
			log.Error(err, "unable to parse issue number from sandbox name", "sandbox", sandbox.GetName())
			continue
		}
		handlerName := parts[1]

		if handlerName != handler.Name {
			continue
		}

		found := false
		for _, issue := range issues {
			if *issue.Number == issueNumber {
				found = true
				break
			}
		}

		if !found {
			log.Info("deleting sandbox for closed issue", "issue", issueNumber)
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			}
		}
	}

	// Create new sandboxes
	for _, issue := range issues {
		sandboxName := fmt.Sprintf("%s-issue-%d-%s", strings.Split(repoWatch.Spec.RepoURL, "/")[len(strings.Split(repoWatch.Spec.RepoURL, "/"))-1], *issue.Number, handler.Name)
		sandboxExists := false
		for _, sandbox := range sandboxes.Items {
			if sandbox.GetName() == sandboxName {
				sandboxExists = true
				replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
				if err != nil || !found {
					log.Error(err, "unable to get replicas for sandbox", "sandbox", sandbox.GetName())
					break
				}
				if replicas > 0 {
					activeSandboxes++
				}
				watchedIssues = append(watchedIssues, reviewv1alpha1.WatchedIssue{
					Number:      *issue.Number,
					SandboxName: sandboxName,
					Status:      "Active",
				})
				break
			}
		}

		if !sandboxExists {
			if activeSandboxes < handler.MaxActiveSandboxes {
				log.Info("creating sandbox for issue", "issue", *issue.Number)
				if err := r.createSandboxForIssueHandler(ctx, user, handler, repoWatch, issue); err != nil {
					log.Error(err, "unable to create sandbox for issue", "issue", *issue.Number)
				} else {
					activeSandboxes++
					watchedIssues = append(watchedIssues, reviewv1alpha1.WatchedIssue{
						Number:      *issue.Number,
						SandboxName: sandboxName,
						Status:      "Creating",
					})
				}
			} else {
				pendingIssues = append(pendingIssues, reviewv1alpha1.PendingIssue{
					Number: *issue.Number,
					Status: "Pending",
				})
			}
		}
	}

	if repoWatch.Status.WatchedIssues == nil {
		repoWatch.Status.WatchedIssues = make(map[string][]reviewv1alpha1.WatchedIssue)
	}
	if repoWatch.Status.PendingIssues == nil {
		repoWatch.Status.PendingIssues = make(map[string][]reviewv1alpha1.PendingIssue)
	}
	repoWatch.Status.WatchedIssues[handler.Name] = watchedIssues
	repoWatch.Status.PendingIssues[handler.Name] = pendingIssues

	return r.Status().Update(ctx, repoWatch)
}

func (r *RepoWatchReconciler) generateReviewPrompt(repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) (string, error) {
	promptTmpl := "You are an expert kubernetes developer who is helping with code reviews. Please look at the PR {{.Number}} linked at {{.HTMLURL}} provide a review feedback."
	if repoWatch.Spec.Review.Gemini.Prompt != "" {
		promptTmpl = repoWatch.Spec.Review.Gemini.Prompt
	}
	tmpl, err := template.New("myTemplate").Parse(promptTmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, pr)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *RepoWatchReconciler) generateIssueHandlerPrompt(handler reviewv1alpha1.IssueHandlerSpec, issue *github.Issue) (string, error) {
	// promptTmpl := "You are an expert kubernetes developer who is helping with bug triage. Please look at the issue {{.Number}} linked at {{.HTMLURL}} and provide a triage summary. Please suggest possible causes and solutions."
	promptTmpl := handler.Gemini.Prompt
	tmpl, err := template.New("myTemplate").Parse(promptTmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, issue)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *RepoWatchReconciler) createReviewSandboxForPR(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) error {
	log := log.FromContext(ctx)
	repoName := strings.Split(repoWatch.Spec.RepoURL, "/")[len(strings.Split(repoWatch.Spec.RepoURL, "/"))-1]
	sandboxName := fmt.Sprintf("%s-pr-%d", repoName, *pr.Number)

	prompt, err := r.generateReviewPrompt(repoWatch, pr)
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
					"configdirRef": repoWatch.Spec.Review.Gemini.ConfigdirRef,
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

	if repoWatch.Spec.Review.DevcontainerConfigRef != "" {
		if err := unstructured.SetNestedField(sandbox.Object, repoWatch.Spec.Review.DevcontainerConfigRef, "spec", "devcontainerConfigRef"); err != nil {
			return err
		}
	}

	if err := controllerutil.SetControllerReference(repoWatch, sandbox, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, sandbox)
}

func (r *RepoWatchReconciler) createSandboxForIssueHandler(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issue *github.Issue) error {
	log := log.FromContext(ctx)
	repoName := strings.Split(repoWatch.Spec.RepoURL, "/")[len(strings.Split(repoWatch.Spec.RepoURL, "/"))-1]
	sandboxName := fmt.Sprintf("%s-issue-%d-%s", repoName, *issue.Number, handler.Name)

	prompt, err := r.generateIssueHandlerPrompt(handler, issue)
	if err != nil {
		return err
	}

	cloneURL := strings.Replace(*issue.RepositoryURL, "api.github.com/repos", "github.com", 1) + ".git"
	// Get repo name which is the string after the last /
	parts := strings.Split(cloneURL, "/")
	repoName = parts[len(parts)-1]
	//originURL := fmt.Sprintf("https://%s:%s@github.com/%s/%s", user.GetLogin(), githubConfig["pat"], user.GetLogin(), repoName)
	originURL := fmt.Sprintf("github.com/%s/%s", user.GetLogin(), repoName)

	log.Info("Generated sandbox for Issue", "issue", *issue)
	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"review.gemini.google.com/repowatch": repoWatch.Name,
					"review.gemini.google.com/handler":   handler.Name,
				},
			},
			"spec": map[string]interface{}{
				"gemini": map[string]interface{}{
					"configdirRef": handler.Gemini.ConfigdirRef,
					"prompt":       prompt,
				},
				"source": map[string]interface{}{
					// change *issue.RepositoryURL from https://api.github.com/repos/org/repo-name to https://github.com/org/repo-name.git
					"cloneURL": cloneURL,
					"htmlURL":  *issue.HTMLURL,
					"issue":    fmt.Sprintf("%d", *issue.Number),
					"title":    *issue.Title,
					"repo":     repoWatch.GetName(),
					"handler":  handler.Name,
				},
				"destination": map[string]interface{}{
					"pushEnabled": handler.PushEnabled,
					"branch":      fmt.Sprintf("issue-%d-%s", *issue.Number, handler.Name),
					"origin":      originURL,
					"user": map[string]interface{}{
						"login": user.GetLogin(),
						"name":  user.GetName(),
						"email": user.GetEmail(),
					},
				},
				"gateway": map[string]interface{}{
					"httpEnabled": true,
				},
				"replicas": int64(1),
			},
		},
	}

	if handler.DevcontainerConfigRef != "" {
		if err := unstructured.SetNestedField(sandbox.Object, handler.DevcontainerConfigRef, "spec", "devcontainerConfigRef"); err != nil {
			return err
		}
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
