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

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// RepoBoard endpoints (docs/design/repoboard.md §7). Phase 1 scope: personal
// boards in the session namespace. The board itself is computed live from
// GitHub involvement + factory sandboxes — no stored queue.

var repoBoardGVR = schema.GroupVersionResource{
	Group:    "board.gemini.google.com",
	Version:  "v1alpha1",
	Resource: "repoboards",
}

const (
	annoBoardRequests = "board.gemini.google.com/requests"

	attentionNeedsYou = "needs-you"
	attentionWorking  = "working"
	attentionWaiting  = "waiting"
)

// githubClientForToken is injectable for tests.
var githubClientForToken = func(ctx context.Context, token string) *github.Client {
	return clients.NewGitHubClient(ctx, token)
}

// memberToken resolves the session member's GitHub token (manual_pat >
// oauth_pat > pat) from their namespace.
func (s *Server) memberToken(ctx context.Context, namespace string) (string, error) {
	sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "github-pat", v1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, key := range []string{"manual_pat", "oauth_pat", "pat"} {
		if v, ok := sec.Data[key]; ok && len(v) > 0 {
			return string(v), nil
		}
	}
	return "", fmt.Errorf("no github token in secret %s/github-pat", namespace)
}

func (s *Server) getBoard(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
}

// repoPermCache caches "does this user's token have push on that repo"
// verdicts (design §4.1: ~15 min, so GitHub-side revocation propagates).
var repoPermCache = struct {
	sync.Mutex
	entries map[string]repoPermEntry
}{entries: map[string]repoPermEntry{}}

type repoPermEntry struct {
	allowed bool
	expires time.Time
}

func (s *Server) hasPushPermission(ctx context.Context, namespace, sessionUser, repoURL string) bool {
	key := sessionUser + "|" + repoURL
	repoPermCache.Lock()
	if e, ok := repoPermCache.entries[key]; ok && time.Now().Before(e.expires) {
		repoPermCache.Unlock()
		return e.allowed
	}
	repoPermCache.Unlock()

	allowed := false
	if owner, repo, err := parseRepoURL(repoURL); err == nil {
		if token, err := s.memberToken(ctx, namespace); err == nil {
			gh := githubClientForToken(ctx, token)
			if repository, _, err := gh.Repositories.Get(ctx, owner, repo); err == nil {
				perms := repository.GetPermissions()
				allowed = perms["push"] || perms["maintain"] || perms["admin"]
			}
		}
	}
	repoPermCache.Lock()
	repoPermCache.entries[key] = repoPermEntry{allowed: allowed, expires: time.Now().Add(15 * time.Minute)}
	repoPermCache.Unlock()
	return allowed
}

