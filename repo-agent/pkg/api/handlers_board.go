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
	annoReviewError     = "review.gemini.google.com/error"
	annoLastTaskType    = "sandbox.gemini.google.com/last-task-type"
	annoPlanDraft       = "board.gemini.google.com/plan"
	annoPlannedAt       = "board.gemini.google.com/planned-at"
	annoPlanFeedback    = "board.gemini.google.com/plan-feedback"
	annoPlanFeedbackAt  = "board.gemini.google.com/plan-feedback-at"
	annoPlanApproved    = "board.gemini.google.com/plan-approved-at"
	annoPlanRejected    = "board.gemini.google.com/plan-rejected-at"
	annoRefixRequest    = "review.gemini.google.com/refix-requested-at"
	annoReviewAbandoned = "review.gemini.google.com/abandoned-at"
	annoTriagePublished = "board.gemini.google.com/triage-published-at"
)

// nowRFC3339 timestamps re-run request annotations.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// reviewRequestFreshWindow bounds how long a bare review request counts as
// needs-you; older requests stay listed but out of UP NEXT.
const reviewRequestFreshWindow = 14 * 24 * time.Hour

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

// pendingReviewCache caches "does the viewer have a pending review parked
// on that PR". GitHub is the only storage for pending reviews, so the board
// rediscovers them even when no sandbox breadcrumb survives (restart,
// cleanup). Short TTL: a finalize/discard on GitHub reflects within a
// minute.
var pendingReviewCache = struct {
	sync.Mutex
	entries map[string]pendingReviewEntry
}{entries: map[string]pendingReviewEntry{}}

type pendingReviewEntry struct {
	pending bool
	expires time.Time
}

// viewerHasPendingReview runs under the viewer's own token — GitHub shows
// pending reviews only to their author. Errors are not definitive: render
// without the pending state rather than caching a wrong verdict.
func (s *Server) viewerHasPendingReview(ctx context.Context, gh *github.Client, owner, repo string, pr int, member string) bool {
	key := fmt.Sprintf("%s|%s/%s#%d", member, owner, repo, pr)
	pendingReviewCache.Lock()
	if e, ok := pendingReviewCache.entries[key]; ok && time.Now().Before(e.expires) {
		pendingReviewCache.Unlock()
		return e.pending
	}
	pendingReviewCache.Unlock()

	reviews, _, err := gh.PullRequests.ListReviews(ctx, owner, repo, pr, &github.ListOptions{PerPage: 100})
	if err != nil {
		return false
	}
	pending := false
	for _, rv := range reviews {
		if strings.EqualFold(rv.GetUser().GetLogin(), member) && rv.GetState() == "PENDING" {
			pending = true
			break
		}
	}
	pendingReviewCache.Lock()
	pendingReviewCache.entries[key] = pendingReviewEntry{pending: pending, expires: time.Now().Add(time.Minute)}
	pendingReviewCache.Unlock()
	return pending
}

func (s *Server) hasPushPermission(ctx context.Context, namespace, sessionUser, repoURL string) bool {
	key := sessionUser + "|" + repoURL
	repoPermCache.Lock()
	if e, ok := repoPermCache.entries[key]; ok && time.Now().Before(e.expires) {
		repoPermCache.Unlock()
		return e.allowed
	}
	repoPermCache.Unlock()

	// Only a definitive GitHub answer is cached for the full TTL. A
	// transient failure (token fetch, network, rate limit) must not poison
	// the verdict: it would silently strip Fix/Promote/Publish from the UI
	// for 15 minutes. On error, keep any previous verdict and retry soon.
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return false
	}
	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		return s.stalePermOrFalse(key)
	}
	gh := githubClientForToken(ctx, token)
	repository, _, err := gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		klog.FromContext(ctx).Info("push-permission check failed; keeping previous verdict", "repo", repoURL, "err", err)
		return s.stalePermOrFalse(key)
	}
	perms := repository.GetPermissions()
	allowed := perms["push"] || perms["maintain"] || perms["admin"]
	repoPermCache.Lock()
	repoPermCache.entries[key] = repoPermEntry{allowed: allowed, expires: time.Now().Add(15 * time.Minute)}
	repoPermCache.Unlock()
	return allowed
}

