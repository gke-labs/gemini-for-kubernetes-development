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
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/url"
	"os"
	"regexp"
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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	pkg_github "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
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
// It ensures that refreshed OAuth tokens (and their new refresh tokens/expiry) are saved back to the K8s Secret,
// preventing token loss if the pod restarts.
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
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
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
	// Token priority:
	// 1. Manual PAT (manual_pat) - User manually provided a PAT.
	// 2. OAuth PAT (oauth_pat) - Token obtained via OAuth flow.
	// 3. Legacy PAT (pat) - Backward compatibility.
	// If OAuth credentials are configured but no token exists, we return a specific error
	// to indicate we are "waiting for user login" via the UI.
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

type cachedUser struct {
	user   *github.User
	expiry time.Time
}

// Reconciler reconciles a RepoWatch object
type Reconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	NewGithubClient  githubClientFactory
	RepoSandboxImage string
	ConfigDirImage   string
	ForceSandboxMode string

	userCacheMu sync.Mutex
	userCache   map[string]cachedUser
}

//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches/finalizers,verbs=update
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=sandboxtasks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

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

	if r.ForceSandboxMode != "" {
		inconsistent := false
		if repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.DindSupport != r.ForceSandboxMode {
			inconsistent = true
		}
		if repoWatch.Spec.Dev.DindSupport != r.ForceSandboxMode {
			inconsistent = true
		}
		if repoWatch.Spec.Review.DindSupport != r.ForceSandboxMode {
			inconsistent = true
		}

		if inconsistent {
			r.setCondition(ctx, repoWatch, "SandboxModeConsistency", metav1.ConditionFalse, "Inconsistent", fmt.Sprintf("force-sandbox-mode is enabled (%s), but dindSupport is not set to match. Sandboxes are forced to use %s anyway.", r.ForceSandboxMode, r.ForceSandboxMode))
		} else {
			r.setCondition(ctx, repoWatch, "SandboxModeConsistency", metav1.ConditionTrue, "Consistent", fmt.Sprintf("Configuration is consistent with force-sandbox-mode (%s).", r.ForceSandboxMode))
		}
	}

	ghClient, githubConfig, err := r.NewGithubClient(ctx, r.Client, repoWatch)
	if err != nil {
		// If we are waiting for user login, do not return an error (which triggers immediate exponential backoff).
		// Instead, requeue with a fixed delay to avoid log spam.
		if strings.Contains(err.Error(), "waiting for user login") {
			log.Info("Waiting for user login to populate github token")
			r.setAuthCondition(ctx, repoWatch, metav1.ConditionFalse, "WaitingForLogin", err.Error())
			return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
		}
		log.Error(err, "unable to create github client")
		r.setAuthCondition(ctx, repoWatch, metav1.ConditionFalse, "TokenMissing", err.Error())
		return ctrl.Result{}, err
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		log.Error(err, "unable to parse repo url")
		return ctrl.Result{}, err
	}

	// Get the current user
	var user *github.User
	cacheKey := repoWatch.Namespace + "/" + repoWatch.Name
	r.userCacheMu.Lock()
	if r.userCache == nil {
		r.userCache = make(map[string]cachedUser)
	}
	if cached, ok := r.userCache[cacheKey]; ok && time.Now().Before(cached.expiry) {
		userCopy := *cached.user
		user = &userCopy
	}
	r.userCacheMu.Unlock()

	if user == nil {
		var err error
		user, _, err = ghClient.Users.Get(ctx, "")
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
				r.setAuthCondition(ctx, repoWatch, metav1.ConditionFalse, "TokenInvalid", err.Error())
				return ctrl.Result{}, err
			}
		} else {
			r.userCacheMu.Lock()
			r.userCache[cacheKey] = cachedUser{
				user:   user,
				expiry: time.Now().Add(15 * time.Minute),
			}
			r.userCacheMu.Unlock()
			userCopy := *user
			user = &userCopy
		}
	}

	// GitHub auth succeeded — clear any previous auth failure condition
	r.setAuthCondition(ctx, repoWatch, metav1.ConditionTrue, "Authenticated", "GitHub authentication successful")
	if githubConfig["name"] != "" {
		user.Name = github.String(githubConfig["name"])
	}
	if githubConfig["email"] != "" {
		user.Email = github.String(githubConfig["email"])
	}

	// List Pods to check for status/eviction
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(repoWatch.Namespace)); err != nil {
		log.Error(err, "unable to list pods")
	}
	podsBySandbox := make(map[string]*corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		// Sandboxes create Pods with label sandbox=<SandboxName>
		if sandboxLabel, ok := pod.Labels["sandbox"]; ok {
			podsBySandbox[sandboxLabel] = pod
		}
	}

	var reconcileErr error
	// Reconcile Reviews for Pull Requests
	log.Info("reconciling reviews")
	if err := r.reconcileReviews(ctx, repoWatch, ghClient, owner, repo, user, podsBySandbox); err != nil {
		log.Error(err, "unable to reconcile reviews")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	log.Info("reconciling issues")
	// Reconcile Issues
	if err := r.reconcileIssues(ctx, repoWatch, ghClient, owner, repo, user, podsBySandbox); err != nil {
		log.Error(err, "unable to reconcile issues")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	log.Info("reconciling dev sandboxes")
	// Reconcile Dev Sandboxes
	if err := r.reconcileDevSandboxes(ctx, user, repoWatch, ghClient, repo, podsBySandbox); err != nil {
		log.Error(err, "unable to reconcile dev sandboxes")
		reconcileErr = errors.Join(reconcileErr, err)
		// Continue to next reconciliation
	}

	return ctrl.Result{RequeueAfter: time.Second * time.Duration(repoWatch.Spec.PollIntervalSeconds)}, reconcileErr
}

// setAuthCondition sets the GitHubAuthentication condition on the RepoWatch status.
func (r *Reconciler) setAuthCondition(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, status metav1.ConditionStatus, reason, message string) {
	log := log.FromContext(ctx)

	condition := metav1.Condition{
		Type:               "GitHubAuthentication",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: repoWatch.Generation,
	}

	changed := apimeta.SetStatusCondition(&repoWatch.Status.Conditions, condition)
	if !changed {
		return
	}
	if err := r.Status().Update(ctx, repoWatch); err != nil {
		log.Error(err, "unable to update RepoWatch auth condition")
	}
}

// setCondition sets a generic condition on the RepoWatch status.
func (r *Reconciler) setCondition(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, condType string, status metav1.ConditionStatus, reason, message string) {
	log := log.FromContext(ctx)

	condition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: repoWatch.Generation,
	}

	changed := apimeta.SetStatusCondition(&repoWatch.Status.Conditions, condition)
	if !changed {
		return
	}
	if err := r.Status().Update(ctx, repoWatch); err != nil {
		log.Error(err, "unable to update RepoWatch condition", "type", condType)
	}
}

