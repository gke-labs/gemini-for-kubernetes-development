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

package repowatch

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

// Character set for the random string
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

const (
	OAuthPATKey  = "oauth_pat"
	ManualPATKey = "manual_pat"
)

// We create a new *rand.Rand instance seeded with the current time.
// This is crucial to get different results on each program execution.
var seededRand = rand.New(
	rand.NewSource(time.Now().UnixNano()))

type githubClientFactory func(ctx context.Context, k8sClient client.Client, repoWatch *reviewv1alpha1.RepoWatch) (*github.Client, map[string]string, error)

// PersistingTokenSource wraps an oauth2.TokenSource and persists the token to a Kubernetes secret when it changes.
type PersistingTokenSource struct {
	Source     oauth2.TokenSource
	K8sClient  client.Client
	SecretName string
	Namespace  string
	mu         sync.Mutex
}

func (s *PersistingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, err := s.Source.Token()
	if err != nil {
		return nil, err
	}

	// Persist the token if it has changed (or at least try to update the secret)
	ctx := context.Background()
	secret := &corev1.Secret{}
	if err := s.K8sClient.Get(ctx, types.NamespacedName{Name: s.SecretName, Namespace: s.Namespace}, secret); err != nil {
		log.Log.Error(err, "failed to get secret for token update", "secret", s.SecretName)
		return t, nil // Return token even if persist fails
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}

	// Check if token changed
	currentPAT := string(secret.Data[OAuthPATKey])
	if currentPAT == "" {
		// Fallback to 'pat' if 'oauth_pat' is not yet set
		currentPAT = string(secret.Data["pat"])
	}

	if currentPAT == t.AccessToken {
		return t, nil
	}

	secret.Data[OAuthPATKey] = []byte(t.AccessToken)
	if t.RefreshToken != "" {
		secret.Data["refresh_token"] = []byte(t.RefreshToken)
	}
	if !t.Expiry.IsZero() {
		secret.Data["expiry"] = []byte(t.Expiry.Format(time.RFC3339))
	}

	if err := s.K8sClient.Update(ctx, secret); err != nil {
		log.Log.Error(err, "failed to update secret with new token", "secret", s.SecretName)
	}

	return t, nil
}

// NameHash generates an FNV-1a hash from a string and returns
// it as a fixed-length hexadecimal string.
func NameHash(objectName string) string {
	h := fnv.New32a()
	h.Write([]byte(objectName))
	hashValue := h.Sum32()

	// Convert the uint32 to a hexadecimal string.
	// This results in an 8-character string (e.g., "a5b3c2d1").
	return fmt.Sprintf("%08x", hashValue)
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

	var pat []byte
	var ok bool
	if pat, ok = secret.Data[ManualPATKey]; !ok || len(string(pat)) == 0 {
		if pat, ok = secret.Data[OAuthPATKey]; !ok || len(string(pat)) == 0 {
			if pat, ok = secret.Data["pat"]; !ok || len(string(pat)) == 0 {
				// If PAT is missing or empty check if we have OAuth credentials configured.
				// If so, we might be waiting for the user to login.
				if os.Getenv("GITHUB_CLIENT_ID") != "" && os.Getenv("GITHUB_CLIENT_SECRET") != "" {
					return nil, nil, fmt.Errorf("waiting for user login to populate github token in secret %s", secretName)
				}
				return nil, nil, fmt.Errorf("GitHub token not found or empty in secret %s (checked %s, %s, and pat)", secretName, ManualPATKey, OAuthPATKey)
			}
		}
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

	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")
	refreshToken := string(secret.Data["refresh_token"])
	expiryStr := string(secret.Data["expiry"])

	var ts oauth2.TokenSource

	if clientID != "" && clientSecret != "" && refreshToken != "" {
		// Use refresh flow
		oauthConf := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     githuboauth.Endpoint,
		}

		token := &oauth2.Token{
			AccessToken:  string(pat),
			RefreshToken: refreshToken,
		}

		if expiryStr != "" {
			if expiry, err := time.Parse(time.RFC3339, expiryStr); err == nil {
				token.Expiry = expiry
			}
		}

		// ReuseTokenSource will use the existing token if valid, or refresh it if expired.
		// We wrap it in PersistingTokenSource to save the new token if refreshed.
		reusingSource := oauthConf.TokenSource(ctx, token)
		ts = &PersistingTokenSource{
			Source:     reusingSource,
			K8sClient:  k8sClient,
			SecretName: secretName,
			Namespace:  repoWatch.Namespace,
		}
	} else {
		// Fallback to static token source
		ts = oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: string(pat)},
		)
	}

	tc := oauth2.NewClient(ctx, ts)
	return clients.NewGitHubClientFromHTTP(tc), githubConfig, nil
}