// stalePermOrFalse returns the last cached verdict (even expired) when a
// fresh check could not be made, extending it briefly so the next request
// retries soon.
func (s *Server) stalePermOrFalse(key string) bool {
	repoPermCache.Lock()
	defer repoPermCache.Unlock()
	if e, ok := repoPermCache.entries[key]; ok {
		repoPermCache.entries[key] = repoPermEntry{allowed: e.allowed, expires: time.Now().Add(30 * time.Second)}
		return e.allowed
	}
	return false
}

// Boards are personal: each lives in its owner's namespace and is visible
// only to them. GitHub is the shared view.

func (s *Server) visibleBoards(ctx context.Context, namespace, sessionUser string) []unstructured.Unstructured {
	list, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return nil
	}
	return list.Items
}

func (s *Server) resolveBoard(ctx context.Context, namespace, sessionUser, name string) (*unstructured.Unstructured, string, error) {
	board, err := s.getBoard(ctx, namespace, name)
	if err != nil {
		return nil, "", fmt.Errorf("board %s not found: %w", name, err)
	}
	return board, sessionUser, nil
}

func (s *Server) getBoards(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	boards := []models.Board{}
	for _, item := range s.visibleBoards(c.Request.Context(), namespace, sessionUser) {
		repoURL, _, _ := unstructured.NestedString(item.Object, "spec", "repoURL")
		needsHuman, _, _ := unstructured.NestedInt64(item.Object, "status", "counts", "needsHuman")
		active, _, _ := unstructured.NestedInt64(item.Object, "status", "counts", "active")
		role := "read-only"
		if s.hasPushPermission(c.Request.Context(), namespace, sessionUser, repoURL) {
			role = "maintainer"
		}
		boards = append(boards, models.Board{
			Name:       item.GetName(),
			Namespace:  item.GetNamespace(),
			RepoURL:    repoURL,
			NeedsHuman: int(needsHuman),
			Active:     int(active),
			Role:       role,
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
	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub token unavailable", "details": err.Error()})
		return
	}
	gh := githubClientForToken(ctx, token)
	// Maintainers see the repo's whole review queue: for push+ viewers
	// every open PR lists (view only — nothing runs without a click or a
	// standing opt-in). Others see involvement only.
	maintainer := s.hasPushPermission(ctx, namespace, sessionUser, repoURL)

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

	// One issues surface: assigned to you, filed by you, and the unclaimed
	// repo-wide remainder all land in the single "issues" group — ownership
	// shows on the row (Claimed by, labels), actions follow row state.
	collect := func(issues []*github.Issue) {
		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			s.mergeIssueRow(items, sandboxes, issue, repo, member)
		}
	}
	assigned, err := listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Assignee: member})
	if err != nil {
		log.Info("failed to list assigned issues", "err", err)
	}
	created, err := listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Creator: member})
	if err != nil {
		log.Info("failed to list created issues", "err", err)
	}
	// Load claimed executors' namespaces before merging rows so their
	// sandboxes surface on the shared board.
	for _, issue := range assigned {
		for _, a := range issue.Assignees {
			loadSandboxNamespace(strings.ToLower(a.GetLogin()))
		}
	}
	collect(assigned)
	collect(created)

	// Repo-wide triage inbox: listing is free (one issues call), so it is
	// always shown — the agent only RUNS on a member's Triage click or the
	// board's auto-triage intake. Excluded and trigger-labeled issues are
	// out (the latter route to fix); rows already claimed keep their group.
	{
		excludeLabels, _, _ := unstructured.NestedStringSlice(board.Object, "spec", "intake", "filters", "excludeLabels")
		all, err := listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open"})
		if err != nil {
			log.Info("failed to list issues for triage", "err", err)
		}
		for _, issue := range all {
			if issue.IsPullRequest() {
				continue
			}
			// Assigned to anyone = owned, not awaiting triage.
			if len(issue.Assignees) > 0 {
				continue
			}
			if hasAnyLabel(issue.Labels, excludeLabels) {
				continue
			}
			s.mergeIssueRow(items, sandboxes, issue, repo, member)
		}
	}

	// PRs: authored by / review-requested to the member, trigger-labeled, or
	// with an existing factory sandbox.
	prs, _, err := gh.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		log.Info("failed to list PRs", "err", err)
	}
	for _, pr := range prs {
		// Every attributed review self-requests the member's review at
		// kickoff, so requested-reviewer rows are where parked pending
		// reviews can hide.
		pendingOnGitHub := false
		for _, reviewer := range pr.RequestedReviewers {
			if strings.EqualFold(reviewer.GetLogin(), member) {
				pendingOnGitHub = s.viewerHasPendingReview(ctx, gh, owner, repo, pr.GetNumber(), member)
				break
			}
		}
		s.mergePRRow(items, sandboxes, pr, member, maintainer, pendingOnGitHub)
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
				s.mergePRRow(items, sandboxes, pr, member, true, false)
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

	// A mailbox entry is a click the controller hasn't materialized yet
	// (launch window is up to a reconcile): render those items as
	// starting so the member sees immediate feedback and no second
	// kickoff is invited.
	if raw := board.GetAnnotations()[annoBoardRequests]; raw != "" {
		requests := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &requests); err == nil {
			preRunPR := map[string]bool{"open": true, "review-requested": true, "review-submitted": true}
			preRunIssue := map[string]bool{"open": true, "untriaged": true, "triage-ready": true, "triaged": true}
			for key := range requests {
				if n, ok := strings.CutPrefix(key, "review-"); ok {
					if item, found := items["pr-"+n]; found && preRunPR[item.Stage] {
						item.Stage, item.Attention = "review-starting", attentionWorking
					}
				}
				if n, ok := strings.CutPrefix(key, "fix-"); ok {
					if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
						item.Stage, item.Attention = "fix-starting", attentionWorking
					}
				}
				if n, ok := strings.CutPrefix(key, "triage-"); ok {
					if item, found := items["issue-"+n]; found && (item.Stage == "untriaged" || item.Stage == "open") {
						item.Stage, item.Attention = "triaging", attentionWorking
					}
				}
				if n, ok := strings.CutPrefix(key, "plan-"); ok {
					if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
						item.Stage, item.Attention = "planning", attentionWorking
					}
				}
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

func hasAnyLabel(labels []*github.Label, names []string) bool {
	for _, name := range names {
		if hasLabel(labels, name) {
			return true
		}
	}
	return false
}

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, issue *github.Issue, repo, member string) {
	key := fmt.Sprintf("issue-%d", issue.GetNumber())
	if _, ok := items[key]; ok {
		return
	}

	// All assignees, viewer first; the UI shows the first two plus a count.
	var assignees []string
	for _, a := range issue.Assignees {
		if strings.EqualFold(a.GetLogin(), member) {
			assignees = append([]string{a.GetLogin()}, assignees...)
		} else {
			assignees = append(assignees, a.GetLogin())
		}
	}
	claimedBy := strings.Join(assignees, ", ")
	if len(assignees) > 2 {
		claimedBy = strings.Join(assignees[:2], ", ") + fmt.Sprintf(" +%d", len(assignees)-2)
	}

	sb := sandboxes[fmt.Sprintf("fix-%s-%d", repo, issue.GetNumber())]
	state := ""
	prURL := ""
	taskType := ""
	planDraft := ""
	planApproved := false
	planRevising := false
	if sb != nil {
		annotations := sb.GetAnnotations()
		state = annotations[annoTaskState]
		taskType = annotations[annoLastTaskType]
		planDraft = annotations[annoPlanDraft]
		planApproved = annotations[annoPlanApproved] != ""
		// Feedback newer than the stored plan means a refinement round is
		// queued or running: agent motion, not the member's move.
		if fb, err := time.Parse(time.RFC3339, annotations[annoPlanFeedbackAt]); err == nil {
			planned, err := time.Parse(time.RFC3339, annotations[annoPlannedAt])
			planRevising = err != nil || fb.After(planned)
		}
		if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
			prURL = u
		}
	}
	triageDraft := ""
	triageState := ""
	triagePublished := false
	triageSB := sandboxes[fmt.Sprintf("triage-%s-%d", repo, issue.GetNumber())]
	if triageSB != nil {
		triageDraft = triageSB.GetAnnotations()["agentDraft"]
		triageState = triageSB.GetAnnotations()[annoTaskState]
		triagePublished = triageSB.GetAnnotations()[annoTriagePublished] != ""
	}

	stage, attention := "open", ""
	switch {
	case state == "Running" && taskType == "plan":
		stage, attention = "planning", attentionWorking
	case state == "Running":
		stage, attention = "fixing", attentionWorking
	case state == "Failed" && taskType == "plan":
		stage, attention = "plan-failed", attentionNeedsYou
	case state == "Failed":
		stage, attention = "fix-failed", attentionNeedsYou
	case state == "Completed" && prURL != "":
		stage, attention = "pr-open", attentionNeedsYou
	case taskType == "plan" && planRevising:
		// Refinement queued: the relaunch window before Running stamps.
		stage, attention = "planning", attentionWorking
	case taskType == "plan" && planDraft != "" && !planApproved:
		stage, attention = "plan-ready", attentionNeedsYou
	case taskType == "plan" && planApproved:
		// Approved: the fix mailbox request is in flight.
		stage, attention = "fix-starting", attentionWorking
	case state == "Completed" && taskType != "plan":
		stage, attention = "fix-done", attentionNeedsYou
	case triageDraft != "" && triagePublished:
		stage = "triaged"
	case triageDraft != "":
		stage, attention = "triage-ready", attentionNeedsYou
	case triageState == "Running":
		stage, attention = "triaging", attentionWorking
	case triageSB != nil && triageDraft == "":
		// Triage sandbox provisioning (no task state yet).
		stage, attention = "triaging", attentionWorking
	case claimedBy == "":
		// Unclaimed and untouched: the triage inbox state.
		stage = "untriaged"
	}
	if sb == nil && triageSB != nil && stage == "triaging" {
		// Surface the triage sandbox on rows without a fix sandbox.
		sb = triageSB
	}

	var labels []string
	for _, l := range issue.Labels {
		labels = append(labels, l.GetName())
	}
	items[key] = &models.WorkItem{
		Type:      "issue",
		Group:     "issues",
		Number:    issue.GetNumber(),
		Title:     issue.GetTitle(),
		HTMLURL:   issue.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		Assignee:  claimedBy,
		PRURL:     prURL,
		Labels:    labels,
		Draft:     triageDraft,
		Plan:      planDraft,
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
// friendlyReviewError rewrites known agent-run failures into an
// instruction the member can act on; anything unrecognized passes through
// verbatim. Empty stays empty — old sandboxes predate the error annotation.
func friendlyReviewError(msg string) string {
	switch {
	case msg == "":
		return ""
	case strings.Contains(msg, "OAuth App access restrictions"):
		return "This organization blocks OAuth app tokens. Add a personal access token (manual_pat key in your github-pat secret), then click Review again. Agent said: " + msg
	case strings.Contains(msg, "Bad credentials"):
		return "GitHub rejected your token — sign in again or add a personal access token (manual_pat), then click Review again. Agent said: " + msg
	default:
		return msg
	}
}

func (s *Server) mergePRRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, pr *github.PullRequest, member string, force, pendingOnGitHub bool) {
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
	// The trigger label is automation-only: it never selects rows for the
	// view. PRs appear through involvement (authored / review-requested),
	// an existing sandbox, or an issue-fix link.
	if sb == nil && !authored && !reviewRequested && !force {
		return
	}

	reviewState := ""
	state := ""
	reviewError := ""
	restarting := false
	if sb != nil {
		annotations := sb.GetAnnotations()
		reviewState = annotations["reviewState"]
		state = annotations[annoTaskState]
		reviewError = annotations[annoReviewError]
		// A re-review marker newer than the last task activity means a
		// relaunch is waking the sandbox: stale Failed/Completed stamps
		// must render as starting, not as the old outcome.
		if markerAt, err := time.Parse(time.RFC3339, annotations[annoRereviewRequest]); err == nil {
			lastActivity := time.Time{}
			for _, key := range []string{"sandbox.gemini.google.com/last-task-time", annoCompletionTime} {
				if t, err := time.Parse(time.RFC3339, annotations[key]); err == nil && t.After(lastActivity) {
					lastActivity = t
				}
			}
			restarting = markerAt.After(lastActivity)
		}
	}

	stage, attention := "open", ""
	switch {
	case state == "Running":
		// An active run always wins — stale reviewState from a previous
		// cycle must not mask a re-review in flight.
		stage, attention = "reviewing", attentionWorking
	case restarting:
		stage, attention = "review-starting", attentionWorking
	case sb != nil && state == "" && reviewState == "":
		// Sandbox exists but the task hasn't stamped a state yet:
		// provisioning (pod scheduling, image pull, clone). Not the
		// member's move.
		stage, attention = "review-starting", attentionWorking
	case state == "Failed" && reviewState == "":
		// The run died without posting anything — surface it instead of
		// falling back to the pre-click stage.
		stage, attention = "review-failed", attentionNeedsYou
	case reviewState == "submitted" && !reviewRequested:
		stage = "review-submitted"
	// A review request on an already-submitted row is GitHub's native
	// "please review again" (submitting clears you from
	// requested_reviewers; a re-request re-adds you) — fall through to the
	// review-requested handling below.
	case reviewState == "pending" || pendingOnGitHub:
		// The agent posted a pending review under the member's identity;
		// GitHub is where they finalize it. pendingOnGitHub is the
		// rediscovered form: GitHub said so directly, no sandbox needed.
		stage, attention = "review-pending", attentionNeedsYou
	case reviewRequested:
		// A bare GitHub review request: nothing is queued, a human is
		// waiting on the member. Fresh requests demand attention; fossils
		// stay out of UP NEXT.
		stage, attention = "review-requested", attentionWaiting
		if time.Since(pr.GetUpdatedAt()) <= reviewRequestFreshWindow {
			attention = attentionNeedsYou
		}
	case authored:
		stage, attention = "open", attentionWaiting
	}

	group := "review"
	if authored {
		group = "mine-pr"
	}

	key := fmt.Sprintf("pr-%d", pr.GetNumber())
	itemError := ""
	if stage == "review-failed" {
		itemError = friendlyReviewError(reviewError)
	}
	items[key] = &models.WorkItem{
		Type:      "pr",
		Group:     group,
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		HTMLURL:   pr.GetHTMLURL(),
		Stage:     stage,
		Attention: attention,
		Error:     itemError,
		PRURL:     pr.GetHTMLURL(),
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

// kickoffTriage handles the Triage click: mailbox only — triage is
// draft-only, so there is no GitHub-side claim to make.
// kickoffPlan handles the Plan click: mailbox only — planning is
// draft-only (nothing written to GitHub) and claims happen at fix time.
func (s *Server) kickoffPlan(c *gin.Context) {
	s.kickoff(c, "plan")
}

func (s *Server) kickoffTriage(c *gin.Context) {
	s.kickoff(c, "triage")
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
	// GitHub-native claim, best-effort under the clicker's token: kickoff
	// proceeds via the mailbox even when the token lacks triage rights.
	// The trigger label is deliberately NOT written: a click is a one-time
	// consent, while a label is a standing trigger that would relaunch the
	// item forever after cleanup.
	if token, err := s.memberToken(ctx, namespace); err == nil && kind != "triage" && kind != "plan" {
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
	} else {
		log.Info("member token unavailable; mailbox-only kickoff", "err", err)
	}

	// A fresh review consent also stamps the re-review marker on any
	// existing sandbox for this PR, so a previously finished (or
	// abandoned) review relaunches instead of staying terminal.
	if kind == "pr" {
		prStr := strconv.Itoa(number)
		for _, ns := range []string{namespace, board.GetNamespace()} {
			sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
			if err != nil {
				continue
			}
			for _, sb := range sandboxes {
				if sb.GetLabels()[labelFactoryPR] == prStr {
					_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoRereviewRequest, nowRFC3339())
				}
			}
		}
	}

	// Authoritative mailbox request; the controller consumes and clears it
	// once the sandbox exists.
	reqKey := fmt.Sprintf("fix-%d", number)
	switch kind {
	case "pr":
		reqKey = fmt.Sprintf("review-%d", number)
	case "triage":
		reqKey = fmt.Sprintf("triage-%d", number)
	case "plan":
		reqKey = fmt.Sprintf("plan-%d", number)
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

// rerunBoardIssue marks a finished fix for re-run via the sandbox
// annotation the controller honors. (Reviews have no rerun endpoint: the
// Review kickoff stamps the re-review marker itself.)
func (s *Server) rerunBoardIssue(c *gin.Context) {
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
	// their own reruns).
	var sandboxNS, sandboxName string
	annotation := annoRefixRequest
	name := fmt.Sprintf("fix-%s-%s", repo, number)
	for _, ns := range []string{namespace, board.GetNamespace()} {
		if sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo); err == nil {
			if _, ok := sandboxes[name]; ok {
				sandboxNS, sandboxName = ns, name
				break
			}
		}
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
	autoReview := false
	if cm, err := s.K8sManager.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, repoboard.PreferencesConfigMap, v1.GetOptions{}); err == nil {
		autoFix = cm.Data[repoboard.AutoFixPreferenceKey(board.GetNamespace(), board.GetName())] == "true"
		autoReview = cm.Data[repoboard.AutoReviewPreferenceKey(board.GetNamespace(), board.GetName())] == "true"
	}
	c.JSON(http.StatusOK, gin.H{"autoFix": autoFix, "autoReview": autoReview})
}

func (s *Server) putBoardSettings(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	var payload struct {
		AutoFix    bool `json:"autoFix"`
		AutoReview bool `json:"autoReview"`
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

	prefs := map[string]bool{
		repoboard.AutoFixPreferenceKey(board.GetNamespace(), board.GetName()):    payload.AutoFix,
		repoboard.AutoReviewPreferenceKey(board.GetNamespace(), board.GetName()): payload.AutoReview,
	}
	apply := func(data map[string]string) {
		for key, on := range prefs {
			if on {
				data[key] = "true"
			} else {
				delete(data, key)
			}
		}
	}
	cms := s.K8sManager.Clientset.CoreV1().ConfigMaps(namespace)
	cm, err := cms.Get(ctx, repoboard.PreferencesConfigMap, v1.GetOptions{})
	if err != nil {
		cm = &corev1.ConfigMap{ObjectMeta: v1.ObjectMeta{Name: repoboard.PreferencesConfigMap, Namespace: namespace}, Data: map[string]string{}}
		apply(cm.Data)
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
	apply(cm.Data)
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

// abandonBoardReview deletes the clicker's pending review on GitHub (only
// one pending review may exist per user, so this is how a bad agent review
// is discarded) and clears the board's memory of the run.
func (s *Server) abandonBoardReview(c *gin.Context) {
	ctx, board, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}

	gh := githubClientForToken(ctx, token)
	reviews, _, err := gh.PullRequests.ListReviews(ctx, owner, repo, number, &github.ListOptions{PerPage: 100})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list reviews", "details": err.Error()})
		return
	}
	deleted := false
	for _, review := range reviews {
		// PENDING reviews are only visible to their author — any returned
		// here belongs to the clicker.
		if strings.EqualFold(review.GetState(), "PENDING") {
			if _, _, err := gh.PullRequests.DeletePendingReview(ctx, owner, repo, number, review.GetID()); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete pending review", "details": err.Error()})
				return
			}
			deleted = true
		}
	}

	// The rediscovery cache must not keep announcing the deleted review.
	suffix := fmt.Sprintf("|%s/%s#%d", owner, repo, number)
	pendingReviewCache.Lock()
	for key := range pendingReviewCache.entries {
		if strings.HasSuffix(key, suffix) {
			delete(pendingReviewCache.entries, key)
		}
	}
	pendingReviewCache.Unlock()

	// Clear the sandbox's review state so the row returns to its plain
	// stage; the abandoned-at marker stops the controller from re-marking
	// the stale invocation result as pending.
	prStr := strconv.Itoa(number)
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		for _, sb := range sandboxes {
			if sb.GetLabels()[labelFactoryPR] != prStr {
				continue
			}
			_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), "reviewState", "")
			_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoReviewAbandoned, nowRFC3339())
			_ = s.K8sManager.ScaledownSandboxByName(ctx, ns, sb.GetName())
		}
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
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