func (r *Reconciler) reconcileReviews(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, user *github.User, podsBySandbox map[string]*corev1.Pod) error {
	log := log.FromContext(ctx)

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
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)

	labelSelector := client.MatchingLabels{
		"review.gemini.google.com/repowatch": repoWatch.Name,
	}

	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace), labelSelector); err != nil {
		log.Error(err, "unable to list Sandboxes")
		return err
	}

	watchedPRs, pendingPRs, activeSandboxes := r.reconcileReviewSandboxesInternal(ctx, user, repoWatch, explicitPRs, prs, sandboxList, podsBySandbox)

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
	var filteredPRs []*github.PullRequest
	for _, pr := range prs {
		// Filter by ExcludeLabels first
		if len(repoWatch.Spec.Review.ExcludeLabels) > 0 {
			excluded := false
			for _, excludeLabel := range repoWatch.Spec.Review.ExcludeLabels {
				for _, prLabel := range pr.Labels {
					if prLabel.Name != nil && *prLabel.Name == excludeLabel {
						excluded = true
						break
					}
				}
				if excluded {
					break
				}
			}
			if excluded {
				continue
			}
		}

		// Filter by Labels
		if len(repoWatch.Spec.Review.Labels) > 0 {
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
		} else {
			// No positive labels specified, so it matches by default (after passing exclusion)
			filteredPRs = append(filteredPRs, pr)
		}
	}
	return filteredPRs
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