// boardMember applies the access gates (design §4.1): the viewer is a
// member if the board lives in their namespace, their login is in the
// allow list (mode list), or their own token proves push+ on the repo
// (mode github). Returns the member login used for involvement queries.
func (s *Server) boardMember(ctx context.Context, board *unstructured.Unstructured, namespace, sessionUser string) (string, error) {
	mode, _, _ := unstructured.NestedString(board.Object, "spec", "access", "mode")
	if mode == "list" {
		allow, _, _ := unstructured.NestedStringSlice(board.Object, "spec", "access", "allow")
		for _, u := range allow {
			if strings.EqualFold(u, sessionUser) {
				return u, nil
			}
		}
		return "", fmt.Errorf("user %s is not a member of board %s", sessionUser, board.GetName())
	}
	// mode github (or empty)
	if board.GetNamespace() == namespace {
		return sessionUser, nil
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	if s.hasPushPermission(ctx, namespace, sessionUser, repoURL) {
		return sessionUser, nil
	}
	return "", fmt.Errorf("user %s has no push permission on %s", sessionUser, repoURL)
}

// visibleBoards lists boards across namespaces the session may see.
func (s *Server) visibleBoards(ctx context.Context, namespace, sessionUser string) []unstructured.Unstructured {
	list, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace("").List(ctx, v1.ListOptions{})
	if err != nil {
		return nil
	}
	var visible []unstructured.Unstructured
	for _, item := range list.Items {
		if _, err := s.boardMember(ctx, &item, namespace, sessionUser); err == nil {
			visible = append(visible, item)
		}
	}
	return visible
}

// resolveBoard finds a visible board by name, preferring the session
// namespace on name collisions.
func (s *Server) resolveBoard(ctx context.Context, namespace, sessionUser, name string) (*unstructured.Unstructured, string, error) {
	if board, err := s.getBoard(ctx, namespace, name); err == nil {
		member, err := s.boardMember(ctx, board, namespace, sessionUser)
		if err == nil {
			return board, member, nil
		}
	}
	for _, board := range s.visibleBoards(ctx, namespace, sessionUser) {
		if board.GetName() == name {
			member, err := s.boardMember(ctx, &board, namespace, sessionUser)
			if err != nil {
				return nil, "", err
			}
			b := board
			return &b, member, nil
		}
	}
	return nil, "", fmt.Errorf("board %s not found", name)
}

func (s *Server) getBoards(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	boards := []models.Board{}
	for _, item := range s.visibleBoards(c.Request.Context(), namespace, sessionUser) {
		repoURL, _, _ := unstructured.NestedString(item.Object, "spec", "repoURL")
		needsHuman, _, _ := unstructured.NestedInt64(item.Object, "status", "counts", "needsHuman")
		active, _, _ := unstructured.NestedInt64(item.Object, "status", "counts", "active")
		boards = append(boards, models.Board{
			Name:       item.GetName(),
			Namespace:  item.GetNamespace(),
			RepoURL:    repoURL,
			NeedsHuman: int(needsHuman),
			Active:     int(active),
		})
	}
	c.JSON(http.StatusOK, boards)
}

func (s *Server) getBoardWork(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}
	triggerLabel, _, _ := unstructured.NestedString(board.Object, "spec", "triggers", "label")

	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub token unavailable", "details": err.Error()})
		return
	}
	gh := githubClientForToken(ctx, token)

	// Sandboxes live where claims point: the board namespace plus every
	// namespace named by an assignee claim on this repo's items.
	sandboxNamespaces := map[string]bool{}
	sandboxes := map[string]*unstructured.Unstructured{}
	loadSandboxNamespace := func(ns string) {
		if ns == "" || sandboxNamespaces[ns] {
			return
		}
		sandboxNamespaces[ns] = true
		got, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			log.Info("failed to list sandboxes", "namespace", ns, "err", err)
			return
		}
		for k, v := range got {
			sandboxes[k] = v
		}
	}
	loadSandboxNamespace(board.GetNamespace())
	loadSandboxNamespace(namespace)

	items := map[string]*models.WorkItem{}

	// Issues: assigned to the member, plus trigger-labeled ones.
	collect := func(issues []*github.Issue) {
		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			s.mergeIssueRow(items, sandboxes, issue, repo, member, triggerLabel)
		}
	}
	assigned, err := listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Assignee: member})
	if err != nil {
		log.Info("failed to list assigned issues", "err", err)
	}
	var labeled []*github.Issue
	if triggerLabel != "" {
		labeled, err = listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Labels: []string{triggerLabel}})
		if err != nil {
			log.Info("failed to list labeled issues", "err", err)
		}
	}
	// Load claimed executors' namespaces before merging rows so their
	// sandboxes surface on the shared board.
	for _, issue := range append(append([]*github.Issue{}, assigned...), labeled...) {
		for _, a := range issue.Assignees {
			loadSandboxNamespace(strings.ToLower(a.GetLogin()))
		}
	}
	collect(assigned)
	collect(labeled)

	// PRs: authored by / review-requested to the member, trigger-labeled, or
	// with an existing factory sandbox.
	prs, _, err := gh.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		log.Info("failed to list PRs", "err", err)
	}
	for _, pr := range prs {
		s.mergePRRow(items, sandboxes, pr, member, triggerLabel)
	}

	work := make([]models.WorkItem, 0, len(items))
	for _, item := range items {
		work = append(work, *item)
	}
	rank := map[string]int{attentionNeedsYou: 0, attentionWorking: 1, attentionWaiting: 2, "": 3}
	sort.Slice(work, func(i, j int) bool {
		if rank[work[i].Attention] != rank[work[j].Attention] {
			return rank[work[i].Attention] < rank[work[j].Attention]
		}
		return work[i].UpdatedAt > work[j].UpdatedAt
	})
	c.JSON(http.StatusOK, work)
}