// boardSpecView is the editable subset of a RepoBoard spec exposed to the
// gear panel. Access/prepIdentity/sandbox stay kubectl-only (owner-level
// governance and operator concerns).
type boardSpecView struct {
	Editable         bool     `json:"editable"`
	TriggerLabel     string   `json:"triggerLabel"`
	TriageIssues     bool     `json:"triageIssues"`
	DraftReviews     bool     `json:"draftReviews"`
	ExcludeLabels    []string `json:"excludeLabels"`
	MaxActive        int64    `json:"maxActive"`
	MaxActivePerUser int64    `json:"maxActivePerUser"`
	AutoIterate      bool     `json:"autoIterate"`
	DraftPR          bool     `json:"draftPR"`
	Disclose         bool     `json:"disclose"`
}

func (s *Server) getBoardSpec(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	view := boardSpecView{Editable: true}
	view.TriggerLabel, _, _ = unstructured.NestedString(board.Object, "spec", "triggers", "label")
	view.TriageIssues, _, _ = unstructured.NestedBool(board.Object, "spec", "intake", "triageIssues")
	view.DraftReviews, _, _ = unstructured.NestedBool(board.Object, "spec", "intake", "draftReviews")
	view.ExcludeLabels, _, _ = unstructured.NestedStringSlice(board.Object, "spec", "intake", "filters", "excludeLabels")
	view.MaxActive, _, _ = unstructured.NestedInt64(board.Object, "spec", "limits", "maxActive")
	view.MaxActivePerUser, _, _ = unstructured.NestedInt64(board.Object, "spec", "limits", "maxActivePerUser")
	view.AutoIterate, _, _ = unstructured.NestedBool(board.Object, "spec", "policy", "autoIterate")
	view.DraftPR, _, _ = unstructured.NestedBool(board.Object, "spec", "policy", "draftPR")
	view.Disclose, _, _ = unstructured.NestedBool(board.Object, "spec", "policy", "disclose")
	c.JSON(http.StatusOK, view)
}