func (r *Reconciler) reconcileReviewSandboxesInternal(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, explicitPRs []*github.PullRequest, prs []*github.PullRequest, sandboxes *unstructured.UnstructuredList, podsBySandbox map[string]*corev1.Pod) ([]reviewv1alpha1.WatchedPR, []int, int) {
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
			// Manage lifecycle (pause/unpause)
			shutdownDuration := time.Minute * time.Duration(repoWatch.Spec.Review.ReviewShutdownAfterMinutes)
			wasScaled, err := r.manageSandboxLifecycle(ctx, existingSandbox, shutdownDuration)
			if err != nil {
				log.Error(err, "unable to manage sandbox lifecycle", "sandbox", existingSandbox.GetName())
			}

			// Check if sandbox is scaled down (re-check in case we just updated it or it was already down)
			replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
			scaledDown := false
			if err == nil && found && replicas == 0 {
				scaledDown = true
			}

			// If it was just scaled down, decrement active count
			if wasScaled && scaledDown {
				activeSandboxes--
			}

			sandboxStatus, err := r.reconcileSandboxPodStatus(ctx, existingSandbox, podsBySandbox, scaledDown)
			if err != nil {
				log.Error(err, "unable to reconcile sandbox pod status", "pr", *pr.Number)
			}

			watchedPRs = append(watchedPRs, reviewv1alpha1.WatchedPR{
				Number:      *pr.Number,
				SandboxName: sandboxName,
				Status:      sandboxStatus,
				ScaledDown:  scaledDown,
			})
		} else {
			// Sandbox does not exist, try to create it if within limits
			prIsExplicit := isPRExplicit(*pr.Number, explicitPRs)
			// Explicit PRs (defined in RepoWatch CRD) bypass MaxActiveSandboxes and MaxSandboxes limits.
			// Auto-discovered PRs must respect these limits to prevent resource exhaustion.
			if prIsExplicit || (activeSandboxes < repoWatch.Spec.Review.MaxActiveSandboxes) &&
				(repoWatch.Spec.Review.MaxSandboxes == 0 || totalSandboxes < repoWatch.Spec.Review.MaxSandboxes) {
				log.Info("creating sandbox for PR", "pr", *pr.Number)
				if err := r.createReviewSandboxForPR(ctx, user, repoWatch, pr); err != nil {
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

func (r *Reconciler) reconcileIssues(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner string, repo string, user *github.User, podsBySandbox map[string]*corev1.Pod) error {
	log := log.FromContext(ctx)
	if repoWatch.Spec.Issue == nil {
		return nil
	}

	// 1. List all open issues
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var allIssues []*github.Issue
	for {
		issues, resp, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			log.Error(err, "unable to list issues")
			return err
		}
		// Filter out PRs
		for _, issue := range issues {
			if !issue.IsPullRequest() {
				allIssues = append(allIssues, issue)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// 2. List existing Sandboxes (issues)
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)
	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace), client.MatchingLabels{"sandbox.gemini.google.com/type": "issue"}); err != nil {
		return err
	}

	ownedSandboxes := getOwnedSandboxes(sandboxList.Items, repoWatch.UID)

	// 3. Process Issues
	activeSandboxes := 0
	totalSandboxes := 0
	watchedIssues := make(map[string][]reviewv1alpha1.WatchedIssue)
	pendingIssues := make(map[string][]int)

	// Helper to count active/total
	for _, sandbox := range ownedSandboxes {
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err == nil && found && replicas > 0 {
			activeSandboxes++
		}
		totalSandboxes++
	}

	// Track which sandboxes are valid (keep them)
	validSandboxNames := make(map[string]bool)

	for _, issue := range allIssues {
		// Identify applicable handlers
		var applicableHandlers []reviewv1alpha1.IssueHandlerSpec
		for _, handler := range repoWatch.Spec.Issue.Handlers {
			if r.isIssueMatch(issue, handler, repoWatch, user) {
				applicableHandlers = append(applicableHandlers, handler)
			}
		}

		if len(applicableHandlers) == 0 {
			continue
		}

		sandboxName := fmt.Sprintf("%s-issue-%d", repoWatch.Name, *issue.Number)
		validSandboxNames[sandboxName] = true

		// Check if sandbox exists
		var existingSandbox *unstructured.Unstructured
		for i := range ownedSandboxes {
			if ownedSandboxes[i].GetName() == sandboxName {
				existingSandbox = &ownedSandboxes[i]
				break
			}
		}

		if existingSandbox != nil {
			log.Info("sandbox found for", "issue", *issue.Number)

			// Check for feedback
			if err := r.reconcileIssueFeedback(ctx, repoWatch, existingSandbox, issue, ghClient); err != nil {
				log.Error(err, "unable to reconcile issue feedback", "issue", *issue.Number)
			}

			// Check for PR failures
			if err := r.reconcilePRFailures(ctx, repoWatch, existingSandbox, issue, ghClient); err != nil {
				log.Error(err, "unable to reconcile PR failures", "issue", *issue.Number)
			}

			// Manage lifecycle (pause/unpause)
			shutdownDuration := time.Minute * time.Duration(repoWatch.Spec.Issue.IssueShutdownAfterMinutes)
			wasScaled, err := r.manageSandboxLifecycle(ctx, existingSandbox, shutdownDuration)
			if err != nil {
				log.Error(err, "unable to manage sandbox lifecycle", "sandbox", existingSandbox.GetName())
			}

			// Re-check replicas to see if it's scaled down
			replicas, found, err := unstructured.NestedInt64(existingSandbox.Object, "spec", "replicas")
			scaledDown := false
			if err == nil && found && replicas == 0 {
				scaledDown = true
			}

			if wasScaled && scaledDown {
				activeSandboxes--
			}

			sandboxStatus, err := r.reconcileSandboxPodStatus(ctx, existingSandbox, podsBySandbox, scaledDown)
			if err != nil {
				log.Error(err, "unable to reconcile sandbox pod status", "issue", *issue.Number)
			}

			// Ensure tasks exist for applicable handlers
			for _, handler := range applicableHandlers {
				if err := r.ensureIssueTask(ctx, repoWatch, existingSandbox, sandboxName, issue, handler); err != nil {
					log.Error(err, "unable to ensure task", "sandbox", sandboxName, "handler", handler.Name)
				}
				watchedIssues[handler.Name] = append(watchedIssues[handler.Name], reviewv1alpha1.WatchedIssue{
					Number:      *issue.Number,
					SandboxName: sandboxName,
					Status:      sandboxStatus,
					ScaledDown:  scaledDown,
				})
			}

		} else {
			log.Info("sandbox not found for", "issue", *issue.Number, "activeSandboxes", activeSandboxes, "totalSandboxes", totalSandboxes)
			// Create Sandbox if within limits
			issueIsExplicit := isIssueExplicit(*issue.Number, repoWatch.Spec.Issue.Issues)
			if issueIsExplicit || (activeSandboxes < repoWatch.Spec.Issue.MaxActiveSandboxes &&
				(repoWatch.Spec.Issue.MaxSandboxes == 0 || totalSandboxes < repoWatch.Spec.Issue.MaxSandboxes)) {
				log.Info("creating sandbox for issue", "issue", *issue.Number)
				createdSandbox, err := r.createIssueSandbox(ctx, user, repoWatch, issue)
				if err != nil {
					log.Error(err, "unable to create sandbox for issue", "issue", *issue.Number)
				} else {
					activeSandboxes++
					totalSandboxes++
					// Create tasks immediately
					for _, handler := range applicableHandlers {
						if err := r.ensureIssueTask(ctx, repoWatch, createdSandbox, sandboxName, issue, handler); err != nil {
							log.Error(err, "unable to create task", "sandbox", sandboxName, "handler", handler.Name)
						}
						watchedIssues[handler.Name] = append(watchedIssues[handler.Name], reviewv1alpha1.WatchedIssue{
							Number:      *issue.Number,
							SandboxName: sandboxName,
							Status:      "Creating",
							ScaledDown:  false,
						})
					}
				}
			} else {
				for _, handler := range applicableHandlers {
					pendingIssues[handler.Name] = append(pendingIssues[handler.Name], *issue.Number)
				}
			}
		}
	}

	// Cleanup old sandboxes
	for _, sandbox := range ownedSandboxes {
		labels := sandbox.GetLabels()
		if labels != nil && labels["sandbox.gemini.google.com/type"] == "dev" {
			continue
		}
		if !validSandboxNames[sandbox.GetName()] {
			log.Info("deleting orphan issue sandbox", "sandbox", sandbox.GetName())
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			}
		}
	}

	repoWatch.Status.IssueSandboxes = watchedIssues
	repoWatch.Status.PendingIssues = pendingIssues

	return r.Status().Update(ctx, repoWatch)
}

func (r *Reconciler) isIssueMatch(issue *github.Issue, handler reviewv1alpha1.IssueHandlerSpec, repoWatch *reviewv1alpha1.RepoWatch, user *github.User) bool {
	if repoWatch.Spec.Issue != nil {
		// Include explicit includes - bypass other filters
		for _, included := range repoWatch.Spec.Issue.Issues {
			if *issue.Number == included {
				return true
			}
		}

		// Exclude explicit excludes from IssueSpec
		for _, excluded := range repoWatch.Spec.Issue.ExcludeIssues {
			if *issue.Number == excluded {
				return false
			}
		}

		// Check AssignedToSelf
		if repoWatch.Spec.Issue.AssignedToSelf && user != nil && user.Login != nil {
			isAssigned := false
			for _, assignee := range issue.Assignees {
				if assignee.Login != nil && *assignee.Login == *user.Login {
					isAssigned = true
					break
				}
			}
			if !isAssigned {
				return false
			}
		}
	}

	// Check labels
	if len(handler.ExcludeLabels) > 0 {
		for _, label := range issue.Labels {
			for _, excludeLabel := range handler.ExcludeLabels {
				if label.Name != nil && *label.Name == excludeLabel {
					return false
				}
			}
		}
	}

	// Check labels
	if len(handler.Labels) > 0 {
		hasLabel := false
		for _, label := range issue.Labels {
			for _, reqLabel := range handler.Labels {
				if label.Name != nil && *label.Name == reqLabel {
					hasLabel = true
					break
				}
			}
			if hasLabel {
				break
			}
		}
		if !hasLabel {
			return false
		}
	}

	return true
}

func (r *Reconciler) createIssueSandbox(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, issue *github.Issue) (*unstructured.Unstructured, error) {
	log := log.FromContext(ctx)
	// Base name matches the issue identifier
	name := fmt.Sprintf("%s-issue-%d", repoWatch.Name, *issue.Number)

	cloneURL := strings.Replace(*issue.RepositoryURL, "api.github.com/repos", "github.com", 1) + ".git"
	repoParts := strings.Split(cloneURL, "/")
	repoName := repoParts[len(repoParts)-1]

	userLogin := user.GetLogin()
	userName := user.GetName()
	if userName == "" {
		userName = userLogin
	}
	userEmail := user.GetEmail()

	// Default bot info to empty (or current user if not using robot account)
	botLogin := ""
	botName := ""
	botEmail := ""

	githubSecretName := repoWatch.Spec.GithubSecretName
	if repoWatch.Spec.Issue.RobotAccount != "" {
		githubSecretName = repoWatch.Spec.Issue.RobotAccount
		if err := r.ensureRobotSecret(ctx, repoWatch.Namespace, githubSecretName); err != nil {
			log.Error(err, "failed to ensure robot secret", "secret", githubSecretName)
			return nil, err
		}

		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: repoWatch.Namespace}, secret); err != nil {
			log.Error(err, "failed to get robot secret", "secret", githubSecretName)
			return nil, err
		}

		if len(secret.Data["userid"]) > 0 {
			botLogin = string(secret.Data["userid"])
		}
		if len(secret.Data["name"]) > 0 {
			botName = string(secret.Data["name"])
		}
		if len(secret.Data["email"]) > 0 {
			botEmail = string(secret.Data["email"])
		}
	}

	originUser := userLogin
	if botLogin != "" {
		originUser = botLogin
	}
	originURL := fmt.Sprintf("github.com/%s/%s", originUser, repoName)
	branchName := fmt.Sprintf("issue-%d-%s", *issue.Number, randString(4))

	log.Info("Generated sandbox for Issue", "issue", *issue)

	// Determine apiKeySecretName from IssueSpec
	apiKeySecretName := repoWatch.Spec.Issue.LLM.APIKeySecretRef
	if apiKeySecretName == "" {
		// Fallback to a default if not specified, to avoid Pod validation error
		apiKeySecretName = "gemini-vscode-tokens"
	}

	dindSupport := repoWatch.Spec.Issue.DindSupport
	if r.ForceSandboxMode != "" {
		dindSupport = r.ForceSandboxMode
	}

	ephemeralStorage := resource.MustParse("6Gi")
	if dindSupport == reviewv1alpha1.DindSupportPrivileged {
		ephemeralStorage = resource.MustParse("40Gi")
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"review.gemini.google.com/repowatch": repoWatch.Name,
				"sandbox.gemini.google.com/type":     "issue",
				"sandbox-type":                       "issue",
			},
			Annotations: map[string]string{
				"agentState": "provisioning",
			},
			CloneURL:              cloneURL,
			HTMLURL:               *issue.HTMLURL,
			Branch:                branchName,
			Origin:                originURL,
			PushEnabled:           false,
			UserLogin:             userLogin,
			UserName:              userName,
			UserEmail:             userEmail,
			BotLogin:              botLogin,
			BotName:               botName,
			BotEmail:              botEmail,
			LLMProvider:           repoWatch.Spec.Issue.LLM.Provider,
			LLMConfigdirRef:       repoWatch.Spec.Issue.LLM.ConfigdirRef,
			LLMAPIKeySecretName:   apiKeySecretName,
			Prompt:                repoWatch.Spec.Issue.LLM.Prompt,
			GithubSecretName:      githubSecretName,
			DevcontainerConfigRef: repoWatch.Spec.Issue.DevcontainerConfigRef,
			Image:                 repoWatch.Spec.Issue.Image,
			RepoSandboxImage:      r.RepoSandboxImage,
			ConfigDirImage:        r.ConfigDirImage,
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "issue-sandbox",
			WorkspaceDiskSize:     repoWatch.Spec.Issue.WorkspaceDiskSize,
			DisableGitHubProxy:    true,
		},
		DindSupport:   dindSupport,
		LLMExtensions: repoWatch.Spec.Issue.LLM.Extensions,
		IssueID:       fmt.Sprintf("%d", *issue.Number),
		IssueTitle:    *issue.Title,
		IssueRepo:     repoWatch.GetName(),
		//Handler:    "", // Handled per task?
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2000m"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
			"ephemeral-storage":   ephemeralStorage,
		},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4000m"),
				corev1.ResourceMemory: resource.MustParse("6Gi"),
				"ephemeral-storage":   ephemeralStorage,
			},
		},
	}

	sb, svc := sandbox.NewAgentSandbox(opt)

	if err := controllerutil.SetControllerReference(repoWatch, sb, r.Scheme); err != nil {
		return nil, err
	}
	if err := controllerutil.SetControllerReference(repoWatch, svc, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.Create(ctx, svc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
	}

	if err := r.Create(ctx, sb); err != nil {
		return nil, err
	}

	return sb, nil
}

