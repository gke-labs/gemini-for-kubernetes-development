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
	"math/rand"
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

// Character set for the random string
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// We create a new *rand.Rand instance seeded with the current time.
// This is crucial to get different results on each program execution.
var seededRand = rand.New(
	rand.NewSource(time.Now().UnixNano()))

type githubClientFactory func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error)

func NewGithubClient(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error) {
	secret := &corev1.Secret{}
	secretName := repoWatch.Spec.GithubSecretName
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: repoWatch.Namespace}, secret); err != nil {
		return nil, nil, err
	}
	githubConfig := map[string]string{
		"name":  "",
		"email": "",
	}
	pat, ok := secret.Data["pat"]
	if !ok {
		return nil, nil, fmt.Errorf("\"pat\" not found in secret %s", secretName)
	}
	githubConfig["pat"] = string(pat)

	_, ok = secret.Data["name"]
	if ok {
		githubConfig["name"] = string(secret.Data["name"])
	}

	_, ok = secret.Data["email"]
	if ok {
		githubConfig["email"] = string(secret.Data["email"])
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: string(pat)},
	)
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc), githubConfig, nil
}

// RepoWatchReconciler reconciles a RepoWatch object
type RepoWatchReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	NewGithubClient githubClientFactory
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

	ghClient, githubConfig, err := r.NewGithubClient(ctx, r.Client, repoWatch)
	if err != nil {
		log.Error(err, "unable to create github client")
		return ctrl.Result{}, err
	}

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
	if err := r.reconcileIssues(ctx, githubConfig, repoWatch, ghClient, owner, repo); err != nil {
		log.Error(err, "unable to reconcile issues")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	return ctrl.Result{RequeueAfter: time.Second * time.Duration(repoWatch.Spec.PollIntervalSeconds)}, reconcileErr
}

func (r *RepoWatchReconciler) reconcileReviews(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string) error {
	log := log.FromContext(ctx)
	log.Info("reconciling reviews")

	explicitPRs := r.getExplicitPRs(ctx, ghClient, repoWatch, owner, repo)

	prs, err := r.listOpenPRs(ctx, ghClient, owner, repo)
	if err != nil {
		return err
	}

	prs = r.filterPRsByLabels(prs, repoWatch)
	prs = r.deduplicatePRs(prs, explicitPRs)
	prs = r.sortPRs(ctx, ghClient, prs, repoWatch)

	// Log repoIssues and sandboxList for debug purposes
	prsStr := []string{}
	for _, pr := range prs {
		prsStr = append(prsStr, fmt.Sprintf("%d", *pr.Number))
	}
	log.V(4).Info("PRs:", "prs", prsStr)

	// Get existing sandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)

	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace)); err != nil {
		log.Error(err, "unable to list ReviewSandboxes")
		return err
	}
	// Reconcile
	if err := r.reconcileReviewSandboxes(ctx, repoWatch, explicitPRs, prs, sandboxList); err != nil {
		log.Error(err, "unable to reconcile sandboxes")
		return err
	}

	return nil
}

func (r *RepoWatchReconciler) getExplicitPRs(ctx context.Context, ghClient *github.Client, repoWatch *reviewv1alpha1.RepoWatch, owner, repo string) []*github.PullRequest {
	var explicitPRs []*github.PullRequest
	log := log.FromContext(ctx)
	if len(repoWatch.Spec.Review.PullRequests) > 0 {
		// If specific PRs are requested, fetch them directly
		for _, prNumber := range repoWatch.Spec.Review.PullRequests {
			pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNumber)
			if err != nil {
				log.Error(err, "unable to get pull request", "prNumber", prNumber)
				// Continue to the next PR if there's an error fetching a specific one.
				continue
			}
			explicitPRs = append(explicitPRs, pr)
		}
	}
	return explicitPRs
}