func listIssues(ctx context.Context, gh *github.Client, owner, repo string, opts *github.IssueListByRepoOptions) ([]*github.Issue, error) {
	opts.ListOptions = github.ListOptions{PerPage: 100}
	var all []*github.Issue
	for {
		page, resp, err := gh.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

func (s *Server) boardSandboxes(ctx context.Context, namespace, owner, repo string) (map[string]*unstructured.Unstructured, error) {
	list, err := s.K8sManager.ListSandboxes(ctx, namespace, "factory.gemini.google.com/managed=true")
	if err != nil {
		return nil, err
	}
	byName := map[string]*unstructured.Unstructured{}
	repoHint := fmt.Sprintf("github.com/%s/%s/", owner, repo)
	for i := range list.Items {
		sb := &list.Items[i]
		annotations := sb.GetAnnotations()
		if annotations != nil {
			if annoRepo := annotations["repo"]; annoRepo != "" && annoRepo != repo {
				continue
			}
			if htmlURL := annotations["htmlURL"]; htmlURL != "" && !strings.Contains(htmlURL, repoHint) {
				continue
			}
		}
		byName[sb.GetName()] = sb
	}
	return byName, nil
}

func workSandbox(sb *unstructured.Unstructured) *models.WorkSandbox {
	if sb == nil {
		return nil
	}
	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	return &models.WorkSandbox{
		Name:      sb.GetName(),
		Replicas:  fmt.Sprintf("%d", replicas),
		TaskState: sb.GetAnnotations()[annoTaskState],
	}
}

func hasLabel(labels []*github.Label, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l.GetName(), name) {
			return true
		}
	}
	return false
}

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, issue *github.Issue, repo, member, triggerLabel string) {
	key := fmt.Sprintf("issue-%d", issue.GetNumber())
	if _, ok := items[key]; ok {
		return
	}

	assignedToMember := false
	claimedBy := ""
	for _, a := range issue.Assignees {
		if claimedBy == "" {
			claimedBy = a.GetLogin()
		}
		if strings.EqualFold(a.GetLogin(), member) {
			assignedToMember = true
			claimedBy = a.GetLogin()
		}
	}
	labeled := triggerLabel != "" && hasLabel(issue.Labels, triggerLabel)

	sb := sandboxes[fmt.Sprintf("fix-%s-%d", repo, issue.GetNumber())]
	state := ""
	prURL := ""
	if sb != nil {
		annotations := sb.GetAnnotations()
		state = annotations[annoTaskState]
		if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
			prURL = u
		}
	}

	stage, attention := "open", ""
	switch {
	case state == "Running":
		stage, attention = "fixing", attentionWorking
	case state == "Failed":
		stage, attention = "fix-failed", attentionNeedsYou
	case state == "Completed" && prURL != "":
		stage, attention = "pr-open", attentionNeedsYou
	case state == "Completed":
		stage, attention = "fix-done", attentionNeedsYou
	case labeled && assignedToMember:
		stage, attention = "queued", attentionWaiting
	case labeled && !assignedToMember:
		// Executor-consent rule: labeled but not consented — awaiting the
		// member's go.
		stage, attention = "awaiting-go", attentionNeedsYou
	}

	items[key] = &models.WorkItem{
		Type:      "issue",
		Number:    issue.GetNumber(),
		Title:     issue.GetTitle(),
		HTMLURL:   issue.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		ClaimedBy: claimedBy,
		PRURL:     prURL,
		Sandbox:   workSandbox(sb),
		UpdatedAt: issue.GetUpdatedAt().UTC().Format(time.RFC3339),
	}
}

func (s *Server) mergePRRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, pr *github.PullRequest, member, triggerLabel string) {
	var sb *unstructured.Unstructured
	prStr := strconv.Itoa(pr.GetNumber())
	for _, candidate := range sandboxes {
		if candidate.GetLabels()["factory.gemini.google.com/pr"] == prStr {
			sb = candidate
			break
		}
	}

	reviewRequested := false
	for _, reviewer := range pr.RequestedReviewers {
		if strings.EqualFold(reviewer.GetLogin(), member) {
			reviewRequested = true
		}
	}
	authored := strings.EqualFold(pr.GetUser().GetLogin(), member)
	labeled := triggerLabel != "" && hasLabel(pr.Labels, triggerLabel)
	if sb == nil && !authored && !reviewRequested && !labeled {
		return
	}

	draft := ""
	reviewState := ""
	state := ""
	if sb != nil {
		annotations := sb.GetAnnotations()
		draft = annotations["agentDraft"]
		reviewState = annotations["reviewState"]
		state = annotations[annoTaskState]
	}

	stage, attention := "open", ""
	switch {
	case reviewState == "submitted":
		stage = "review-submitted"
	case draft != "":
		stage, attention = "review-ready", attentionNeedsYou
	case state == "Running":
		stage, attention = "reviewing", attentionWorking
	case labeled || reviewRequested:
		stage, attention = "review-queued", attentionWaiting
	case authored:
		stage, attention = "open", attentionWaiting
	}

	claimedBy := ""
	if reviewRequested {
		claimedBy = member
	}

	key := fmt.Sprintf("pr-%d", pr.GetNumber())
	items[key] = &models.WorkItem{
		Type:      "pr",
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		HTMLURL:   pr.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		ClaimedBy: claimedBy,
		PRURL:     pr.GetHTMLURL(),
		Sandbox:   workSandbox(sb),
		UpdatedAt: pr.GetUpdatedAt().UTC().Format(time.RFC3339),
	}
}

