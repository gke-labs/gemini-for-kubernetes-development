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
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	yamlv3 "go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/controllers/repoboard"
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

// Factory CLI wire contract: sandbox labels and annotations the board reads
// to resolve work state and request re-runs.
const (
	labelFactoryPR      = "factory.gemini.google.com/pr"
	labelFactoryManaged = "factory.gemini.google.com/managed"
	labelRepoWatch      = "review.gemini.google.com/repowatch"
	annoTaskState       = "sandbox.gemini.google.com/last-task-state"
	annoCompletionTime  = "sandbox.gemini.google.com/completion-time"
	annoRereviewRequest = "review.gemini.google.com/rereview-requested-at"
	annoRefixRequest    = "review.gemini.google.com/refix-requested-at"
)

// nowRFC3339 timestamps re-run request annotations.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

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

	// Incoming issues (fix/triage): assigned to the member, plus
	// trigger-labeled ones. Issues the member filed land in "mine-issue"
	// unless they already qualify as incoming work.
	collect := func(issues []*github.Issue, group string) {
		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			s.mergeIssueRow(items, sandboxes, issue, repo, member, triggerLabel, group)
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
	created, err := listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Creator: member})
	if err != nil {
		log.Info("failed to list created issues", "err", err)
	}
	// Load claimed executors' namespaces before merging rows so their
	// sandboxes surface on the shared board.
	for _, issue := range append(append([]*github.Issue{}, assigned...), labeled...) {
		for _, a := range issue.Assignees {
			loadSandboxNamespace(strings.ToLower(a.GetLogin()))
		}
	}
	collect(assigned, "fix")
	collect(labeled, "fix")
	collect(created, "mine-issue")

	// PRs: authored by / review-requested to the member, trigger-labeled, or
	// with an existing factory sandbox.
	prs, _, err := gh.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		log.Info("failed to list PRs", "err", err)
	}
	for _, pr := range prs {
		s.mergePRRow(items, sandboxes, pr, member, triggerLabel, false)
	}

	// A PR that addresses an issue on this board is board work even when
	// nothing else selects it (e.g. a bot-authored fix): surface it for
	// review so it can swallow the issue row below.
	for _, pr := range prs {
		if _, ok := items[fmt.Sprintf("pr-%d", pr.GetNumber())]; ok {
			continue
		}
		for _, n := range closingRefs(pr.GetBody()) {
			if _, ok := items[fmt.Sprintf("issue-%d", n)]; ok {
				s.mergePRRow(items, sandboxes, pr, member, triggerLabel, true)
				break
			}
		}
	}

	// Fold issues into the PR that addresses them: once a fix PR exists the
	// PR row is the focus. Linkage comes from GitHub closing keywords in the
	// PR body and from the fix sandbox's recorded PR URL.
	for _, item := range items {
		if item.Type != "pr" {
			continue
		}
		for _, n := range item.Fixes {
			delete(items, fmt.Sprintf("issue-%d", n))
		}
	}
	for key, item := range items {
		if item.Type != "issue" || item.PRURL == "" {
			continue
		}
		if prNum := prNumberFromURL(item.PRURL); prNum > 0 {
			if prItem, ok := items[fmt.Sprintf("pr-%d", prNum)]; ok {
				prItem.Fixes = appendUnique(prItem.Fixes, item.Number)
				delete(items, key)
			}
		}
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

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, issue *github.Issue, repo, member, triggerLabel, group string) {
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
	triageDraft := ""
	if triageSB := sandboxes[fmt.Sprintf("triage-%s-%d", repo, issue.GetNumber())]; triageSB != nil {
		triageDraft = triageSB.GetAnnotations()["agentDraft"]
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
	case triageDraft != "":
		stage, attention = "triage-ready", attentionNeedsYou
	}

	items[key] = &models.WorkItem{
		Type:      "issue",
		Group:     group,
		Number:    issue.GetNumber(),
		Title:     issue.GetTitle(),
		HTMLURL:   issue.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		ClaimedBy: claimedBy,
		PRURL:     prURL,
		Draft:     triageDraft,
		Sandbox:   workSandbox(sb),
		UpdatedAt: issue.GetUpdatedAt().UTC().Format(time.RFC3339),
	}
}

// closingRefRe matches GitHub closing keywords ("Fixes #123") in PR bodies;
// used to fold issue rows into the PR that addresses them.
var closingRefRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[:\s]+#(\d+)`)

var prURLRe = regexp.MustCompile(`/pull/(\d+)`)

func prNumberFromURL(u string) int {
	if m := prURLRe.FindStringSubmatch(u); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

func appendUnique(nums []int, n int) []int {
	for _, v := range nums {
		if v == n {
			return nums
		}
	}
	return append(nums, n)
}

func closingRefs(body string) []int {
	var refs []int
	for _, m := range closingRefRe.FindAllStringSubmatch(body, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			refs = append(refs, n)
		}
	}
	return refs
}