// Reconciler reconciles a RepoWatch object
type Reconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	NewGithubClient githubClientFactory
}

//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/finalizers,verbs=update
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=reviewsandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=issuesandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=sandboxtasks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update;patch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
		// If we are waiting for user login, do not return an error (which triggers immediate exponential backoff).
		// Instead, requeue with a fixed delay to avoid log spam.
		if strings.Contains(err.Error(), "waiting for user login") {
			log.Info("Waiting for user login to populate github token")
			return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
		}
		log.Error(err, "unable to create github client")
		return ctrl.Result{}, err
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		log.Error(err, "unable to parse repo url")
		return ctrl.Result{}, err
	}

	// Get the current user
	user, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		// If we see this error : "GET https://api.github.com/user: 403 Resource not accessible by integration []"
		// we are running in a github workflow with a GITHUB_TOKEN that does not have access to read user info.
		// In this case we just log a warning and set fake user info.
		if strings.Contains(err.Error(), "403 Resource not accessible by integration") {
			log.Info("Warning: unable to get current user info due to insufficient permissions. Using fallback user info.")
			user = &github.User{
				Login: github.String("fake-user"),
			}
		} else {
			log.Error(err, "unable to get current user")
			return ctrl.Result{}, err
		}
	}
	if githubConfig["name"] != "" {
		user.Name = github.String(githubConfig["name"])
	}
	if githubConfig["email"] != "" {
		user.Email = github.String(githubConfig["email"])
	}

	var reconcileErr error
	// Reconcile Reviews for Pull Requests
	if err := r.reconcileReviews(ctx, repoWatch, ghClient, owner, repo, user); err != nil {
		log.Error(err, "unable to reconcile reviews")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	// Reconcile Issues
	if err := r.reconcileIssues(ctx, githubConfig, repoWatch, ghClient, owner, repo, user); err != nil {
		log.Error(err, "unable to reconcile issues")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	// Reconcile Dev Sandboxes
	if err := r.reconcileDevSandboxes(ctx, user, repoWatch, ghClient, repo); err != nil {
		log.Error(err, "unable to reconcile dev sandboxes")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	return ctrl.Result{RequeueAfter: time.Second * time.Duration(repoWatch.Spec.PollIntervalSeconds)}, reconcileErr
}

func (r *Reconciler) reconcileReviews(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, user *github.User) error {
	log := log.FromContext(ctx)
	log.Info("reconciling reviews")

	explicitPRs := r.getExplicitPRs(ctx, ghClient, repoWatch, owner, repo)

	prs, err := r.listOpenPRs(ctx, ghClient, owner, repo)
	if err != nil {
		return err
	}

	prs = r.filterPRsByLabels(prs, repoWatch)
	prs = r.filterPRsByAssignees(prs, repoWatch, user)
	prs = r.deduplicatePRs(prs, explicitPRs)
	prs = r.excludePRs(prs, repoWatch)
	prs = r.sortPRs(ctx, prs, repoWatch, user)

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

	watchedPRs, pendingPRs, activeSandboxes := r.reconcileReviewSandboxesInternal(ctx, repoWatch, explicitPRs, prs, sandboxList)

	repoWatch.Status.ActiveSandboxCount = activeSandboxes
	repoWatch.Status.ReviewSandboxes = watchedPRs
	repoWatch.Status.PendingPRs = pendingPRs

	return r.Status().Update(ctx, repoWatch)
}

func (r *Reconciler) getExplicitPRs(ctx context.Context, ghClient *github.Client, repoWatch *reviewv1alpha1.RepoWatch, owner, repo string) []*github.PullRequest {
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

func (r *Reconciler) listOpenPRs(ctx context.Context, ghClient *github.Client, owner, repo string) ([]*github.PullRequest, error) {
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

func (r *Reconciler) filterPRsByLabels(prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch) []*github.PullRequest {
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

func (r *Reconciler) filterPRsByAssignees(prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch, user *github.User) []*github.PullRequest {
	var filteredPRs []*github.PullRequest
	assigneesMap := make(map[string]bool)
	for _, assignee := range repoWatch.Spec.Review.Assignees {
		assigneesMap[assignee] = true
	}

	if repoWatch.Spec.Review.AssignedToSelf && user != nil && user.Login != nil {
		assigneesMap[*user.Login] = true
	}

	if len(assigneesMap) == 0 {
		return prs
	}

	for _, pr := range prs {
		matches := false
		for _, assignee := range pr.Assignees {
			if assignee.Login != nil && assigneesMap[*assignee.Login] {
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

func (r *Reconciler) deduplicatePRs(prs []*github.PullRequest, explicitPRs []*github.PullRequest) []*github.PullRequest {
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

func (r *Reconciler) excludePRs(prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch) []*github.PullRequest {
	if len(repoWatch.Spec.Review.ExcludePullRequests) == 0 {
		return prs
	}
	excludedPRsMap := make(map[int]bool)
	for _, prNum := range repoWatch.Spec.Review.ExcludePullRequests {
		excludedPRsMap[prNum] = true
	}

	var filteredPRs []*github.PullRequest
	for _, pr := range prs {
		if !excludedPRsMap[*pr.Number] {
			filteredPRs = append(filteredPRs, pr)
		}
	}
	return filteredPRs
}

func (r *Reconciler) reconcileReviewSandboxesInternal(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, explicitPRs []*github.PullRequest, prs []*github.PullRequest, sandboxes *unstructured.UnstructuredList) ([]reviewv1alpha1.WatchedPR, []int, int) {
	log := log.FromContext(ctx)

	ownedSandboxes := getOwnedSandboxes(sandboxes.Items, repoWatch.UID)

	// Filter ownedSandboxes to exclude those for closed PRs
	allOpenPRs := append(explicitPRs, prs...)
	var validOwnedSandboxes []unstructured.Unstructured
	for _, sandbox := range ownedSandboxes {
		parts := strings.Split(sandbox.GetName(), "-pr-")
		if len(parts) < 2 {
			continue
		}
		prNumber, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		found := false
		for _, pr := range allOpenPRs {
			if *pr.Number == prNumber {
				found = true
				break
			}
		}
		if found {
			validOwnedSandboxes = append(validOwnedSandboxes, sandbox)
		}
	}

	activeSandboxes, totalSandboxes := countSandboxes(validOwnedSandboxes, explicitPRs)

	// Cleanup closed PRs from the owned list
	r.cleanupClosedPRSandboxes(ctx, totalSandboxes, ownedSandboxes, allOpenPRs)

	watchedPRs := []reviewv1alpha1.WatchedPR{}
	pendingPRs := []int{}

	// Combine explicit and auto-discovered PRs for processing
	allPRs := append(explicitPRs, prs...)

	for _, pr := range allPRs {
		sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, *pr.Number)
		sandboxExists := false
		var existingSandbox *unstructured.Unstructured

		for i := range ownedSandboxes {
			if ownedSandboxes[i].GetName() == sandboxName {
				sandboxExists = true
				existingSandbox = &ownedSandboxes[i]
				break
			}
		}

		if sandboxExists {
			// Check for scale down
			if repoWatch.Spec.Review.ReviewShutdownAfterMinutes > 0 {
				creationTimestamp := existingSandbox.GetCreationTimestamp()
				shutdownDuration := time.Minute * time.Duration(repoWatch.Spec.Review.ReviewShutdownAfterMinutes)
				if time.Since(creationTimestamp.Time) > shutdownDuration {
					replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
					if err == nil && found && replicas > 0 {
						log.Info("scaling down review sandbox", "sandbox", existingSandbox.GetName())
						if err := unstructured.SetNestedField(existingSandbox.Object, int64(0), "spec", "replicas"); err != nil {
							log.Error(err, "unable to set replicas for sandbox", "sandbox", existingSandbox.GetName())
						} else {
							if err := r.Update(ctx, existingSandbox); err != nil {
								log.Error(err, "unable to update sandbox", "sandbox", existingSandbox.GetName())
							} else {
								// Decrement active count as it is no longer active
								activeSandboxes--
							}
						}
					}
				}
			}

			// Check if sandbox is scaled down (re-check in case we just updated it or it was already down)
			replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
			scaledDown := false
			if err == nil && found && replicas == 0 {
				scaledDown = true
			}

			watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
				Number:      *pr.Number,
				SandboxName: sandboxName,
				Status:      "Active",
				ScaledDown:  scaledDown,
			})
		} else {
			// Sandbox does not exist, try to create it if within limits
			prIsExplicit := isPRExplicit(*pr.Number, explicitPRs)
			if prIsExplicit || (activeSandboxes < repoWatch.Spec.Review.MaxActiveSandboxes) &&
				(repoWatch.Spec.Review.MaxSandboxes == 0 || totalSandboxes < repoWatch.Spec.Review.MaxSandboxes) {
				log.Info("creating sandbox for PR", "pr", *pr.Number)
				if err := r.createReviewSandboxForPR(ctx, repoWatch, pr); err != nil {
					log.Error(err, "unable to create sandbox for PR", "pr", *pr.Number)
				} else {
					activeSandboxes++
					totalSandboxes++
					watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
						Number:      *pr.Number,
						SandboxName: sandboxName,
						Status:      "Creating",
						ScaledDown:  false,
					})
				}
			} else {
				pendingPRs = append(pendingPRs, *pr.Number)
			}
		}
	}
	return watchedPRs, pendingPRs, activeSandboxes
}

func (r *Reconciler) reconcileIssues(ctx context.Context, githubConfig map[string]string, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, user *github.User) error {
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

	if user == nil {
		return fmt.Errorf("user is nil")
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

func (r *Reconciler) reconcileIssuesForHandler(ctx context.Context, user *github.User, sandboxList *unstructured.UnstructuredList, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, _ map[string]string) error {
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

	repoIssues = r.excludeIssues(repoIssues, handler)

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
	return r.reconcileIssueHandlerSandboxesInternal(ctx, user, handler, repoWatch, repoIssues, sandboxList)
}

func (r *Reconciler) excludeIssues(issues []*github.Issue, handler reviewv1alpha1.IssueHandlerSpec) []*github.Issue {
	if len(handler.ExcludeIssues) == 0 {
		return issues
	}
	excludedIssuesMap := make(map[int]bool)
	for _, issueNum := range handler.ExcludeIssues {
		excludedIssuesMap[issueNum] = true
	}

	var filteredIssues []*github.Issue
	for _, issue := range issues {
		if !excludedIssuesMap[*issue.Number] {
			filteredIssues = append(filteredIssues, issue)
		}
	}
	return filteredIssues
}

func (r *Reconciler) reconcileIssueHandlerSandboxesInternal(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issues []*github.Issue, sandboxes *unstructured.UnstructuredList) error {
	log := log.FromContext(ctx)

	// 1. Filter sandboxes to only include those owned by this RepoWatch instance and handler
	ownedSandboxes := getOwnedIssueSandboxes(sandboxes.Items, repoWatch.UID, handler.Name)

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
	pendingIssues := []int{}

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
		var existingSandbox *unstructured.Unstructured

		for i := range ownedSandboxes {
			if ownedSandboxes[i].GetName() == sandboxName {
				sandboxExists = true
				existingSandbox = &ownedSandboxes[i]
				break
			}
		}

		if sandboxExists {
			scaledDown := false
			// Scale down check
			if handler.IssueShutdownAfterMinutes > 0 {
				creationTimestamp := existingSandbox.GetCreationTimestamp()
				shutdownDuration := time.Minute * time.Duration(handler.IssueShutdownAfterMinutes)
				if time.Since(creationTimestamp.Time) > shutdownDuration {
					replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
					if err == nil && found && replicas > 0 {
						log.Info("scaling down issue sandbox", "sandbox", existingSandbox.GetName())
						if err := unstructured.SetNestedField(existingSandbox.Object, int64(0), "spec", "replicas"); err != nil {
							log.Error(err, "unable to set replicas for sandbox", "sandbox", existingSandbox.GetName())
						} else {
							if err := r.Update(ctx, existingSandbox); err != nil {
								log.Error(err, "unable to update sandbox", "sandbox", existingSandbox.GetName())
							} else {
								scaledDown = true
							}
						}
					}
				}
			}

			if !scaledDown {
				replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
				if err == nil && found && replicas > 0 {
					activeSandboxes++
				}
			}

			watchedIssues = append(watchedIssues, reviewv1alpha1.WatchedIssue{
				Number:      *issue.Number,
				SandboxName: sandboxName,
				Status:      "Active",
				ScaledDown:  scaledDown,
			})
		} else {
			// Sandbox does not exist, try to create it if within limits
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
						ScaledDown:  false,
					})
				}
			} else {
				pendingIssues = append(pendingIssues, *issue.Number)
			}
		}
	}

	if repoWatch.Status.IssueSandboxes == nil {
		repoWatch.Status.IssueSandboxes = make(map[string][]reviewv1alpha1.WatchedIssue)
	}
	if repoWatch.Status.PendingIssues == nil {
		repoWatch.Status.PendingIssues = make(map[string][]int)
	}
	repoWatch.Status.IssueSandboxes[handler.Name] = watchedIssues
	repoWatch.Status.PendingIssues[handler.Name] = pendingIssues

	return r.Status().Update(ctx, repoWatch)
}

// generateIssueHandlerPrompt generates a prompt for an issue handler.
func (r *Reconciler) generateIssueHandlerPrompt(handler reviewv1alpha1.IssueHandlerSpec, issue *github.Issue) (string, error) {
	return prompts.ExpandIssueHandlerPrompt(handler.LLM.Prompt, issue)
}

// createReviewSandboxForPR creates a ReviewSandbox for a pull request.
// It uses the LLM configuration from the RepoWatch CRD to configure the
// sandbox.
func (r *Reconciler) createReviewSandboxForPR(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) error {
	log := log.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, *pr.Number)

	prompt := repoWatch.Spec.Review.LLM.Prompt

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
				"annotations": map[string]interface{}{
					"agentState":  "provisioning",
					"reviewState": "",
				},
			},
			"spec": map[string]interface{}{
				"llmBackend": map[string]interface{}{
					"name": repoWatch.Spec.Review.LLM.Provider,
				},
				"llm": map[string]interface{}{
					"configdirRef":     repoWatch.Spec.Review.LLM.ConfigdirRef,
					"prompt":           "",
					"apiKeySecretName": repoWatch.Spec.Review.LLM.APIKeySecretRef,
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
				"maxReviewFiles": int64(repoWatch.Spec.Review.MaxReviewFiles),
				"replicas":       int64(1),
			},
		},
	}

	if repoWatch.Spec.Review.DevcontainerConfigRef != "" {
		if err := unstructured.SetNestedField(sandbox.Object, repoWatch.Spec.Review.DevcontainerConfigRef, "spec", "devcontainerConfigRef"); err != nil {
			return err
		}
	}

	if repoWatch.Spec.Review.Image != "" {
		if err := unstructured.SetNestedField(sandbox.Object, repoWatch.Spec.Review.Image, "spec", "image"); err != nil {
			return err
		}
	}

	if err := controllerutil.SetControllerReference(repoWatch, sandbox, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, sandbox); err != nil {
		return err
	}

	if err := r.createSandboxTask(ctx, repoWatch, sandboxName, "review", map[string]string{
		"AGENT_PROMPT": prompt,
	}); err != nil {
		log.Error(err, "unable to create initial review task for sandbox", "sandbox", sandboxName)
	}

	return nil
}