// kickoffFix handles the Fix click: best-effort GitHub-native claim
// (assignment) and trigger label, plus the authoritative mailbox request the
// controller consumes. Consent is the click — the session user is the
// executor.
func (s *Server) kickoffFix(c *gin.Context) {
	s.kickoff(c, "issue")
}

// kickoffReview handles the Review click: best-effort self-requested review
// and trigger label, plus the mailbox request.
func (s *Server) kickoffReview(c *gin.Context) {
	s.kickoff(c, "pr")
}

func (s *Server) kickoff(c *gin.Context, kind string) {
	log := klog.FromContext(c.Request.Context())
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	number, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid number"})
		return
	}
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}
	triggerLabel, _, _ := unstructured.NestedString(board.Object, "spec", "triggers", "label")

	// GitHub-native claim + optional label, best-effort under the clicker's
	// token: kickoff proceeds via the mailbox even when the token lacks
	// triage rights on the repo.
	if token, err := s.memberToken(ctx, namespace); err == nil {
		gh := githubClientForToken(ctx, token)
		if kind == "issue" {
			if _, _, err := gh.Issues.AddAssignees(ctx, owner, repo, number, []string{member}); err != nil {
				log.Info("best-effort assignment failed", "issue", number, "err", err)
			}
		} else {
			if _, _, err := gh.PullRequests.RequestReviewers(ctx, owner, repo, number, github.ReviewersRequest{Reviewers: []string{member}}); err != nil {
				log.Info("best-effort self review-request failed", "pr", number, "err", err)
			}
		}
		if triggerLabel != "" {
			if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{triggerLabel}); err != nil {
				log.Info("best-effort trigger label failed", "number", number, "err", err)
			}
		}
	} else {
		log.Info("member token unavailable; mailbox-only kickoff", "err", err)
	}

	// Authoritative mailbox request; the controller consumes and clears it
	// once the sandbox exists.
	reqKey := fmt.Sprintf("fix-%d", number)
	if kind == "pr" {
		reqKey = fmt.Sprintf("review-%d", number)
	}
	annotations := board.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	requests := map[string]string{}
	if raw := annotations[annoBoardRequests]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &requests)
	}
	requests[reqKey] = member
	b, _ := json.Marshal(requests)
	annotations[annoBoardRequests] = string(b)
	board.SetAnnotations(annotations)
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record request", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) rerunBoardIssue(c *gin.Context) { s.rerunBoardWork(c, "issues") }
func (s *Server) rerunBoardPR(c *gin.Context)    { s.rerunBoardWork(c, "prs") }

// rerunBoardWork marks a finished fix/review for re-run via the sandbox
// annotations the controller honors.
func (s *Server) rerunBoardWork(c *gin.Context, kind string) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	number := c.Param("id")
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}

	// Fix sandboxes live in the executor's namespace (session user for
	// their own reruns); review sandboxes live in the board namespace.
	var sandboxNS, sandboxName, annotation string
	switch kind {
	case "issues":
		annotation = annoRefixRequest
		name := fmt.Sprintf("fix-%s-%s", repo, number)
		for _, ns := range []string{namespace, board.GetNamespace()} {
			if sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo); err == nil {
				if _, ok := sandboxes[name]; ok {
					sandboxNS, sandboxName = ns, name
					break
				}
			}
		}
	case "prs":
		annotation = annoRereviewRequest
		for _, ns := range []string{board.GetNamespace(), namespace} {
			sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
			if err != nil {
				continue
			}
			for name, sb := range sandboxes {
				if sb.GetLabels()["factory.gemini.google.com/pr"] == number {
					sandboxNS, sandboxName = ns, name
					break
				}
			}
			if sandboxName != "" {
				break
			}
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be issues or prs"})
		return
	}
	if sandboxName == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no sandbox for this item yet"})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, sandboxNS, sandboxName, annotation, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to request re-run", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// createBoard creates a personal RepoBoard in the session namespace from a
// repo URL — boards are one-URL onboarding by design.
func (s *Server) createBoard(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	var payload struct {
		RepoURL string `json:"repoURL"`
		Name    string `json:"name"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.RepoURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repoURL is required"})
		return
	}
	_, repo, err := parseRepoURL(payload.RepoURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid repoURL", "details": err.Error()})
		return
	}
	name := payload.Name
	if name == "" {
		name = strings.ToLower(repo)
	}

	board := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "board.gemini.google.com/v1alpha1",
		"kind":       "RepoBoard",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"spec": map[string]interface{}{
			"repoURL": payload.RepoURL,
			"access":  map[string]interface{}{"mode": "list", "allow": []interface{}{sessionUser}},
		},
	}}
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).Create(ctx, board, v1.CreateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create board", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name})
}

func (s *Server) deleteBoard(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	if err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).Delete(ctx, c.Param("board"), v1.DeleteOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete board", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}