// mergePRRow adds a PR row when it involves the member (authored,
// review-requested, trigger-labeled, or has a factory sandbox); force
// includes it regardless (used for PRs that address an issue on the board).
func (s *Server) mergePRRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, pr *github.PullRequest, member, triggerLabel string, force bool) {
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
	if sb == nil && !authored && !reviewRequested && !labeled && !force {
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
	rowDraft := ""
	if stage == "review-ready" {
		rowDraft = draft
	}

	group := "review"
	if authored {
		group = "mine-pr"
	}

	key := fmt.Sprintf("pr-%d", pr.GetNumber())
	items[key] = &models.WorkItem{
		Type:      "pr",
		Group:     group,
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		HTMLURL:   pr.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		ClaimedBy: claimedBy,
		PRURL:     pr.GetHTMLURL(),
		Draft:     rowDraft,
		DraftPR:   pr.GetDraft(),
		Fixes:     closingRefs(pr.GetBody()),
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

// Board settings: per-member opt-ins stored in the member's own namespace
// (design §4.2) — the target namespace comes from the session, never the
// request, so nobody can write another member's consent.

func (s *Server) getBoardSettings(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	autoFix := false
	if cm, err := s.K8sManager.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, repoboard.PreferencesConfigMap, v1.GetOptions{}); err == nil {
		autoFix = cm.Data[repoboard.AutoFixPreferenceKey(board.GetNamespace(), board.GetName())] == "true"
	}
	c.JSON(http.StatusOK, gin.H{"autoFix": autoFix})
}

func (s *Server) putBoardSettings(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	var payload struct {
		AutoFix bool `json:"autoFix"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	key := repoboard.AutoFixPreferenceKey(board.GetNamespace(), board.GetName())
	cms := s.K8sManager.Clientset.CoreV1().ConfigMaps(namespace)
	cm, err := cms.Get(ctx, repoboard.PreferencesConfigMap, v1.GetOptions{})
	if err != nil {
		cm = &corev1.ConfigMap{ObjectMeta: v1.ObjectMeta{Name: repoboard.PreferencesConfigMap, Namespace: namespace}, Data: map[string]string{}}
		if payload.AutoFix {
			cm.Data[key] = "true"
		}
		if _, err := cms.Create(ctx, cm, v1.CreateOptions{}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save settings", "details": err.Error()})
			return
		}
		c.Status(http.StatusOK)
		return
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if payload.AutoFix {
		cm.Data[key] = "true"
	} else {
		delete(cm.Data, key)
	}
	if _, err := cms.Update(ctx, cm, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save settings", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// Human-gated writes (design §6): publish a review draft, promote a draft
// PR, or merge — always under the acting member's own token, so GitHub
// re-enforces permissions and branch protection at the point of action.

func (s *Server) boardWriteContext(c *gin.Context) (context.Context, *unstructured.Unstructured, string, string, string, int, bool) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	number, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid number"})
		return ctx, nil, "", "", "", 0, false
	}
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return ctx, nil, "", "", "", 0, false
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return ctx, nil, "", "", "", 0, false
	}
	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub token unavailable", "details": err.Error()})
		return ctx, nil, "", "", "", 0, false
	}
	return ctx, board, owner, repo, token, number, true
}

// publishBoardReview posts the stored (or payload-overridden) review draft
// to GitHub as a pending review under the clicker's token.
func (s *Server) publishBoardReview(c *gin.Context) {
	ctx, board, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var payload struct {
		Review string `json:"review"`
	}
	_ = c.ShouldBindJSON(&payload)

	// Resolve the review sandbox holding the draft.
	var draftSB *unstructured.Unstructured
	prStr := strconv.Itoa(number)
	for _, ns := range []string{board.GetNamespace(), s.Auth.GetNamespaceFromContext(c)} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		for _, sb := range sandboxes {
			if sb.GetLabels()["factory.gemini.google.com/pr"] == prStr && sb.GetAnnotations()["agentDraft"] != "" {
				draftSB = sb
				break
			}
		}
		if draftSB != nil {
			break
		}
	}

	draft := payload.Review
	if draft == "" && draftSB != nil {
		draft = draftSB.GetAnnotations()["agentDraft"]
	}
	if draft == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no review draft to publish"})
		return
	}

	agentOutput := &models.ReviewAgentOutput{}
	reviewRequest := &github.PullRequestReviewRequest{}
	if err := yamlv3.Unmarshal([]byte(draft), agentOutput); err != nil || agentOutput.Review == nil {
		reviewRequest.Body = github.String(draft)
	} else {
		reviewRequest = agentOutput.Review.ToGitHubReviewRequest()
	}
	reviewRequest.Event = nil // pending (draft) review; the human finalizes on GitHub

	gh := githubClientForToken(ctx, token)
	if _, _, err := gh.PullRequests.CreateReview(ctx, owner, repo, number, reviewRequest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to publish review", "details": err.Error()})
		return
	}
	if draftSB != nil {
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, draftSB.GetNamespace(), draftSB.GetName(), "reviewState", "submitted"); err == nil {
			_ = s.K8sManager.ScaledownSandboxByName(ctx, draftSB.GetNamespace(), draftSB.GetName())
		}
	}
	c.Status(http.StatusOK)
}

// promoteBoardPR marks a draft PR ready for review (GraphQL — the REST API
// cannot un-draft) under the clicker's token.
func (s *Server) promoteBoardPR(c *gin.Context) {
	ctx, _, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	gh := githubClientForToken(ctx, token)
	pr, _, err := gh.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "PR not found", "details": err.Error()})
		return
	}
	if !pr.GetDraft() {
		c.Status(http.StatusOK)
		return
	}
	if err := markPRReadyForReview(ctx, token, pr.GetNodeID()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to promote draft PR", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// mergeBoardPR merges under the clicker's token; GitHub branch protection
// is the enforcement.
func (s *Server) mergeBoardPR(c *gin.Context) {
	ctx, _, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	gh := githubClientForToken(ctx, token)
	if _, _, err := gh.PullRequests.Merge(ctx, owner, repo, number, "", &github.PullRequestOptions{MergeMethod: "squash"}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Merge failed", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// markPRReadyForReview is injectable for tests.
var markPRReadyForReview = func(ctx context.Context, token, nodeID string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"query":     "mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }",
		"variables": map[string]string{"id": nodeID},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(respBody), `"errors"`) {
		return fmt.Errorf("graphql markPullRequestReadyForReview failed: %s", string(respBody))
	}
	return nil
}