func (r *RepoWatchReconciler) listOpenPRs(ctx context.Context, ghClient *github.Client, owner, repo string) ([]*github.PullRequest, error) {
	var prs []*github.PullRequest
	log := log.FromContext(ctx)
	// Otherwise, list open PRs
	opts := &github.PullRequestListOptions{
		State:       "open",
		Sort:        "created",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		list, resp, err := ghClient.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			log.Error(err, "unable to list pull requests")
			return nil, err
		}
		prs = append(prs, list...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return prs, nil
}

func (r *RepoWatchReconciler) filterPRsByLabels(prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch) []*github.PullRequest {
	// Filter by Labels
	if len(repoWatch.Spec.Review.Labels) > 0 {
		var filteredPRs []*github.PullRequest
		for _, pr := range prs {
			matches := false
			for _, labelSet := range repoWatch.Spec.Review.Labels {
				labelSetMatches := true
				for _, requiredLabel := range labelSet {
					hasRequiredLabel := false
					for _, prLabel := range pr.Labels {
						if prLabel.Name != nil && *prLabel.Name == requiredLabel {
							hasRequiredLabel = true
							break
						}
					}
					if !hasRequiredLabel {
						labelSetMatches = false
						break
					}
				}
				if labelSetMatches {
					matches = true
					break
				}
			}
			if matches {
				filteredPRs = append(filteredPRs, pr)
			}
		}
		return filteredPRs
	}
	return prs
}

func (r *RepoWatchReconciler) deduplicatePRs(prs []*github.PullRequest, explicitPRs []*github.PullRequest) []*github.PullRequest {
	// Filter out duplicates from explicitPRs
	var filteredPRs []*github.PullRequest
	for _, pr := range prs {
		found := false
		for _, explicitPR := range explicitPRs {
			if *pr.Number == *explicitPR.Number {
				found = true
				break
			}
		}
		if !found {
			filteredPRs = append(filteredPRs, pr)
		}
	}
	return filteredPRs
}

func (r *RepoWatchReconciler) sortPRs(ctx context.Context, ghClient *github.Client, prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch) []*github.PullRequest {
	log := log.FromContext(ctx)
	// Sort by PreferAssignedToSelf
	// TODO(barney-s): May be rate limited. Cache the user info.
	if repoWatch.Spec.Review.PreferAssignedToSelf {
		user, _, err := ghClient.Users.Get(ctx, "")
		if err != nil {
			log.Error(err, "unable to get current user for sorting PRs")
			return prs
		}
		if user.Login == nil {
			log.Error(errors.New("user login is nil"), "unable to get current user login for sorting PRs")
			return prs
		}
		var assignedToMe []*github.PullRequest
		var others []*github.PullRequest
		for _, pr := range prs {
			isAssigned := false
			for _, assignee := range pr.Assignees {
				if assignee.Login != nil && *assignee.Login == *user.Login {
					isAssigned = true
					break
				}
			}
			if isAssigned {
				assignedToMe = append(assignedToMe, pr)
			} else {
				others = append(others, pr)
			}
		}
		return append(assignedToMe, others...)
	}
	return prs
}

func (r *RepoWatchReconciler) reconcileIssues(ctx context.Context, githubConfig map[string]string, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string) error {
	log := log.FromContext(ctx)
	var reconcileErr error

	// Get existing sandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)

	// TODO filter by handler and or namespace
	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace)); err != nil {
		log.Error(err, "unable to list ReviewSandboxes")
		return err
	}

	// Get the github user name and email for the given token
	user, _, err := ghClient.Users.Get(ctx, "")
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

	for _, handler := range repoWatch.Spec.IssueHandlers {
		if err := r.reconcileIssuesForHandler(ctx, user, sandboxList, handler, repoWatch, ghClient, owner, repo, githubConfig); err != nil {
			log.Error(err, "unable to reconcile issues for handler: "+handler.Name)
			reconcileErr = errors.Join(reconcileErr, err)
			// Continue to next reconciliation
		}
	}
	return reconcileErr
}

func (r *RepoWatchReconciler) reconcileIssuesForHandler(ctx context.Context, user *github.User, sandboxList *unstructured.UnstructuredList, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, _ map[string]string) error {
	log := log.FromContext(ctx)

	listOptions := &github.IssueListByRepoOptions{
		State: "open",
	}
	if len(handler.Labels) != 0 {
		listOptions.Labels = handler.Labels
	}

	// Get open issues with specified labels
	issues, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, listOptions)
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

	// If the handler has a list of issues, filter the issues
	if len(handler.Issues) > 0 {
		var filteredIssues []*github.Issue
		for _, issue := range repoIssues {
			for _, issueNumber := range handler.Issues {
				if *issue.Number == issueNumber {
					filteredIssues = append(filteredIssues, issue)
					break
				}
			}
		}
		repoIssues = filteredIssues
	}

	// Log repoIssues and sandboxList for debug purposes
	issuesStr := []string{}
	for _, issue := range repoIssues {
		issuesStr = append(issuesStr, fmt.Sprintf("%d", *issue.Number))
	}
	sandboxesStr := []string{}
	for _, sandbox := range sandboxList.Items {
		sandboxesStr = append(sandboxesStr, sandbox.GetName())
	}
	log.Info("DEBUG INFO issues", "handler", handler.Name, "issues", issuesStr)
	log.Info("DEBUG INFO sandboxes", "handler", handler.Name, "sandboxes", sandboxesStr)

	// Workaround for https://github.com/gke-labs/gemini-for-kubernetes-development/issues/8
	if len(repoIssues) == 0 {
		log.Info("No issues found")
		return nil
	}
	// Reconcile
	if err := r.reconcileIssueHandlerSandboxes(ctx, user, handler, repoWatch, repoIssues, sandboxList); err != nil {
		log.Error(err, "unable to reconcile triage sandboxes")
		return err
	}

	return nil
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