// createSandboxTask creates a SandboxTask for a sandbox.
func (r *Reconciler) createSandboxTask(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, sandboxName string, taskType string, params map[string]string) error {
	taskName := fmt.Sprintf("%s-task-%d-%s", sandboxName, time.Now().Unix(), strings.ToLower(randString(4)))

	// Convert params to map[string]interface{}
	paramsInterface := make(map[string]interface{})
	for k, v := range params {
		paramsInterface[k] = v
	}

	task := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "SandboxTask",
			"metadata": map[string]interface{}{
				"name":      taskName,
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/sandbox-name": sandboxName,
				},
			},
			"spec": map[string]interface{}{
				"sandboxName": sandboxName,
				"type":        taskType,
				"params":      paramsInterface,
			},
		},
	}

	if err := controllerutil.SetControllerReference(repoWatch, task, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, task)
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
func (r *Reconciler) createSandboxForIssueHandler(ctx context.Context, user *github.User, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, issue *github.Issue) error {
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
				"annotations": map[string]interface{}{
					"agentState": "provisioning",
				},
			},
			"spec": map[string]interface{}{
				"llmBackend": map[string]interface{}{
					"name": handler.LLM.Provider,
				},
				"llm": map[string]interface{}{
					"configdirRef":     handler.LLM.ConfigdirRef,
					"prompt":           prompt,
					"apiKeySecretName": handler.LLM.APIKeySecretRef,
				},
				"source": map[string]interface{}{
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

	if handler.Image != "" {
		if err := unstructured.SetNestedField(sandbox.Object, handler.Image, "spec", "image"); err != nil {
			return err
		}
	}

	if err := controllerutil.SetControllerReference(repoWatch, sandbox, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, sandbox)
}

func (r *Reconciler) reconcileDevSandboxes(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, upstreamRepo string) error {
	log := log.FromContext(ctx)

	if repoWatch.Spec.Dev.MaxSandboxes == 0 {
		return nil
	}

	// 1. Get User's Fork
	// We assume the fork has the same name as the upstream repo
	forkOwner := user.GetLogin()
	forkRepo := upstreamRepo

	// Verify fork exists
	repo, _, err := ghClient.Repositories.Get(ctx, forkOwner, forkRepo)
	if err != nil {
		log.Error(err, "unable to get user fork", "owner", forkOwner, "repo", forkRepo)
		// If fork doesn't exist, we can't do anything.
		return nil
	}

	branches, err := r.getDevCandidateBranches(ctx, ghClient, repoWatch, forkOwner, forkRepo, repo.GetDefaultBranch())
	if err != nil {
		return err
	}

	watchedDevSandboxes, pendingDevBranches, err := r.reconcileDevSandboxesInternal(ctx, user, repoWatch, branches, forkOwner, forkRepo)
	if err != nil {
		return err
	}

	repoWatch.Status.DevSandboxes = watchedDevSandboxes
	repoWatch.Status.PendingDevBranches = pendingDevBranches

	return r.Status().Update(ctx, repoWatch)
}

func (r *Reconciler) getDevCandidateBranches(ctx context.Context, ghClient *github.Client, repoWatch *reviewv1alpha1.RepoWatch, forkOwner, forkRepo string, defaultBranch string) ([]*github.Branch, error) {
	log := log.FromContext(ctx)

	// 2. List Branches (or use explicit list)
	var allBranches []*github.Branch
	if len(repoWatch.Spec.Dev.Branches) > 0 {
		// If explicit branches are specified, fetch them directly
		for _, branchName := range repoWatch.Spec.Dev.Branches {
			branch, _, err := ghClient.Repositories.GetBranch(ctx, forkOwner, forkRepo, branchName, true)
			if err != nil {
				log.Error(err, "unable to get branch", "branchName", branchName)
				continue
			}
			allBranches = append(allBranches, branch)
		}
	} else {
		// Otherwise, list all branches
		branches, _, err := ghClient.Repositories.ListBranches(ctx, forkOwner, forkRepo, &github.BranchListOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		if err != nil {
			return nil, fmt.Errorf("listing branches: %w", err)
		}
		allBranches = branches
	}

	// 3. Filter Branches (exclude issues, main/master, and explicitly excluded branches)
	var candidateBranches []*github.Branch
	excludedBranchesMap := make(map[string]bool)
	for _, branchName := range repoWatch.Spec.Dev.ExcludeBranches {
		excludedBranchesMap[branchName] = true
	}

	for _, branch := range allBranches {
		name := branch.GetName()
		if strings.HasPrefix(name, "issue-") {
			continue
		}
		if name == "main" {
			continue
		}
		if name == "master" {
			continue
		}
		if name == defaultBranch {
			continue
		}
		if excludedBranchesMap[name] {
			continue
		}
		candidateBranches = append(candidateBranches, branch)
	}

	// 4. Sort Branches by Commit Date
	// We need to fetch commit details for sorting.
	type BranchWithDate struct {
		Branch *github.Branch
		Date   time.Time
	}
	var branchesWithDate []BranchWithDate

	for _, branch := range candidateBranches {
		// Check if the branch is ahead of the default branch
		comp, _, err := ghClient.Repositories.CompareCommits(ctx, forkOwner, forkRepo, defaultBranch, branch.GetName(), nil)
		if err != nil {
			log.Error(err, "comparing commits", "branch", branch.GetName())
			continue
		}
		if comp.GetAheadBy() == 0 {
			continue
		}

		commit, _, err := ghClient.Repositories.GetCommit(ctx, forkOwner, forkRepo, branch.GetCommit().GetSHA(), nil)
		if err != nil {
			log.Error(err, "getting commit details", "branch", branch.GetName())
			continue
		}
		branchesWithDate = append(branchesWithDate, BranchWithDate{
			Branch: branch,
			Date:   commit.GetCommit().GetCommitter().GetDate(),
		})
	}

	sort.Slice(branchesWithDate, func(i, j int) bool {
		return branchesWithDate[i].Date.After(branchesWithDate[j].Date)
	})

	var sortedBranches []*github.Branch
	for _, b := range branchesWithDate {
		sortedBranches = append(sortedBranches, b.Branch)
	}

	return sortedBranches, nil
}

func (r *Reconciler) reconcileDevSandboxesInternal(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, branches []*github.Branch, forkOwner, forkRepo string) ([]reviewv1alpha1.DevSandbox, []string, error) {
	log := log.FromContext(ctx)
	// 6. List Existing DevSandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)
	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace), client.MatchingLabels{"sandbox.gemini.google.com/type": "dev"}); err != nil {
		return nil, nil, fmt.Errorf("listing dev sandboxes: %w", err)
	}

	activeSandboxes := 0
	watchedDevSandboxes := []reviewv1alpha1.DevSandbox{}
	pendingDevBranches := []string{}

	// Identify which branches we want to have sandboxes for.
	desiredBranches := make(map[string]bool)
	for _, b := range branches {
		desiredBranches[b.GetName()] = true
	}

	for _, sandbox := range sandboxList.Items {
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

		// Get branch from spec
		branch, found, err := unstructured.NestedString(sandbox.Object, "spec", "destination", "branch")
		if err != nil || !found {
			log.Error(err, "unable to get branch from sandbox", "sandbox", sandbox.GetName())
			continue
		}

		if !desiredBranches[branch] {
			log.Info("deleting dev sandbox for untracked branch", "branch", branch)
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			}
			continue
		}

		// Check if scaled down
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		scaledDown := false
		if err == nil && found && replicas == 0 {
			scaledDown = true
		}

		if !scaledDown {
			activeSandboxes++
		}
		watchedDevSandboxes = append(watchedDevSandboxes, reviewv1alpha1.DevSandbox{
			BranchName:  branch,
			SandboxName: sandbox.GetName(),
			Status:      "Active",
			ScaledDown:  scaledDown,
		})

	}

	// 7. Create/Update Sandboxes
	for _, branch := range branches {
		branchName := branch.GetName()
		// Sanitize branch name for kubernetes resource name
		safeBranchName := strings.ReplaceAll(branchName, "/", "-")
		safeBranchName = strings.ReplaceAll(safeBranchName, "_", "-")
		safeBranchName = strings.ReplaceAll(safeBranchName, ".", "-")
		safeBranchName = strings.ToLower(safeBranchName)

		// Kubernetes names must be <= 63 characters
		// hashing ensures we don't exceed this limit
		fullSuffix := fmt.Sprintf("dev-%s-%s", forkRepo, safeBranchName)
		hashedSuffix := NameHash(fullSuffix)
		sandboxName := fmt.Sprintf("%s-dev", hashedSuffix)

		// Check if sandbox exists
		sandboxExists := false
		for _, ws := range watchedDevSandboxes {
			if ws.SandboxName == sandboxName {
				sandboxExists = true
				break
			}
		}

		if sandboxExists {
			continue
		}

		if activeSandboxes < repoWatch.Spec.Dev.MaxActiveSandboxes && (repoWatch.Spec.Dev.MaxSandboxes == 0 || len(watchedDevSandboxes) < repoWatch.Spec.Dev.MaxSandboxes) {
			log.Info("creating dev sandbox", "branch", branchName)
			if err := r.createDevSandbox(ctx, user, repoWatch, forkOwner, forkRepo, branchName, sandboxName); err != nil {
				log.Error(err, "creating dev sandbox", "branch", branchName)
			} else {
				activeSandboxes++
				watchedDevSandboxes = append(watchedDevSandboxes, reviewv1alpha1.DevSandbox{
					BranchName:  branchName,
					SandboxName: sandboxName,
					Status:      "Creating",
					ScaledDown:  false,
				})
			}
		} else {
			pendingDevBranches = append(pendingDevBranches, branchName)
		}
	}
	return watchedDevSandboxes, pendingDevBranches, nil
}