func (r *Reconciler) ensureIssueTask(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, sandbox client.Object, sandboxName string, issue *github.Issue, handler reviewv1alpha1.IssueHandlerSpec) error {
	taskName := fmt.Sprintf("%s-%s", sandboxName, handler.Name) // e.g. repo-issue-123-triage

	// Check if task exists
	task := &sandboxtaskv1alpha1.SandboxTask{}
	err := r.Get(ctx, types.NamespacedName{Name: taskName, Namespace: repoWatch.Namespace}, task)
	if err == nil {
		return nil // Task exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Create Task
	prompt, err := r.generateIssueHandlerPrompt(handler, issue)
	if err != nil {
		return err
	}

	draftPR := false
	if repoWatch.Spec.Issue.RobotAccount == "" {
		if repoWatch.Spec.Issue.DraftPR != nil {
			draftPR = *repoWatch.Spec.Issue.DraftPR
		} else {
			draftPR = true
		}
	} else {
		draftPR = false
	}

	params := map[string]string{
		"ISSUEID":      fmt.Sprintf("%d", *issue.Number),
		"AGENT_PROMPT": prompt,
		"HANDLER_NAME": handler.Name,
		"PR_LABEL":     "repo-agent",
	}
	if draftPR {
		params["DRAFT_PR"] = "true"
	}
	//params["GIT_PUSH_ENABLED"] = "true"
	if repoWatch.Spec.Issue.LLM.Provider != "" {
		params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Issue.LLM.Provider
	}
	if repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}
	if repoWatch.Spec.Issue.LLM.ConfigdirRef != "" {
		params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Issue.LLM.ConfigdirRef
	}
	if len(repoWatch.Spec.Issue.LLM.Extensions) > 0 {
		exts, _ := json.Marshal(repoWatch.Spec.Issue.LLM.Extensions)
		params["AGENT_LLM_EXTENSIONS"] = string(exts)
	}
	if len(repoWatch.Spec.Issue.Models) > 0 {
		params["model"] = strings.Join(repoWatch.Spec.Issue.Models, ",")
	}

	taskType := handler.TaskType
	if taskType == "" {
		taskType = "issue"
	}

	return r.createSandboxTask(ctx, repoWatch, sandbox, sandboxName, taskName, taskType, params)
}

// generateIssueHandlerPrompt generates a prompt for an issue handler.
func (r *Reconciler) generateIssueHandlerPrompt(handler reviewv1alpha1.IssueHandlerSpec, issue *github.Issue) (string, error) {
	return prompts.ExpandIssueHandlerPrompt(handler.Prompt, issue)
}

// createReviewSandboxForPR creates a ReviewSandbox for a pull request.
// It uses the LLM configuration from the RepoWatch CRD to configure the
// sandbox.
func (r *Reconciler) createReviewSandboxForPR(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, pr *github.PullRequest) error {
	log := log.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-pr-%d", repoWatch.Name, *pr.Number)

	prompt := repoWatch.Spec.Review.LLM.Prompt

	userLogin := user.GetLogin()
	userName := user.GetName()
	if userName == "" {
		userName = userLogin
	}
	userEmail := user.GetEmail()

	githubSecretName := repoWatch.Spec.GithubSecretName
	botLogin := ""
	botName := ""
	botEmail := ""
	if repoWatch.Spec.Review.RobotAccount != "" {
		githubSecretName = repoWatch.Spec.Review.RobotAccount
		if err := r.ensureRobotSecret(ctx, repoWatch.Namespace, githubSecretName); err != nil {
			log.Error(err, "failed to ensure robot secret", "secret", githubSecretName)
			return err
		}

		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: githubSecretName, Namespace: repoWatch.Namespace}, secret); err != nil {
			log.Error(err, "failed to get robot secret", "secret", githubSecretName)
			return err
		}

		if len(secret.Data["userid"]) > 0 {
			botLogin = string(secret.Data["userid"])
		}
		if len(secret.Data["name"]) > 0 {
			botName = string(secret.Data["name"])
		}
		if len(secret.Data["email"]) > 0 {
			botEmail = string(secret.Data["email"])
		}
	}

	log.Info("Generated Sandbox for PR", "pr", *pr, "llm.provider", repoWatch.Spec.Review.LLM.Provider)

	opt := sandbox.ReviewSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"review.gemini.google.com/repowatch": repoWatch.Name,
				"sandbox.gemini.google.com/type":     "review",
			},
			UserLogin:   userLogin,
			UserName:    userName,
			UserEmail:   userEmail,
			BotLogin:    botLogin,
			BotName:     botName,
			BotEmail:    botEmail,
			LLMProvider: repoWatch.Spec.Review.LLM.Provider, LLMConfigdirRef: repoWatch.Spec.Review.LLM.ConfigdirRef,
			LLMAPIKeySecretName:   repoWatch.Spec.Review.LLM.APIKeySecretRef,
			Prompt:                repoWatch.Spec.Review.LLM.Prompt,
			GithubSecretName:      githubSecretName,
			DevcontainerConfigRef: repoWatch.Spec.Review.DevcontainerConfigRef,
			Image:                 repoWatch.Spec.Review.Image,
			RepoSandboxImage:      r.RepoSandboxImage,
			ConfigDirImage:        r.ConfigDirImage,
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "review-sandbox",
			DindSupport: func() string {
				if r.ForceSandboxMode != "" {
					return r.ForceSandboxMode
				}
				return repoWatch.Spec.Review.DindSupport
			}(),
			DisableGitHubProxy: true,
		},
		PRNumber:          *pr.Number,
		PRTitle:           *pr.Title,
		PRHTMLURL:         *pr.HTMLURL,
		PRDiffURL:         *pr.DiffURL,
		PRCloneURL:        fmt.Sprintf("%s#refs/heads/%s", *pr.Head.Repo.CloneURL, *pr.Head.Ref),
		RepoName:          repoWatch.GetName(),
		MaxReviewFiles:    repoWatch.Spec.Review.MaxReviewFiles,
		IgnoreFiles:       repoWatch.Spec.Review.IgnoreFiles,
		SeverityThreshold: repoWatch.Spec.Review.SeverityThreshold,
		LLMExtensions:     repoWatch.Spec.Review.LLM.Extensions,
		WorkspaceDiskSize: repoWatch.Spec.Review.WorkspaceDiskSize,
	}

	sb, svc := sandbox.NewReviewSandbox(opt)

	if err := controllerutil.SetControllerReference(repoWatch, sb, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, sb); err != nil {
		return err
	}

	if err := controllerutil.SetControllerReference(repoWatch, svc, r.Scheme); err != nil {
		log.Error(err, "failed to set owner ref on service")
	}

	if err := r.Create(ctx, svc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	if err := r.createSandboxTask(ctx, repoWatch, sb, sandboxName, "", "review", map[string]string{
		"AGENT_PROMPT": prompt,
	}); err != nil {
		log.Error(err, "unable to create initial review task for sandbox", "sandbox", sandboxName)
	}

	return nil
}