// putBoardSpec updates the editable spec subset. Board governance stays
// with its owner: only boards in the session's own namespace are writable.
func (s *Server) putBoardSpec(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	var payload boardSpecView
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	set := func(value interface{}, fields ...string) bool {
		if err := unstructured.SetNestedField(board.Object, value, fields...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set field", "details": err.Error()})
			return false
		}
		return true
	}
	labels := make([]interface{}, 0, len(payload.ExcludeLabels))
	for _, l := range payload.ExcludeLabels {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	ok := set(payload.TriggerLabel, "spec", "triggers", "label") &&
		set(payload.TriageIssues, "spec", "intake", "triageIssues") &&
		set(payload.DraftReviews, "spec", "intake", "draftReviews") &&
		set(labels, "spec", "intake", "filters", "excludeLabels") &&
		set(payload.MaxActive, "spec", "limits", "maxActive") &&
		set(payload.MaxActivePerUser, "spec", "limits", "maxActivePerUser") &&
		set(payload.AutoIterate, "spec", "policy", "autoIterate") &&
		set(payload.DraftPR, "spec", "policy", "draftPR") &&
		set(payload.Disclose, "spec", "policy", "disclose")
	if !ok {
		return
	}
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save board settings", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// findPlanSandbox locates the issue's fix sandbox carrying a plan draft,
// checking the viewer's namespace then the board's.
func (s *Server) findPlanSandbox(c *gin.Context, board *unstructured.Unstructured, owner, repo string, number int) (*unstructured.Unstructured, string) {
	ctx := c.Request.Context()
	name := fmt.Sprintf("fix-%s-%d", repo, number)
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		if sb, found := sandboxes[name]; found && sb.GetAnnotations()[annoPlanDraft] != "" {
			return sb, ns
		}
	}
	return nil, ""
}

// planBoardFeedback records the member's refinement feedback on the plan
// sandbox; the controller re-runs the planner against the previous plan.
func (s *Server) planBoardFeedback(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var req struct {
		Feedback string `json:"feedback"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Feedback) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "feedback text is required"})
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to refine"})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlanFeedback, strings.TrimSpace(req.Feedback)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record feedback", "details": err.Error()})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlanFeedbackAt, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record feedback", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// planBoardApprove approves the plan and launches the fix: the fix runs
// --with-plan, so the approved plan ships in the PR description (its
// durable record). Approval is the consent for both.
func (s *Server) planBoardApprove(c *gin.Context) {
	_, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to approve"})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(c.Request.Context(), ns, sb.GetName(), annoPlanApproved, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to approve plan", "details": err.Error()})
		return
	}
	// The fix kickoff does the rest: GitHub claim (assignment) + mailbox.
	s.kickoff(c, "issue")
}

// planBoardReject discards the draft: plan annotations are cleared and the
// reject stamp stops the controller from resurrecting the old result.
func (s *Server) planBoardReject(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to reject"})
		return
	}
	for _, key := range []string{annoPlanDraft, annoPlannedAt, annoPlanFeedback, annoPlanFeedbackAt, annoPlanApproved} {
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), key, ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear plan", "details": err.Error()})
			return
		}
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlanRejected, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reject plan", "details": err.Error()})
		return
	}
	_ = s.K8sManager.ScaledownSandboxByName(ctx, ns, sb.GetName())
	c.Status(http.StatusOK)
}

// putBoardTriageDraft saves a member-edited triage suggestion back onto
// the draft sandbox. The edit is validated against the same schema publish
// consumes, so a save that publish could not act on is rejected up front.
func (s *Server) putBoardTriageDraft(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}

	var req struct {
		Draft string `json:"draft"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	if err := validateTriageDraft(req.Draft); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "draft does not match the triage schema", "details": err.Error()})
		return
	}

	name := fmt.Sprintf("triage-%s-%d", repo, number)
	for _, ns := range []string{board.GetNamespace(), s.Auth.GetNamespaceFromContext(c)} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		if sb, found := sandboxes[name]; found && sb.GetAnnotations()["agentDraft"] != "" {
			if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, "agentDraft", strings.TrimSpace(req.Draft)+"\n"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save draft", "details": err.Error()})
				return
			}
			c.Status(http.StatusOK)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "no triage suggestion to edit"})
}