func (r *Reconciler) createDevSandbox(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, forkOwner, forkRepo, branchName, sandboxName string) error {
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", forkOwner, forkRepo)
	originURL := fmt.Sprintf("github.com/%s/%s.git", forkOwner, forkRepo)

	opts := sandbox.DevSandboxOptions{
		Name:      sandboxName,
		Namespace: repoWatch.Namespace,
		Labels: map[string]string{
			"review.gemini.google.com/repowatch": repoWatch.Name,
		},
		CloneURL: cloneURL,
		HTMLURL:  fmt.Sprintf("https://github.com/%s/%s", forkOwner, forkRepo),

		Branch:      branchName,
		Origin:      originURL,
		PushEnabled: true,
		UserLogin:   user.GetLogin(),
		UserName:    user.GetName(),
		UserEmail:   user.GetEmail(),

		LLMProvider:         repoWatch.Spec.Dev.LLM.Provider,
		LLMConfigdirRef:     repoWatch.Spec.Dev.LLM.ConfigdirRef,
		LLMAPIKeySecretName: repoWatch.Spec.Dev.LLM.APIKeySecretRef,

		GithubSecretName:      repoWatch.Spec.GithubSecretName,
		DevcontainerConfigRef: repoWatch.Spec.Dev.DevcontainerConfigRef,
		Image:                 repoWatch.Spec.Dev.Image,

		HTTPEnabled: true,
		Replicas:    1,
	}

	sb := sandbox.NewDevSandbox(opts)

	if err := controllerutil.SetControllerReference(repoWatch, sb, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, sb)
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&reviewv1alpha1.RepoWatch{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		// Owns(&reviewv1alpha1.ReviewSandbox{}).
		Complete(r)
}