// createSandboxTask creates a SandboxTask for a sandbox.
func (r *Reconciler) createSandboxTask(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, owner client.Object, sandboxName string, name string, taskType string, params map[string]string) error {
	taskName := name
	if taskName == "" {
		taskName = fmt.Sprintf("%s-task-%d-%s", sandboxName, time.Now().Unix(), strings.ToLower(randString(4)))
	}

	task := &sandboxtaskv1alpha1.SandboxTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: repoWatch.Namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/sandbox-name": sandboxName,
				"review.gemini.google.com/repowatch":     repoWatch.Name,
			},
		},
		Spec: sandboxtaskv1alpha1.SandboxTaskSpec{
			SandboxName: sandboxName,
			Type:        taskType,
			Params:      params,
		},
	}

	if err := controllerutil.SetControllerReference(owner, task, r.Scheme); err != nil {
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

func (r *Reconciler) reconcileDevSandboxes(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, upstreamRepo string, podsBySandbox map[string]*corev1.Pod) error {
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

	watchedDevSandboxes, pendingDevBranches, err := r.reconcileDevSandboxesInternal(ctx, user, repoWatch, branches, forkOwner, forkRepo, podsBySandbox)
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

func (r *Reconciler) reconcileDevSandboxesInternal(ctx context.Context, user *github.User, repoWatch *reviewv1alpha1.RepoWatch, branches []*github.Branch, forkOwner, forkRepo string, podsBySandbox map[string]*corev1.Pod) ([]reviewv1alpha1.DevSandbox, []string, error) {
	log := log.FromContext(ctx)
	// 6. List Existing DevSandboxes
	sandboxList := &unstructured.UnstructuredList{}
	sandboxGVK := schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	}
	sandboxList.SetGroupVersionKind(sandboxGVK)
	if err := r.List(ctx, sandboxList, client.InNamespace(repoWatch.Namespace), client.MatchingLabels{"sandbox.gemini.google.com/type": "dev"}); err != nil {
		return nil, nil, fmt.Errorf("listing dev sandboxes: %w", err)
	}

	ownedSandboxes := getOwnedSandboxes(sandboxList.Items, repoWatch.UID)

	activeSandboxes := 0
	watchedDevSandboxes := []reviewv1alpha1.DevSandbox{}
	pendingDevBranches := []string{}

	// Identify which branches we want to have sandboxes for.
	desiredBranches := make(map[string]bool)
	for _, b := range branches {
		desiredBranches[b.GetName()] = true
	}

	for _, sandbox := range ownedSandboxes {
		// Get branch from annotations
		branch := sandbox.GetAnnotations()["sandbox.gemini.google.com/branch"]
		if branch == "" {
			log.Error(nil, "unable to get branch from sandbox annotations", "sandbox", sandbox.GetName())
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

		sandboxStatus, err := r.reconcileSandboxPodStatus(ctx, &sandbox, podsBySandbox, scaledDown)
		if err != nil {
			log.Error(err, "unable to reconcile sandbox pod status", "sandbox", sandbox.GetName())
		}

		watchedDevSandboxes = append(watchedDevSandboxes, reviewv1alpha1.DevSandbox{
			BranchName:  branch,
			SandboxName: sandbox.GetName(),
			Status:      sandboxStatus,
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
		sandboxName := fmt.Sprintf("dev-%s", hashedSuffix)

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
	cloneURL := strings.TrimSuffix(repoWatch.Spec.RepoURL, ".git") + ".git"
	originURL := fmt.Sprintf("github.com/%s/%s.git", forkOwner, forkRepo)

	userLogin := user.GetLogin()
	userName := user.GetName()
	if userName == "" {
		userName = userLogin
	}
	userEmail := user.GetEmail()

	opts := sandbox.DevSandboxOptions{
		Name:      sandboxName,
		Namespace: repoWatch.Namespace,
		Labels: map[string]string{
			"review.gemini.google.com/repowatch": repoWatch.Name,
			"sandbox.gemini.google.com/type":     "dev",
			"sandbox-type":                       "dev",
		},
		CloneURL: cloneURL,
		HTMLURL:  strings.TrimSuffix(repoWatch.Spec.RepoURL, ".git"),

		Branch:      branchName,
		Origin:      originURL,
		PushEnabled: true,
		UserLogin:   userLogin,
		UserName:    userName,
		UserEmail:   userEmail,

		LLMProvider:         repoWatch.Spec.Dev.LLM.Provider,
		LLMConfigdirRef:     repoWatch.Spec.Dev.LLM.ConfigdirRef,
		LLMAPIKeySecretName: repoWatch.Spec.Dev.LLM.APIKeySecretRef,

		GithubSecretName:      repoWatch.Spec.GithubSecretName,
		DevcontainerConfigRef: repoWatch.Spec.Dev.DevcontainerConfigRef,
		Image:                 repoWatch.Spec.Dev.Image,
		RepoSandboxImage:      r.RepoSandboxImage,
		ConfigDirImage:        r.ConfigDirImage,

		HTTPEnabled:        true,
		Replicas:           1,
		ServiceAccountName: "issue-sandbox",
		DindSupport: func() string {
			if r.ForceSandboxMode != "" {
				return r.ForceSandboxMode
			}
			return repoWatch.Spec.Dev.DindSupport
		}(),
		WorkspaceDiskSize: repoWatch.Spec.Dev.WorkspaceDiskSize,
	}

	sb, svc := sandbox.NewDevSandbox(opts)

	if err := controllerutil.SetControllerReference(repoWatch, sb, r.Scheme); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(repoWatch, svc, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, svc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	if err := r.Create(ctx, sb); err != nil {
		return err
	}

	params := map[string]string{
		"REPO_URL":          opts.CloneURL, // CloneURL is the upstream repository URL
		"BRANCH_NAME":       branchName,
		"GITHUB_USER_LOGIN": opts.UserLogin,
		"GITHUB_USER_EMAIL": opts.UserEmail,
		"GITHUB_USER_NAME":  opts.UserName,
	}
	if repoWatch.Spec.Dev.LLM.Prompt != "" {
		params["AGENT_PROMPT"] = repoWatch.Spec.Dev.LLM.Prompt
	}
	if len(repoWatch.Spec.Dev.LLM.Extensions) > 0 {
		exts, _ := json.Marshal(repoWatch.Spec.Dev.LLM.Extensions)
		params["AGENT_LLM_EXTENSIONS"] = string(exts)
	}

	return r.createSandboxTask(ctx, repoWatch, sb, sb.GetName(), "", "dev-setup", params)
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&reviewv1alpha1.RepoWatch{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		// Owns(&reviewv1alpha1.ReviewSandbox{}).
		Complete(r)
}

func (r *Reconciler) ensureRobotSecret(ctx context.Context, namespace, secretName string) error {
	// Check if secret exists in namespace
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)
	if err == nil {
		return nil // Secret exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Secret not found, try to copy from system namespace
	systemNamespace := os.Getenv("REPO_AGENT_SYSTEM_NAMESPACE")
	if systemNamespace == "" {
		systemNamespace = "repo-agent-system"
	}

	sourceSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: systemNamespace}, sourceSecret); err != nil {
		return fmt.Errorf("failed to find robot secret %s in %s: %w", secretName, systemNamespace, err)
	}

	// Create secret in target namespace
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        secretName,
			Namespace:   namespace,
			Labels:      sourceSecret.Labels,
			Annotations: sourceSecret.Annotations,
		},
		Data: sourceSecret.Data,
		Type: sourceSecret.Type,
	}

	return r.Create(ctx, newSecret)
}

func (r *Reconciler) unpauseSandboxIfPendingTasks(ctx context.Context, sandbox *unstructured.Unstructured) (bool, error) {
	log := log.FromContext(ctx)

	// Check if paused (replicas == 0)
	replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if err != nil || !found {
		// If field missing, default is usually 1, so not paused.
		return false, nil
	}
	if replicas > 0 {
		return false, nil
	}

	// List tasks
	tasks := &sandboxtaskv1alpha1.SandboxTaskList{}
	if err := r.List(ctx, tasks, client.InNamespace(sandbox.GetNamespace()), client.MatchingLabels{"sandbox.gemini.google.com/sandbox-name": sandbox.GetName()}); err != nil {
		return false, err
	}

	hasPending := false
	for _, task := range tasks.Items {
		state := task.Status.TaskState
		// Pending (default if empty) or Running
		if state == "" || state == "Pending" || state == "Running" {
			hasPending = true
			break
		}
	}

	if hasPending {
		log.Info("Unpausing sandbox due to pending tasks", "sandbox", sandbox.GetName())
		if err := unstructured.SetNestedField(sandbox.Object, int64(1), "spec", "replicas"); err != nil {
			return false, err
		}
		return true, r.Update(ctx, sandbox)
	}
	return false, nil
}

func (r *Reconciler) pauseSandboxIfIdle(ctx context.Context, sandbox *unstructured.Unstructured, shutdownDuration time.Duration) (bool, error) {
	log := log.FromContext(ctx)

	// Check for manual override annotation
	annotations := sandbox.GetAnnotations()
	if val, ok := annotations["sandbox.gemini.google.com/prevent-auto-shutdown"]; ok && val == "true" {
		// Log only at debug level to avoid spam, or Info if occasional
		log.V(4).Info("Skipping auto-pause due to manual override", "sandbox", sandbox.GetName())
		return false, nil
	}

	// Check if running (replicas > 0)
	replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if err == nil && found && replicas == 0 {
		return false, nil // Already paused
	}

	// List tasks
	tasks := &sandboxtaskv1alpha1.SandboxTaskList{}
	if err := r.List(ctx, tasks, client.InNamespace(sandbox.GetNamespace()), client.MatchingLabels{"sandbox.gemini.google.com/sandbox-name": sandbox.GetName()}); err != nil {
		return false, err
	}

	// Check if all tasks are completed and find latest completion time
	latestTime := sandbox.GetCreationTimestamp().Time

	for _, task := range tasks.Items {
		state := task.Status.TaskState
		if state != "Completed" && state != "Failed" {
			// Found an active task, do not pause
			return false, nil
		}

		// Check completion time
		annotations := task.GetAnnotations()
		if tsStr, ok := annotations["sandbox.gemini.google.com/completion-time"]; ok {
			if ts, err := time.Parse(time.RFC3339, tsStr); err == nil {
				if ts.After(latestTime) {
					latestTime = ts
				}
			}
		}
	}

	if time.Since(latestTime) > shutdownDuration {
		log.Info("Pausing sandbox (idle)", "sandbox", sandbox.GetName(), "lastActivity", latestTime)
		if err := unstructured.SetNestedField(sandbox.Object, int64(0), "spec", "replicas"); err != nil {
			return false, err
		}
		return true, r.Update(ctx, sandbox)
	}

	return false, nil
}

func (r *Reconciler) manageSandboxLifecycle(ctx context.Context, sandbox *unstructured.Unstructured, shutdownDuration time.Duration) (bool, error) {
	replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if err != nil || !found {
		// If field missing, assume it's running (default behavior usually)
		replicas = 1
	}

	if replicas == 0 {
		return r.unpauseSandboxIfPendingTasks(ctx, sandbox)
	}

	if shutdownDuration > 0 {
		return r.pauseSandboxIfIdle(ctx, sandbox, shutdownDuration)
	}
	return false, nil
}

func (r *Reconciler) reconcileIssueFeedback(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, sandbox *unstructured.Unstructured, issue *github.Issue, ghClient *github.Client) error {
	log := log.FromContext(ctx)

	// Check if we have an active address-feedback task
	tasks := &sandboxtaskv1alpha1.SandboxTaskList{}
	if err := r.List(ctx, tasks, client.InNamespace(sandbox.GetNamespace()), client.MatchingLabels{"sandbox.gemini.google.com/sandbox-name": sandbox.GetName()}); err != nil {
		return err
	}

	if len(tasks.Items) == 0 {
		return nil
	}

	activeTaskExists := false
	var lastAddressFeedbackTaskTime time.Time

	for _, task := range tasks.Items {
		// If ANY task is active, skip creating a new one
		state := task.Status.TaskState
		if state == "" || state == "Pending" || state == "Running" {
			activeTaskExists = true
		}

		if task.Spec.Type == "address-feedback" {
			// Track the latest address-feedback task
			if task.CreationTimestamp.Time.After(lastAddressFeedbackTaskTime) {
				lastAddressFeedbackTaskTime = task.CreationTimestamp.Time
			}
		}
	}

	if activeTaskExists {
		return nil
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		return err
	}

	pr, err := r.getLinkedPRFromSandbox(ctx, ghClient, sandbox)
	if err != nil {
		return err
	}
	if pr == nil {
		return nil
	}

	if pr.GetState() != "open" {
		return nil
	}

	// Fetch PR commits to find the latest one to establish a baseline time
	var latestCommitTime time.Time
	var latestCommitAuthorLogin string
	opts := &github.ListOptions{PerPage: 100}
	commitsFound := false
	for {
		commits, resp, err := ghClient.PullRequests.ListCommits(ctx, owner, repo, *pr.Number, opts)
		if err != nil {
			log.Error(err, "unable to list commits for PR", "pr", pr.Number)
			break
		}
		for _, commit := range commits {
			commitsFound = true
			if t := commit.GetCommit().GetCommitter().GetDate(); t.After(latestCommitTime) {
				latestCommitTime = t
				if commit.Author != nil {
					latestCommitAuthorLogin = commit.Author.GetLogin()
				}
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if !commitsFound {
		return nil
	}

	// Check for new feedback
	hasNew, latestFeedbackTime, err := r.hasNewFeedback(ctx, ghClient, owner, repo, pr, issue, latestCommitTime, latestCommitAuthorLogin)
	if err != nil {
		log.Error(err, "checking for new feedback", "pr", pr.Number)
		return nil
	}

	if hasNew {
		// Check if we have already created a task after the latest feedback
		if !lastAddressFeedbackTaskTime.IsZero() && lastAddressFeedbackTaskTime.After(latestFeedbackTime) {
			log.Info("Skipping address-feedback: last attempt was after latest feedback", "pr", *pr.Number, "lastAttempt", lastAddressFeedbackTaskTime, "latestFeedback", latestFeedbackTime)
			return nil
		}

		log.Info("Found new feedback, creating address-feedback task", "pr", pr.Number)
		params := map[string]string{
			"PULL_REQUEST_ID": fmt.Sprintf("%d", *pr.Number),
			"ISSUE_URL":       *issue.HTMLURL,
			"AGENT_PROMPT":    repoWatch.Spec.Issue.LLM.Prompt,
		}
		// Add LLM params
		if repoWatch.Spec.Issue.LLM.Provider != "" {
			params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Issue.LLM.Provider
		}
		if repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
			params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Issue.LLM.APIKeySecretRef
		}
		if repoWatch.Spec.Issue.LLM.ConfigdirRef != "" {
			params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Issue.LLM.ConfigdirRef
		}
		if len(repoWatch.Spec.Issue.LLM.Extensions) > 0 {
			exts, _ := json.Marshal(repoWatch.Spec.Issue.LLM.Extensions)
			params["AGENT_LLM_EXTENSIONS"] = string(exts)
		}
		if len(repoWatch.Spec.Issue.Models) > 0 {
			params["model"] = strings.Join(repoWatch.Spec.Issue.Models, ",")
		}

		// Ensure sandbox is scaled up
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err != nil || !found || replicas == 0 {
			if err := unstructured.SetNestedField(sandbox.Object, int64(1), "spec", "replicas"); err != nil {
				log.Error(err, "unable to set replicas to 1")
			} else {
				if err := r.Update(ctx, sandbox); err != nil {
					log.Error(err, "unable to scale up sandbox")
				}
			}
		}

		return r.createSandboxTask(ctx, repoWatch, sandbox, sandbox.GetName(), "", "address-feedback", params)
	}

	return nil
}

func (r *Reconciler) reconcilePRFailures(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, sandbox *unstructured.Unstructured, issue *github.Issue, ghClient *github.Client) error {
	log := log.FromContext(ctx)

	// Check if we have an active task
	tasks := &sandboxtaskv1alpha1.SandboxTaskList{}
	if err := r.List(ctx, tasks, client.InNamespace(sandbox.GetNamespace()), client.MatchingLabels{"sandbox.gemini.google.com/sandbox-name": sandbox.GetName()}); err != nil {
		return err
	}

	if len(tasks.Items) == 0 {
		return nil
	}

	activeTaskExists := false
	var lastInvestigateFailuresTaskTime time.Time

	for _, task := range tasks.Items {
		// If ANY task is active, skip creating a new one
		state := task.Status.TaskState
		if state == "" || state == "Pending" || state == "Running" {
			activeTaskExists = true
		}

		if task.Spec.Type == "investigate-failures" {
			// Track the latest investigate-failures task
			if task.CreationTimestamp.Time.After(lastInvestigateFailuresTaskTime) {
				lastInvestigateFailuresTaskTime = task.CreationTimestamp.Time
			}
		}
	}

	if activeTaskExists {
		return nil
	}

	owner, repo, err := parseRepoURL(repoWatch.Spec.RepoURL)
	if err != nil {
		return err
	}

	pr, err := r.getLinkedPRFromSandbox(ctx, ghClient, sandbox)
	if err != nil {
		return err
	}
	if pr == nil {
		return nil
	}

	if pr.GetState() != "open" {
		return nil
	}

	// Check for failures on the latest commit (HEAD)
	sha := pr.GetHead().GetSHA()

	// 1. Check Statuses
	combinedStatus, _, err := ghClient.Repositories.GetCombinedStatus(ctx, owner, repo, sha, nil)
	if err != nil {
		log.Error(err, "unable to get combined status", "sha", sha)
		return nil
	}

	failed := false
	if combinedStatus.GetState() == "failure" || combinedStatus.GetState() == "error" {
		failed = true
	}

	// 2. Check CheckRuns
	if !failed {
		checkRuns, err := listAllCheckRuns(ctx, ghClient, owner, repo, sha)
		if err != nil {
			log.Error(err, "unable to list check runs", "sha", sha)
			return nil
		}
		for _, cr := range checkRuns {
			if cr.GetConclusion() == "failure" || cr.GetConclusion() == "timed_out" || cr.GetConclusion() == "action_required" {
				failed = true
				break
			}
		}
	}

	if failed {
		// Get head commit time to avoid re-triggering for the same commit if we already tried
		commit, _, err := ghClient.Repositories.GetCommit(ctx, owner, repo, sha, nil)
		if err != nil {
			log.Error(err, "unable to get head commit", "sha", sha)
			return nil
		}
		latestCommitTime := commit.GetCommit().GetCommitter().GetDate()

		if !lastInvestigateFailuresTaskTime.IsZero() && lastInvestigateFailuresTaskTime.After(latestCommitTime) {
			log.Info("Skipping investigate-failures: last attempt was after latest commit", "pr", *pr.Number, "lastAttempt", lastInvestigateFailuresTaskTime, "latestCommit", latestCommitTime)
			return nil
		}

		log.Info("Found failures on latest commit, creating investigate-failures task", "pr", pr.Number, "sha", sha)
		params := map[string]string{
			"PULL_REQUEST_ID": fmt.Sprintf("%d", *pr.Number),
			"ISSUE_URL":       *issue.HTMLURL,
			"AGENT_PROMPT":    repoWatch.Spec.Issue.LLM.Prompt,
		}
		// Add LLM params
		if repoWatch.Spec.Issue.LLM.Provider != "" {
			params["AGENT_LLM_PROVIDER"] = repoWatch.Spec.Issue.LLM.Provider
		}
		if repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
			params["AGENT_LLM_API_KEY_SECRET"] = repoWatch.Spec.Issue.LLM.APIKeySecretRef
		}
		if repoWatch.Spec.Issue.LLM.ConfigdirRef != "" {
			params["AGENT_LLM_CONFIGDIR"] = repoWatch.Spec.Issue.LLM.ConfigdirRef
		}
		if len(repoWatch.Spec.Issue.LLM.Extensions) > 0 {
			exts, _ := json.Marshal(repoWatch.Spec.Issue.LLM.Extensions)
			params["AGENT_LLM_EXTENSIONS"] = string(exts)
		}
		if len(repoWatch.Spec.Issue.Models) > 0 {
			params["model"] = strings.Join(repoWatch.Spec.Issue.Models, ",")
		}

		// Ensure sandbox is scaled up
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err != nil || !found || replicas == 0 {
			if err := unstructured.SetNestedField(sandbox.Object, int64(1), "spec", "replicas"); err != nil {
				log.Error(err, "unable to set replicas to 1")
			} else {
				if err := r.Update(ctx, sandbox); err != nil {
					log.Error(err, "unable to scale up sandbox")
				}
			}
		}

		return r.createSandboxTask(ctx, repoWatch, sandbox, sandbox.GetName(), "", "investigate-failures", params)
	}

	return nil
}

var prURLRegex = regexp.MustCompile(`https://github\.com/[\w-]+/[\w-]+/pull/\d+`)

func (r *Reconciler) getLinkedPRFromSandbox(ctx context.Context, ghClient *github.Client, sandbox *unstructured.Unstructured) (*github.PullRequest, error) {
	// List tasks
	tasks := &sandboxtaskv1alpha1.SandboxTaskList{}
	if err := r.List(ctx, tasks, client.InNamespace(sandbox.GetNamespace()), client.MatchingLabels{"sandbox.gemini.google.com/sandbox-name": sandbox.GetName()}); err != nil {
		return nil, err
	}

	for _, task := range tasks.Items {
		annotations := task.GetAnnotations()
		agentDraft, ok := annotations["agentDraft"]
		if !ok || agentDraft == "" {
			continue
		}

		matches := prURLRegex.FindAllString(agentDraft, -1)
		for _, match := range matches {
			prRef, err := pkg_github.ParsePullRequestURL(match)
			if err != nil {
				continue
			}

			pr, _, err := ghClient.PullRequests.Get(ctx, prRef.Repo.Owner, prRef.Repo.Name, prRef.PullRequestNumber)
			if err != nil {
				continue
			}
			return pr, nil
		}
	}
	return nil, nil
}

func (r *Reconciler) hasNewFeedback(ctx context.Context, ghClient *github.Client, owner, repo string, pr *github.PullRequest, issue *github.Issue, since time.Time, latestCommitAuthorLogin string) (bool, time.Time, error) {
	var latestFeedbackTime time.Time
	found := false

	// Check PR comments
	comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, *pr.Number, &github.IssueListCommentsOptions{
		Since: &since,
	})
	if err != nil {
		return false, time.Time{}, err
	}
	for _, c := range comments {
		if c.CreatedAt != nil && c.CreatedAt.After(since) {
			if c.User.GetLogin() == latestCommitAuthorLogin {
				continue
			}
			found = true
			if c.CreatedAt.After(latestFeedbackTime) {
				latestFeedbackTime = *c.CreatedAt
			}
		}
	}

	// Check PR reviews
	reviews, _, err := ghClient.PullRequests.ListReviews(ctx, owner, repo, *pr.Number, nil)
	if err != nil {
		return false, time.Time{}, err
	}
	for _, rev := range reviews {
		if rev.SubmittedAt != nil && rev.SubmittedAt.After(since) {
			if rev.User.GetLogin() == latestCommitAuthorLogin {
				continue
			}
			found = true
			if rev.SubmittedAt.After(latestFeedbackTime) {
				latestFeedbackTime = *rev.SubmittedAt
			}
		}
	}

	// Check Issue comments
	issueComments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, *issue.Number, &github.IssueListCommentsOptions{
		Since: &since,
	})
	if err != nil {
		return false, time.Time{}, err
	}
	for _, c := range issueComments {
		if c.CreatedAt != nil && c.CreatedAt.After(since) {
			// We use the latest commit author (likely the bot/agent) to filter out
			// comments made by the agent itself on the issue.
			if c.User.GetLogin() == latestCommitAuthorLogin {
				continue
			}
			found = true
			if c.CreatedAt.After(latestFeedbackTime) {
				latestFeedbackTime = *c.CreatedAt
			}
		}
	}

	return found, latestFeedbackTime, nil
}

func (r *Reconciler) reconcileSandboxPodStatus(ctx context.Context, sandbox *unstructured.Unstructured, podsBySandbox map[string]*corev1.Pod, scaledDown bool) (string, error) {
	log := log.FromContext(ctx)
	podName := sandbox.GetName()
	pod := podsBySandbox[podName]
	sandboxStatus := "Active"
	if scaledDown {
		sandboxStatus = "ScaledDown"
	}

	podStatusStr := ""
	if pod != nil {
		if pod.Status.Reason == "Evicted" {
			if pod.Status.Message != "" {
				podStatusStr = fmt.Sprintf("Evicted: %s", pod.Status.Message)
			} else {
				podStatusStr = "Evicted"
			}
		} else if pod.Status.Phase == corev1.PodFailed {
			podStatusStr = fmt.Sprintf("fail: %s", pod.Status.Reason)
		} else if pod.Status.Phase == corev1.PodPending {
			podStatusStr = "Pending"
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
					podStatusStr = fmt.Sprintf("Pending: %s", cond.Message)
					break
				}
			}
		} else {
			podStatusStr = string(pod.Status.Phase)
		}
		sandboxStatus = podStatusStr
	}

	updateAnnotation := false
	annotations := sandbox.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	shouldPersist := podStatusStr != ""
	if shouldPersist {
		if annotations["sandbox.gemini.google.com/pod-status"] != podStatusStr {
			annotations["sandbox.gemini.google.com/pod-status"] = podStatusStr
			updateAnnotation = true
		}
	} else {
		if _, ok := annotations["sandbox.gemini.google.com/pod-status"]; ok {
			delete(annotations, "sandbox.gemini.google.com/pod-status")
			updateAnnotation = true
		}
	}

	if updateAnnotation {
		sandbox.SetAnnotations(annotations)
		if err := r.Update(ctx, sandbox); err != nil {
			log.Error(err, "failed to update sandbox annotation for pod status", "sandbox", sandbox.GetName())
			return sandboxStatus, err
		}
	}

	return sandboxStatus, nil
}

func listAllCheckRuns(ctx context.Context, client *github.Client, owner, repo, ref string) ([]*github.CheckRun, error) {
	var allRuns []*github.CheckRun
	opts := &github.ListCheckRunsOptions{
		ListOptions: github.ListOptions{
			PerPage: 200,
		},
	}
	for {
		runs, resp, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
		if err != nil {
			return nil, err
		}
		allRuns = append(allRuns, runs.CheckRuns...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allRuns, nil
}