// validateTriageDraft enforces the schema publish consumes: well-formed
// YAML, only known fields, and at least one actionable suggestion.
func validateTriageDraft(draft string) error {
	if strings.TrimSpace(draft) == "" {
		return fmt.Errorf("draft is empty")
	}
	dec := yamlv3.NewDecoder(strings.NewReader(draft))
	dec.KnownFields(true)
	suggestion := &triageSuggestion{}
	if err := dec.Decode(suggestion); err != nil {
		return err
	}
	t := suggestion.Triage
	if len(t.Labels) == 0 && t.Priority == "" && len(t.Duplicates) == 0 && t.Assessment == "" {
		return fmt.Errorf("nothing to publish: set at least one of triage.labels, triage.priority, triage.duplicates, triage.assessment")
	}
	return nil
}

// triageSuggestion mirrors the YAML factory's triage task emits.
type triageSuggestion struct {
	Triage struct {
		Labels     []string `yaml:"labels"`
		Priority   string   `yaml:"priority"`
		Duplicates []string `yaml:"duplicates"`
		Assessment string   `yaml:"assessment"`
	} `yaml:"triage"`
}

// publishBoardTriage applies a stored triage suggestion to the issue under
// the clicker's token: labels plus an assessment comment. The suggestion
// stays viewable; the row settles to the quiet triaged stage.
func (s *Server) publishBoardTriage(c *gin.Context) {
	ctx, board, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}

	var draftSB *unstructured.Unstructured
	name := fmt.Sprintf("triage-%s-%d", repo, number)
	for _, ns := range []string{board.GetNamespace(), s.Auth.GetNamespaceFromContext(c)} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		if sb, found := sandboxes[name]; found && sb.GetAnnotations()["agentDraft"] != "" {
			draftSB = sb
			break
		}
	}
	if draftSB == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no triage suggestion to publish"})
		return
	}

	suggestion := &triageSuggestion{}
	if err := yamlv3.Unmarshal([]byte(draftSB.GetAnnotations()["agentDraft"]), suggestion); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "triage suggestion is not parseable", "details": err.Error()})
		return
	}

	gh := githubClientForToken(ctx, token)
	if len(suggestion.Triage.Labels) > 0 {
		if _, _, err := gh.Issues.AddLabelsToIssue(ctx, owner, repo, number, suggestion.Triage.Labels); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply labels", "details": err.Error()})
			return
		}
	}
	if suggestion.Triage.Assessment != "" {
		body := "**Triage:** " + suggestion.Triage.Assessment
		if suggestion.Triage.Priority != "" {
			body += "\n\nSuggested priority: " + suggestion.Triage.Priority
		}
		if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{Body: &body}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to post triage comment", "details": err.Error()})
			return
		}
	}

	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, draftSB.GetNamespace(), draftSB.GetName(), annoTriagePublished, nowRFC3339()); err != nil {
		klog.FromContext(ctx).Info("failed to mark triage published", "issue", number, "err", err)
	}
	_ = s.K8sManager.ScaledownSandboxByName(ctx, draftSB.GetNamespace(), draftSB.GetName())
	c.Status(http.StatusOK)
}