func (r *RepoWatchReconciler) reconcileReviewSandboxes(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, explicitPRs []*github.PullRequest, prs []*github.PullRequest, sandboxes *unstructured.UnstructuredList) error {
	log := log.FromContext(ctx)
	log.Info("reconciling review sandboxes")

	// Filter sandboxes to only include those owned by this RepoWatch instance
	var ownedSandboxes []unstructured.Unstructured
	for _, sandbox := range sandboxes.Items {
		isOwned := false
		for _, ownerRef := range sandbox.GetOwnerReferences() {
			if ownerRef.UID == repoWatch.UID {
				isOwned = true
				break
			}
		}
		if isOwned {
			ownedSandboxes = append(ownedSandboxes, sandbox)
		}
	}

	// Pre-calculate active and total sandboxes from the owned list
	activeSandboxes := 0
	totalSandboxes := len(ownedSandboxes)
	for _, sandbox := range ownedSandboxes {
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err == nil && found && replicas > 0 {
			// Check if the PR is explicit, if so, dont count it towards the active sandbox limit
			prIsExplicit := false
			prNumber, err := strconv.Atoi(strings.Split(sandbox.GetName(), "-pr-")[1])
			if err != nil {
				log.Error(err, "unable to parse pr number from sandbox name", "sandbox", sandbox.GetName())
			} else {
				for _, explicitPR := range explicitPRs {
					if *explicitPR.Number == prNumber {
						prIsExplicit = true
						break
					}
				}
			}
			if !prIsExplicit {
				activeSandboxes++
			}
		}
	}

	watchedPRs := []reviewv1alpha1.WatchedPR{}
	pendingPRs := []reviewv1alpha1.PendingPR{}

	// Cleanup closed PRs from the owned list
	for _, sandbox := range ownedSandboxes {
		prNumber, err := strconv.Atoi(strings.Split(sandbox.GetName(), "-pr-")[1])
		if err != nil {
			log.Error(err, "unable to parse pr number from sandbox name", "sandbox", sandbox.GetName())
			continue
		}

		found := false
		for _, pr := range append(explicitPRs, prs...) {
			if *pr.Number == prNumber {
				found = true
				break
			}
		}

		if !found {
			log.Info("deleting sandbox for closed pr", "pr", prNumber)
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			} else {
				totalSandboxes--
			}
		}
	}

	// Process all open PRs and create sandboxes if within limits
	for _, pr := range append(explicitPRs, prs...) {
		sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, *pr.Number)
		sandboxExists := false
		for _, sandbox := range ownedSandboxes {
			if sandbox.GetName() == sandboxName {
				sandboxExists = true
				// Scale down check
				if repoWatch.Spec.Review.ReviewShutdownAfterMinutes > 0 {
					creationTimestamp := sandbox.GetCreationTimestamp()
					shutdownDuration := time.Minute * time.Duration(repoWatch.Spec.Review.ReviewShutdownAfterMinutes)
					if time.Since(creationTimestamp.Time) > shutdownDuration {
						replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
						if err == nil && found && replicas > 0 {
							log.Info("scaling down sandbox", "sandbox", sandbox.GetName())
							if err := unstructured.SetNestedField(sandbox.Object, int64(0), "spec", "replicas"); err != nil {
								log.Error(err, "unable to set replicas for sandbox", "sandbox", sandbox.GetName())
							} else {
								if err := r.Update(ctx, &sandbox); err != nil {
									log.Error(err, "unable to update sandbox", "sandbox", sandbox.GetName())
								}
							}
						}
					}
				}
				watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
					Number:      *pr.Number,
					SandboxName: sandboxName,
					Status:      "Active",
				})
				break
			}
		}

		if sandboxExists {
			continue
		}

		// Logic to create a new sandbox if it doesn't exist
		prIsExplicit := false
		for _, explicitPR := range explicitPRs {
			if *explicitPR.Number == *pr.Number {
				prIsExplicit = true
				break
			}
		}

		if prIsExplicit || ((activeSandboxes < repoWatch.Spec.Review.MaxActiveSandboxes) && (repoWatch.Spec.Review.MaxSandboxes == 0 || totalSandboxes < repoWatch.Spec.Review.MaxSandboxes)) {
			log.Info("creating sandbox for pr", "pr", *pr.Number)
			if err := r.createReviewSandboxForPR(ctx, repoWatch, pr); err != nil {
				log.Error(err, "unable to create sandbox for pr", "pr", *pr.Number)
			} else {
				activeSandboxes++
				totalSandboxes++
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

	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	repoWatch.Status.WatchedPRs = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs

	return r.Status().Update(ctx, repoWatch)
}

func (r *RepoWatchReconciler) reconcileIssueHandlerSandboxes(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issues []*github.Issue, sandboxes *unstructured.UnstructuredList) error {
	log := log.FromContext(ctx)

	// 1. Filter sandboxes to only include those owned by this RepoWatch instance and handler
	var ownedSandboxes []unstructured.Unstructured
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

		// Further filter by handler name encoded in the sandbox name
		parts := strings.Split(sandbox.GetName(), "-issue-")
		if len(parts) < 2 {
			continue
		}
		handlerName := strings.Split(parts[1], "-")[1]
		if handlerName == handler.Name {
			ownedSandboxes = append(ownedSandboxes, sandbox)
		}
	}

	// 2. Pre-calculate active and total sandboxes from the owned list.
	activeSandboxes := 0
	totalSandboxes := len(ownedSandboxes)
	for _, sandbox := range ownedSandboxes {
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err == nil && found && replicas > 0 {
			activeSandboxes++ // Count all active sandboxes
		}
	}

	watchedIssues := []reviewv1alpha1.WatchedIssue{}
	pendingIssues := []reviewv1alpha1.PendingIssue{}

	// 3. Cleanup closed issues from the owned list
	for _, sandbox := range ownedSandboxes {
		parts := strings.Split(sandbox.GetName(), "-issue-")
		issueNumber, err := strconv.Atoi(strings.Split(parts[1], "-")[0])
		if err != nil {
			log.Error(err, "unable to parse issue number from sandbox name", "sandbox", sandbox.GetName())
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
			} else {
				totalSandboxes--
			}
		}
	}

	// 4. Process all open issues and create sandboxes if within limits
	for _, issue := range issues {
		sandboxName := fmt.Sprintf("%s-issue-%d-%s", repoWatch.Name, *issue.Number, handler.Name)
		sandboxExists := false
		for _, sandbox := range ownedSandboxes {
			if sandbox.GetName() == sandboxName {
				sandboxExists = true
				// Scale down check
				if handler.IssueShutdownAfterMinutes > 0 {
					creationTimestamp := sandbox.GetCreationTimestamp()
					shutdownDuration := time.Minute * time.Duration(handler.IssueShutdownAfterMinutes)
					if time.Since(creationTimestamp.Time) > shutdownDuration {
						replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
						if err == nil && found && replicas > 0 {
							log.Info("scaling down issue sandbox", "sandbox", sandbox.GetName())
							if err := unstructured.SetNestedField(sandbox.Object, int64(0), "spec", "replicas"); err != nil {
								log.Error(err, "unable to set replicas for sandbox", "sandbox", sandbox.GetName())
							} else {
								if err := r.Update(ctx, &sandbox); err != nil {
									log.Error(err, "unable to update sandbox", "sandbox", sandbox.GetName())
								}
							}
						}
					}
				}
				watchedIssues = append(watchedIssues, reviewv1alpha1.WatchedIssue{
					Number:      *issue.Number,
					SandboxName: sandboxName,
					Status:      "Active",
				})
				break
			}
		}

		if sandboxExists {
			continue
		}

		if activeSandboxes < handler.MaxActiveSandboxes && (handler.MaxSandboxes == 0 || totalSandboxes < handler.MaxSandboxes) {
			log.Info("creating sandbox for issue", "issue", *issue.Number)
			if err := r.createSandboxForIssueHandler(ctx, user, handler, repoWatch, issue); err != nil {
				log.Error(err, "unable to create sandbox for issue", "issue", *issue.Number)
			} else {
				activeSandboxes++
				totalSandboxes++
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

// generateReviewPrompt generates a prompt for a pull request review.
// It uses the prompt specified in the RepoWatch CRD, and if it is not
// specified, it uses a default prompt.
func (r *RepoWatchReconciler) generateReviewPrompt(repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) (string, error) {
	// Level 1 substitution
	promptTmpl := reviewPromptTemplate

	templateVar := struct {
		github.PullRequest
		Prompt string
	}{
		PullRequest: *pr,
		Prompt:      repoWatch.Spec.Review.LLM.Prompt,
	}

	lvl1, err := template.New("lvl1").Parse(promptTmpl)
	if err != nil {
		return "", err
	}

	var level1 bytes.Buffer
	err = lvl1.Execute(&level1, templateVar)
	if err != nil {
		return "", err
	}

	// Level 2 subsitution
	tmpl, err := template.New("lvl2").Parse(level1.String())
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

// generateIssueHandlerPrompt generates a prompt for an issue handler.
// It uses the prompt specified in the RepoWatch CRD.
func (r *RepoWatchReconciler) generateIssueHandlerPrompt(handler reviewv1alpha1.IssueHandlerSpec, issue *github.Issue) (string, error) {
	// promptTmpl := "You are an expert kubernetes developer who is helping with bug triage. Please look at the issue {{.Number}} linked at {{.HTMLURL}} and provide a triage summary. Please suggest possible causes and solutions."
	promptTmpl := handler.LLM.Prompt
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

// createReviewSandboxForPR creates a ReviewSandbox for a pull request.
// It uses the LLM configuration from the RepoWatch CRD to configure the
// sandbox.
func (r *RepoWatchReconciler) createReviewSandboxForPR(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) error {
	log := log.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, *pr.Number)

	prompt, err := r.generateReviewPrompt(repoWatch, pr)
	if err != nil {
		return err
	}

	log.Info("Generated sandbox for PR", "pr", *pr, "llm.provider", repoWatch.Spec.Review.LLM.Provider)
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
				"llmBackend": map[string]interface{}{
					"name": repoWatch.Spec.Review.LLM.Provider,
				},
				"llm": map[string]interface{}{
					"configdirRef": repoWatch.Spec.Review.LLM.ConfigdirRef,
					"prompt":       prompt,
				},
				"source": map[string]interface{}{
					"cloneURL": fmt.Sprintf("%s#refs/heads/%s", *pr.Head.Repo.CloneURL, *pr.Head.Ref),
					"diffURL":  *pr.DiffURL,
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

// randString generates a random string of length n.
func randString(n int) string {
	// Create a byte slice of length n
	b := make([]byte, n)

	// Fill each position in the slice with a random character
	// from our letterBytes constant
	for i := range b {
		b[i] = letterBytes[seededRand.Intn(len(letterBytes))]
	}

	// Convert the byte slice to a string and return it
	return string(b)
}

// createSandboxForIssueHandler creates an IssueSandbox for an issue.
// It uses the LLM configuration from the RepoWatch CRD to configure the
// sandbox.
func (r *RepoWatchReconciler) createSandboxForIssueHandler(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issue *github.Issue) error {
	log := log.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-issue-%d-%s", repoWatch.Name, *issue.Number, handler.Name)

	prompt, err := r.generateIssueHandlerPrompt(handler, issue)
	if err != nil {
		return err
	}

	cloneURL := strings.Replace(*issue.RepositoryURL, "api.github.com/repos", "github.com", 1) + ".git"
	// Get repo name which is the string after the last /
	parts := strings.Split(cloneURL, "/")
	repoName := parts[len(parts)-1]
	//originURL := fmt.Sprintf("https://%s:%s@github.com/%s/%s", user.GetLogin(), githubConfig["pat"], user.GetLogin(), repoName)
	originURL := fmt.Sprintf("github.com/%s/%s", user.GetLogin(), repoName)

	branchName := fmt.Sprintf("issue-%d-%s-%s", *issue.Number, handler.Name, randString(4))

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
				"llmBackend": map[string]interface{}{
					"name": handler.LLM.Provider,
				},
				"llm": map[string]interface{}{
					"configdirRef": handler.LLM.ConfigdirRef,
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
					"branch":      branchName,
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
