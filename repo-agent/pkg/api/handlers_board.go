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
	"bytes"
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
	"github.com/gregjones/httpcache"
	yamlv3 "go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
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

// ghConditionalCache backs conditional requests (ETags): GitHub answers
// unchanged resources with 304, which costs ZERO rate-limit quota — the
// difference between polling being a quota problem and being nearly free
// at steady state. Responses carry Vary: Authorization, so entries are
// keyed per token and never leak across members.
var ghConditionalCache = httpcache.NewMemoryCache()

// githubClientForToken is injectable for tests. The oauth2 transport runs
// inside the cache transport so the Authorization header is set before
// the conditional-request layer sees it.
var githubClientForToken = func(ctx context.Context, token string) *github.Client {
	// oauth2 OUTSIDE, cache INSIDE: the cache layer must see the
	// Authorization header for Vary: Authorization to partition entries
	// per member — critical here, where ListReviews responses carry
	// viewer-private pending reviews.
	cached := httpcache.NewTransport(ghConditionalCache)
	auth := &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		Base:   cached,
	}
	return github.NewClient(&http.Client{Transport: auth})
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
	// Secret Manager reference mode: the controller (the only component
	// with GSM access) resolves the reference and materializes the token
	// into factory-user — read it from there.
	if fu, ferr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{}); ferr == nil {
		if v, ok := fu.Data["GITHUB_TOKEN"]; ok && len(v) > 0 {
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
	pending  bool
	reviewed bool
	expires  time.Time
}

// viewerReviewStates runs under the viewer's own token — GitHub shows
// pending reviews only to their author. One ListReviews yields both
// verdicts: a parked pending review ("Pending on GitHub" with no sandbox
// breadcrumb) and a submitted one ("Reviewed ✓" that survives clean
// slates). Errors are not definitive: render without the states rather
// than caching a wrong verdict.
func (s *Server) viewerReviewStates(ctx context.Context, gh *github.Client, owner, repo string, pr int, member string) (pending, reviewed bool) {
	key := fmt.Sprintf("%s|%s/%s#%d", member, owner, repo, pr)
	pendingReviewCache.Lock()
	if e, ok := pendingReviewCache.entries[key]; ok && time.Now().Before(e.expires) {
		pendingReviewCache.Unlock()
		return e.pending, e.reviewed
	}
	pendingReviewCache.Unlock()

	reviews, _, err := gh.PullRequests.ListReviews(ctx, owner, repo, pr, &github.ListOptions{PerPage: 100})
	if err != nil {
		return false, false
	}
	for _, rv := range reviews {
		if !strings.EqualFold(rv.GetUser().GetLogin(), member) {
			continue
		}
		switch strings.ToUpper(rv.GetState()) {
		case "PENDING":
			pending = true
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			reviewed = true
		}
	}
	pendingReviewCache.Lock()
	pendingReviewCache.entries[key] = pendingReviewEntry{pending: pending, reviewed: reviewed, expires: time.Now().Add(time.Minute)}
	pendingReviewCache.Unlock()
	return pending, reviewed
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
		// The badge must agree with Up Next: when the feed cache holds
		// this board, count the same attention the tab opens onto. The
		// controller's status count is a sandbox-only heuristic from an
		// older era — it serves only as the cold-start fallback until the
		// first feed build lands.
		if items, ok := workFeedPeek(item.GetNamespace() + "/" + item.GetName()); ok {
			n := int64(0)
			for i := range items {
				if items[i].Attention == "needs-you" {
					n++
				}
			}
			needsHuman = n
		}
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

// workFeedCache makes the board fast and steady: building the feed costs
// dozens of serial GitHub calls on big repos (the 20s stutters), so the
// handler serves cached results instantly and refreshes in the
// background (stale-while-revalidate, singleflight). Mutating handlers
// invalidate so clicks reflect immediately.
var workFeedCache = struct {
	sync.Mutex
	entries    map[string]workFeedEntry
	refreshing map[string]bool
}{entries: map[string]workFeedEntry{}, refreshing: map[string]bool{}}

type workFeedEntry struct {
	items []models.WorkItem
	at    time.Time
}

const (
	workFeedFreshFor      = 15 * time.Second
	workFeedServeStaleFor = 3 * time.Minute
)

// workFeedGet returns cached items when usable; needsRefresh asks the
// caller to kick a background rebuild (claimed here, under the lock, so
// only one refresher runs per board).
func workFeedGet(key string) (items []models.WorkItem, ok, needsRefresh bool) {
	workFeedCache.Lock()
	defer workFeedCache.Unlock()
	e, found := workFeedCache.entries[key]
	if !found {
		return nil, false, false
	}
	age := time.Since(e.at)
	if age <= workFeedFreshFor {
		return e.items, true, false
	}
	if age <= workFeedServeStaleFor {
		refresh := !workFeedCache.refreshing[key]
		if refresh {
			workFeedCache.refreshing[key] = true
		}
		return e.items, true, refresh
	}
	return nil, false, false
}

// workFeedPeek returns cached items when present and unexpired, without
// claiming a refresh — badge counting must never trigger feed rebuilds.
func workFeedPeek(key string) ([]models.WorkItem, bool) {
	workFeedCache.Lock()
	defer workFeedCache.Unlock()
	e, found := workFeedCache.entries[key]
	if !found || time.Since(e.at) > workFeedServeStaleFor {
		return nil, false
	}
	return e.items, true
}

func workFeedPut(key string, items []models.WorkItem) {
	workFeedCache.Lock()
	workFeedCache.entries[key] = workFeedEntry{items: items, at: time.Now()}
	delete(workFeedCache.refreshing, key)
	workFeedCache.Unlock()
}

func invalidateWorkFeed(namespace, boardName string) {
	workFeedCache.Lock()
	delete(workFeedCache.entries, namespace+"/"+boardName)
	workFeedCache.Unlock()
}

func (s *Server) refreshWorkFeed(ctx context.Context, key string, board *unstructured.Unstructured, member, namespace string) {
	defer func() {
		workFeedCache.Lock()
		delete(workFeedCache.refreshing, key)
		workFeedCache.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	items, err := s.buildBoardWork(ctx, board, member, namespace)
	if err != nil {
		klog.FromContext(ctx).Info("background feed refresh failed", "board", key, "err", err)
		return
	}
	workFeedPut(key, items)
}

func (s *Server) getBoardWork(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)

	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	key := board.GetNamespace() + "/" + board.GetName()
	if items, ok, needsRefresh := workFeedGet(key); ok {
		if needsRefresh {
			// Detached: an aborted poll must not cancel the rebuild (the
			// suggestion-prefetch lesson).
			go s.refreshWorkFeed(context.WithoutCancel(ctx), key, board, member, namespace)
		}
		c.JSON(http.StatusOK, items)
		return
	}

	items, err := s.buildBoardWork(ctx, board, member, namespace)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build board feed", "details": err.Error()})
		return
	}
	workFeedPut(key, items)
	c.JSON(http.StatusOK, items)
}

// buildBoardWork assembles the feed universe. The independent GitHub
// listings run concurrently and are page-capped, newest first: the board
// is a work queue, not an archive — on huge repos the tail belongs on
// GitHub search, not in every 20-second poll.
func (s *Server) buildBoardWork(ctx context.Context, board *unstructured.Unstructured, member, namespace string) ([]models.WorkItem, error) {
	log := klog.FromContext(ctx)

	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid repoURL on board: %w", err)
	}
	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("github token unavailable: %w", err)
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

	// The board's persistent view filter (spec.view.labels) narrows the
	// universe server-side — VIEW ONLY, automation is scoped by spec.auto
	// alone. Items with an active sandbox always surface. Client-side
	// filters can only narrow further, never widen past this.
	viewLabels, _, _ := unstructured.NestedStringSlice(board.Object, "spec", "view", "labels")

	items := map[string]*models.WorkItem{}

	// One issues surface: assigned to you, filed by you, and the unclaimed
	// repo-wide remainder all land in the single "issues" group — ownership
	// shows on the row (Claimed by, labels), actions follow row state.
	collect := func(issues []*github.Issue) {
		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			s.mergeIssueRow(items, sandboxes, issue, repo, member, viewLabels, autoIterateDefault(board))
		}
	}
	var assigned, created, allIssues []*github.Issue
	var prs []*github.PullRequest
	var wg sync.WaitGroup
	concurrently := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	concurrently(func() {
		var err error
		if assigned, err = listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Assignee: member, Sort: "updated", Direction: "desc"}); err != nil {
			log.Info("failed to list assigned issues", "err", err)
		}
	})
	concurrently(func() {
		var err error
		if created, err = listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Creator: member, Sort: "updated", Direction: "desc"}); err != nil {
			log.Info("failed to list created issues", "err", err)
		}
	})
	concurrently(func() {
		var err error
		if allIssues, err = listIssues(ctx, gh, owner, repo, &github.IssueListByRepoOptions{State: "open", Sort: "updated", Direction: "desc"}); err != nil {
			log.Info("failed to list issues for triage", "err", err)
		}
	})
	concurrently(func() {
		var err error
		if prs, _, err = gh.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{State: "open", Sort: "updated", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}); err != nil {
			log.Info("failed to list PRs", "err", err)
		}
	})
	wg.Wait()
	// Load claimed executors' namespaces before merging rows so their
	// sandboxes surface on the shared board.
	for _, issue := range assigned {
		for _, a := range issue.Assignees {
			loadSandboxNamespace(strings.ToLower(a.GetLogin()))
		}
	}
	collect(assigned)
	collect(created)

	// Repo-wide triage inbox: the feed returns the full universe — what
	// the member LOOKS at is fluid client-side view state (scopes, label
	// filters), never server logic. Rows already claimed keep their group.
	for _, issue := range allIssues {
		if issue.IsPullRequest() {
			continue
		}
		// Assigned to anyone = owned, not awaiting triage.
		if len(issue.Assignees) > 0 {
			continue
		}
		s.mergeIssueRow(items, sandboxes, issue, repo, member, viewLabels, autoIterateDefault(board))
	}

	// Review-state rediscovery per non-authored PR, concurrently — GitHub
	// is the only durable record of the member's reviews, so both a
	// parked pending review and a submitted one must survive lost sandbox
	// breadcrumbs. (Requested-only gating could never see the submitted
	// case: submitting clears the reviewer request.) Cached 60s per PR,
	// ETag-backed underneath.
	type reviewStates struct{ pending, reviewed bool }
	statesByPR := map[int]reviewStates{}
	{
		var mu sync.Mutex
		var pwg sync.WaitGroup
		for _, pr := range prs {
			if strings.EqualFold(pr.GetUser().GetLogin(), member) {
				continue
			}
			pwg.Add(1)
			go func(num int) {
				defer pwg.Done()
				pending, reviewed := s.viewerReviewStates(ctx, gh, owner, repo, num, member)
				mu.Lock()
				statesByPR[num] = reviewStates{pending: pending, reviewed: reviewed}
				mu.Unlock()
			}(pr.GetNumber())
		}
		pwg.Wait()
	}
	for _, pr := range prs {
		st := statesByPR[pr.GetNumber()]
		s.mergePRRow(items, sandboxes, pr, repo, member, st.pending, st.reviewed, viewLabels, autoIterateDefault(board))
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
				s.mergePRRow(items, sandboxes, pr, repo, member, false, false, viewLabels, autoIterateDefault(board))
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
			// Clicks beyond the launch limits are honestly "queued", not
			// "starting": the controller defers them until a slot frees
			// (which includes the finished-but-idle hour today).
			running := 0
			for _, sb := range sandboxes {
				if replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas"); err == nil && found && replicas > 0 {
					running++
				}
			}
			atCapacity := int64(running) >= boardLimit(board, "maxActive", 5)
			preRunPR := map[string]bool{"open": true, "review-requested": true, "review-submitted": true}
			preRunIssue := map[string]bool{"open": true, "untriaged": true, "triage-ready": true, "triaged": true}
			mark := func(item *models.WorkItem, startingStage string) {
				// A sandbox-backed row is already launched (its stage came
				// from the sandbox, not this pre-run map); only truly
				// pre-sandbox clicks can be queued.
				if atCapacity && item.Sandbox == nil {
					item.Stage, item.Attention = "queued", attentionWaiting
					return
				}
				item.Stage, item.Attention = startingStage, attentionWorking
			}
			for key := range requests {
				if n, ok := strings.CutPrefix(key, "review-"); ok {
					if item, found := items["pr-"+n]; found && preRunPR[item.Stage] {
						mark(item, "review-starting")
					}
				}
				if n, ok := strings.CutPrefix(key, "fix-"); ok {
					if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
						mark(item, "fix-starting")
					}
				}
				if n, ok := strings.CutPrefix(key, "triage-"); ok {
					if item, found := items["issue-"+n]; found && (item.Stage == "untriaged" || item.Stage == "open") {
						// Triage is only board-capacity gated, not per-user.
						item.Stage, item.Attention = "triaging", attentionWorking
					}
				}
				for prefix, stageName := range map[string]string{"iterate-": "iterating", "address-": "addressing", "investigate-": "investigating"} {
					if n, ok := strings.CutPrefix(key, prefix); ok {
						if item, found := items["pr-"+n]; found {
							item.Stage, item.Attention = stageName, attentionWorking
						}
					}
				}
				if n, ok := strings.CutPrefix(key, "plan-"); ok {
					if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
						mark(item, "planning")
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
	// Within needs-you, finished agent work awaiting a verdict (drafts,
	// pending reviews, failures with Retry) outranks a bare review
	// request — the latter is an incoming ask with nothing prepared yet.
	deferred := func(item models.WorkItem) int {
		if item.Stage == "review-requested" {
			return 1
		}
		return 0
	}
	sort.Slice(work, func(i, j int) bool {
		if rank[work[i].Attention] != rank[work[j].Attention] {
			return rank[work[i].Attention] < rank[work[j].Attention]
		}
		if deferred(work[i]) != deferred(work[j]) {
			return deferred(work[i]) < deferred(work[j])
		}
		return work[i].UpdatedAt > work[j].UpdatedAt
	})
	return work, nil
}

// boardLimit mirrors the controller's absent-parent defaulting: zero or
// missing means the default, never "block everything".
func boardLimit(board *unstructured.Unstructured, field string, def int64) int64 {
	v, found, err := unstructured.NestedInt64(board.Object, "spec", "limits", field)
	if err != nil || !found || v <= 0 {
		return def
	}
	return v
}

// listIssues is page-capped: sorted newest-updated first by the callers,
// so the cap keeps the live edge and drops the archive tail — unbounded
// pagination on big repos was the feed's 20-second stall.
func listIssues(ctx context.Context, gh *github.Client, owner, repo string, opts *github.IssueListByRepoOptions) ([]*github.Issue, error) {
	const maxPages = 3
	opts.ListOptions = github.ListOptions{PerPage: 100}
	var all []*github.Issue
	for page := 0; page < maxPages; page++ {
		items, resp, err := gh.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return all, err
		}
		all = append(all, items...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
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

func workSandbox(sb *unstructured.Unstructured, autoIterateDefault bool) *models.WorkSandbox {
	if sb == nil {
		return nil
	}
	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	engine := sb.GetAnnotations()["board.gemini.google.com/engine"]
	if engine == "" {
		engine = "gemini" // pre-stamp sandboxes only ever ran gemini
	}
	// Effective auto-follow-up: the per-PR annotation overrides the
	// board policy in either direction (mirrors autoIterateEnabled).
	override := sb.GetAnnotations()["board.gemini.google.com/auto-iterate"]
	effective := autoIterateDefault
	switch override {
	case "on":
		effective = true
	case "off":
		effective = false
	}
	auto := "off"
	if effective {
		auto = "on"
	}
	return &models.WorkSandbox{
		Name:                  sb.GetName(),
		Replicas:              fmt.Sprintf("%d", replicas),
		TaskState:             sb.GetAnnotations()[annoTaskState],
		Engine:                engine,
		AutoIterate:           auto,
		AutoIterateOverridden: override == "on" || override == "off",
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

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, issue *github.Issue, repo, member string, viewLabels []string, autoDefault bool) {
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
	if len(viewLabels) > 0 && !hasAnyLabel(issue.Labels, viewLabels) && sb == nil && triageSB == nil {
		return
	}
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
	case triageSB != nil && triageDraft == "" && triageState != "Completed" && triageState != "Failed":
		// Triage sandbox provisioning (no task state yet). Finished
		// sandboxes without a draft (rejected, failed) rest instead.
		stage, attention = "triaging", attentionWorking
	case claimedBy == "":
		// Unclaimed and untouched: the triage inbox state.
		stage = "untriaged"
	}
	if sb == nil && triageSB != nil && (triageDraft != "" || triageState != "Completed") {
		// Surface the triage sandbox on rows without a fix sandbox while
		// it carries a draft or is still moving. A rejected leftover
		// (completed, draft cleared) stays off the row so it reads fully
		// reset — Triage / Plan / Fix again.
		sb = triageSB
	}

	var labels []string
	for _, l := range issue.Labels {
		labels = append(labels, l.GetName())
	}
	items[key] = &models.WorkItem{
		Type:            "issue",
		Group:           "issues",
		Number:          issue.GetNumber(),
		Author:          issue.GetUser().GetLogin(),
		Title:           issue.GetTitle(),
		HTMLURL:         issue.GetHTMLURL(),
		Stage:           stage,
		Attention:       attention,
		Assignee:        claimedBy,
		PRURL:           prURL,
		Labels:          labels,
		Draft:           triageDraft,
		TriagePublished: triagePublished,
		Plan:            planDraft,
		PlanApproved:    planApproved,
		Sandbox:         workSandbox(sb, autoDefault),
		UpdatedAt:       issue.GetUpdatedAt().UTC().Format(time.RFC3339),
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

func (s *Server) mergePRRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, pr *github.PullRequest, repo, member string, pendingOnGitHub, reviewedOnGitHub bool, viewLabels []string, autoDefault bool) {
	var sb *unstructured.Unstructured
	prStr := strconv.Itoa(pr.GetNumber())
	for _, candidate := range sandboxes {
		if candidate.GetLabels()["factory.gemini.google.com/pr"] == prStr {
			sb = candidate
			break
		}
	}
	// The PR label is stamped by the fix child's post-step; if that child
	// died (controller rollout) the alias is lost. Fall back to the PR's
	// closing refs — "fixes #N" names the fix sandbox directly.
	if sb == nil {
		for _, n := range closingRefs(pr.GetBody()) {
			if candidate, found := sandboxes[fmt.Sprintf("fix-%s-%d", repo, n)]; found {
				sb = candidate
				break
			}
		}
	}

	if len(viewLabels) > 0 && !hasAnyLabel(pr.Labels, viewLabels) && sb == nil {
		return
	}

	reviewRequested := false
	for _, reviewer := range pr.RequestedReviewers {
		if strings.EqualFold(reviewer.GetLogin(), member) {
			reviewRequested = true
		}
	}
	authored := strings.EqualFold(pr.GetUser().GetLogin(), member)

	// PR follow-up verbs (iterate / address-comments / investigate) run in
	// the fix sandbox; while one runs (or after it fails) the row shows it
	// as the machine's state, same as fixing/reviewing.
	followUpStage := ""
	if sb != nil {
		followUpNames := map[string]string{"iterate": "iterating", "address-comments": "addressing", "investigate": "investigating"}
		if name, ok := followUpNames[sb.GetAnnotations()["sandbox.gemini.google.com/last-task-type"]]; ok {
			switch sb.GetAnnotations()[annoTaskState] {
			case "Running":
				followUpStage = name
			case "Failed":
				followUpStage = name + "-failed"
			}
		}
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
		// cycle must not mask a re-review in flight. The stage is named by
		// the TASK TYPE: an aliased fix sandbox can be running a fix or a
		// follow-up, and calling those "reviewing" misled owners.
		stage, attention = "reviewing", attentionWorking
		switch sb.GetAnnotations()["sandbox.gemini.google.com/last-task-type"] {
		case "fix-issue":
			stage = "fixing"
		case "iterate":
			stage = "iterating"
		case "address-comments":
			stage = "addressing"
		case "investigate":
			stage = "investigating"
		}
	case restarting:
		stage, attention = "review-starting", attentionWorking
	case reviewError != "" && reviewState == "":
		// Parked failure — including pre-task failures (sandbox-ready
		// timeout, connect errors) that never stamp a task state. Without
		// this case they fall into "provisioning" below and render as
		// starting forever, with no Retry to unstick them.
		stage, attention = "review-failed", attentionNeedsYou
	case sb != nil && state == "" && reviewState == "":
		// Sandbox exists but the task hasn't stamped a state yet:
		// provisioning (pod scheduling, image pull, clone). Not the
		// member's move.
		stage, attention = "review-starting", attentionWorking
	case state == "Failed" && reviewState == "":
		// The run died without posting anything — surface it instead of
		// falling back to the pre-click stage.
		stage, attention = "review-failed", attentionNeedsYou
	case (reviewState == "submitted" || reviewedOnGitHub) && !reviewRequested:
		// The sandbox breadcrumb or GitHub itself: Reviewed ✓ survives
		// clean slates because the submitted review IS the record.
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
	case authored && followUpStage != "":
		stage, attention = followUpStage, attentionWorking
		if strings.HasSuffix(followUpStage, "-failed") {
			attention = attentionNeedsYou
		}
	case authored:
		stage, attention = "open", attentionWaiting
		// Your own draft PR awaits your promote — under the draft-PR
		// policy the agent opens drafts precisely so a human promotes
		// them, which makes promotion a pending human act (Up Next).
		// Same freshness decay as review requests: a deliberately parked
		// WIP draft fossilizes out of the inbox instead of nagging.
		if pr.GetDraft() && time.Since(pr.GetUpdatedAt()) <= reviewRequestFreshWindow {
			attention = attentionNeedsYou
		}
	}

	group := "review"
	if authored {
		group = "mine-pr"
	}

	var prLabels []string
	for _, l := range pr.Labels {
		prLabels = append(prLabels, l.GetName())
	}

	key := fmt.Sprintf("pr-%d", pr.GetNumber())
	itemError := ""
	if stage == "review-failed" {
		itemError = friendlyReviewError(reviewError)
	}
	items[key] = &models.WorkItem{
		Type:            "pr",
		Group:           group,
		Number:          pr.GetNumber(),
		Author:          pr.GetUser().GetLogin(),
		Title:           pr.GetTitle(),
		Labels:          prLabels,
		ReviewRequested: reviewRequested,
		HTMLURL:         pr.GetHTMLURL(),
		Stage:           stage,
		Attention:       attention,
		Error:           itemError,
		PRURL:           pr.GetHTMLURL(),
		DraftPR:         pr.GetDraft(),
		Fixes:           closingRefs(pr.GetBody()),
		Sandbox:         workSandbox(sb, autoDefault),
		UpdatedAt:       pr.GetUpdatedAt().UTC().Format(time.RFC3339),
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
	invalidateWorkFeed(board.GetNamespace(), board.GetName())
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
	// Any write invalidates the feed cache: the member's next poll must
	// reflect their click, not a cached pre-click universe.
	invalidateWorkFeed(board.GetNamespace(), board.GetName())
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
	Editable          bool     `json:"editable"`
	ViewLabels        []string `json:"viewLabels"`
	AutoTriage        string   `json:"autoTriage"`
	AutoFix           string   `json:"autoFix"`
	AutoReview        string   `json:"autoReview"`
	AutoLabels        []string `json:"autoLabels"`
	AutoExcludeLabels []string `json:"autoExcludeLabels"`
	RecencyDays       int64    `json:"recencyDays"`
	MaxActive         int64    `json:"maxActive"`
	IdleMinutes       int64    `json:"idleMinutes"`
	Engine            string   `json:"engine"`
	AutoIterate       bool     `json:"autoIterate"`
	DraftPR           bool     `json:"draftPR"`
	Disclose          bool     `json:"disclose"`
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
	view := boardSpecView{Editable: true, AutoTriage: "off", AutoFix: "off", AutoReview: "off", RecencyDays: 7}
	view.ViewLabels, _, _ = unstructured.NestedStringSlice(board.Object, "spec", "view", "labels")
	if v, _, _ := unstructured.NestedString(board.Object, "spec", "auto", "triage"); v != "" {
		view.AutoTriage = v
	}
	if v, _, _ := unstructured.NestedString(board.Object, "spec", "auto", "fix"); v != "" {
		view.AutoFix = v
	}
	if v, _, _ := unstructured.NestedString(board.Object, "spec", "auto", "review"); v != "" {
		view.AutoReview = v
	}
	view.AutoLabels, _, _ = unstructured.NestedStringSlice(board.Object, "spec", "auto", "labels")
	view.AutoExcludeLabels, _, _ = unstructured.NestedStringSlice(board.Object, "spec", "auto", "excludeLabels")
	if v, _, _ := unstructured.NestedInt64(board.Object, "spec", "auto", "recencyDays"); v > 0 {
		view.RecencyDays = v
	}
	view.MaxActive, _, _ = unstructured.NestedInt64(board.Object, "spec", "limits", "maxActive")
	view.IdleMinutes, _, _ = unstructured.NestedInt64(board.Object, "spec", "sandbox", "idleMinutes")
	view.Engine, _, _ = unstructured.NestedString(board.Object, "spec", "sandbox", "engine")
	if view.Engine == "" {
		view.Engine = "gemini"
	}
	if view.IdleMinutes <= 0 {
		view.IdleMinutes = 60
	}
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
	cleanLabels := func(in []string) []interface{} {
		out := make([]interface{}, 0, len(in))
		for _, l := range in {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	enum := func(v string, allowed ...string) string {
		for _, a := range allowed {
			if v == a {
				return v
			}
		}
		return "off"
	}
	recency := payload.RecencyDays
	if recency <= 0 {
		recency = 7
	}
	idleMinutes := payload.IdleMinutes
	if idleMinutes <= 0 {
		idleMinutes = 60
	}
	ok := set(cleanLabels(payload.ViewLabels), "spec", "view", "labels") &&
		set(enum(payload.AutoTriage, "off", "unclaimed", "all"), "spec", "auto", "triage") &&
		set(enum(payload.AutoFix, "off", "assigned"), "spec", "auto", "fix") &&
		set(enum(payload.AutoReview, "off", "requested", "all"), "spec", "auto", "review") &&
		set(cleanLabels(payload.AutoLabels), "spec", "auto", "labels") &&
		set(cleanLabels(payload.AutoExcludeLabels), "spec", "auto", "excludeLabels") &&
		set(recency, "spec", "auto", "recencyDays") &&
		set(payload.MaxActive, "spec", "limits", "maxActive") &&
		set(idleMinutes, "spec", "sandbox", "idleMinutes") &&
		set(engineOrDefault(payload.Engine), "spec", "sandbox", "engine") &&
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

// autoIterateDefault mirrors the controller's policy defaulting: absent
// means enabled.
func autoIterateDefault(board *unstructured.Unstructured) bool {
	v, found, _ := unstructured.NestedBool(board.Object, "spec", "policy", "autoIterate")
	return !found || v
}

// autoIterateBoardPR sets or clears the per-PR auto-follow-up override on
// the PR's fix sandbox: {"mode": "on" | "off" | "inherit"}.
func (s *Server) autoIterateBoardPR(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Mode != "on" && req.Mode != "off" && req.Mode != "inherit") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be on, off, or inherit"})
		return
	}
	sb, ns := s.findPRFixSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no agent sandbox for this PR"})
		return
	}
	value := req.Mode
	if value == "inherit" {
		value = ""
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), "board.gemini.google.com/auto-iterate", value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set auto-iterate", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// findPRFixSandbox locates the fix sandbox that created this PR (the
// factory pr label), checking the viewer's namespace then the board's.
func (s *Server) findPRFixSandbox(c *gin.Context, board *unstructured.Unstructured, owner, repo string, number int) (*unstructured.Unstructured, string) {
	ctx := c.Request.Context()
	prStr := strconv.Itoa(number)
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		for _, sb := range sandboxes {
			if (strings.HasPrefix(sb.GetName(), "fix-") || strings.HasPrefix(sb.GetName(), "factory-pr-")) &&
				sb.GetLabels()["factory.gemini.google.com/pr"] == prStr {
				return sb, ns
			}
		}
		// Alias lost (fix child died before stamping): resolve through the
		// cached feed's closing refs and heal the alias so the watch and
		// the label path recover too — exactly what AliasSandboxToPR would
		// have stamped.
		if items, ok := workFeedPeek(board.GetNamespace() + "/" + board.GetName()); ok {
			for i := range items {
				if items[i].Type != "pr" || items[i].Number != number {
					continue
				}
				for _, ref := range items[i].Fixes {
					name := fmt.Sprintf("fix-%s-%d", repo, ref)
					sb, found := sandboxes[name]
					if !found {
						continue
					}
					_ = s.K8sManager.UpdateSandboxLabel(ctx, ns, name, "factory.gemini.google.com/pr", prStr)
					_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, "pr", prStr)
					_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, "htmlURL", items[i].HTMLURL)
					return sb, ns
				}
			}
		}
	}
	return nil, ""
}

// kickoffPRTask stamps a follow-up request (Iterate / Address comments /
// Fix CI) on the PR's fix sandbox. The annotation is the durable consent
// the controller drives from — no mailbox claim to strand on a restart.
func (s *Server) kickoffPRTask(c *gin.Context, reqKey, instructionKey string) {
	// boardWriteContext's fifth return is the member TOKEN, not the member
	// — the mailbox records the executor namespace (a credential in a CR
	// annotation was the failure mode this comment guards against).
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	member := s.Auth.GetNamespaceFromContext(c)
	var req struct {
		Instruction string `json:"instruction"`
	}
	_ = c.ShouldBindJSON(&req) // body optional
	sb, ns := s.findPRFixSandbox(c, board, owner, repo, number)
	if sb == nil {
		// Hand-made PR: no sandbox yet. Bridge via the mailbox — the
		// controller launches the factory verb, factory ensures the
		// factory-pr sandbox itself (gh pr checkout attaches the branch),
		// and the claim converts to the durable sandbox annotation once
		// the sandbox exists.
		kind := map[string]string{
			"board.gemini.google.com/iterate-requested-at":     "iterate",
			"board.gemini.google.com/address-requested-at":     "address",
			"board.gemini.google.com/investigate-requested-at": "investigate",
		}[reqKey]
		annotations := board.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		if kind == "iterate" && strings.TrimSpace(req.Instruction) != "" {
			annotations[fmt.Sprintf("board.gemini.google.com/iterate-instruction-%d", number)] = strings.TrimSpace(req.Instruction)
		}
		requests := map[string]string{}
		if raw := annotations[annoBoardRequests]; raw != "" {
			_ = json.Unmarshal([]byte(raw), &requests)
		}
		requests[fmt.Sprintf("%s-%d", kind, number)] = member
		b, _ := json.Marshal(requests)
		annotations[annoBoardRequests] = string(b)
		board.SetAnnotations(annotations)
		if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record request", "details": err.Error()})
			return
		}
		c.Status(http.StatusOK)
		return
	}
	if instructionKey != "" {
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), instructionKey, strings.TrimSpace(req.Instruction)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record instruction", "details": err.Error()})
			return
		}
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), reqKey, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record request", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) iterateBoardPR(c *gin.Context) {
	s.kickoffPRTask(c, "board.gemini.google.com/iterate-requested-at", "board.gemini.google.com/iterate-instruction")
}

func (s *Server) addressBoardPR(c *gin.Context) {
	s.kickoffPRTask(c, "board.gemini.google.com/address-requested-at", "")
}

func (s *Server) investigateBoardPR(c *gin.Context) {
	s.kickoffPRTask(c, "board.gemini.google.com/investigate-requested-at", "")
}

// kickoffExplore records an exploration click: a mailbox claim (member
// namespace as the value — never a token) plus the run parameters as
// board annotations; the controller bridges sandbox creation and
// converts the claim to durable sandbox annotations.
func (s *Server) kickoffExplore(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	var req struct {
		Kind     string `json:"kind"`
		Topic    string `json:"topic"`
		Since    string `json:"since"`
		Scenario string `json:"scenario"`
		Guidance string `json:"guidance"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Kind != "onboard" && req.Kind != "activity" && req.Kind != "topic") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be onboard, activity, or topic"})
		return
	}
	if req.Kind == "topic" && strings.TrimSpace(req.Topic) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "topic is required for kind=topic"})
		return
	}
	annotations := board.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if req.Kind == "topic" {
		annotations["board.gemini.google.com/explore-topic"] = strings.TrimSpace(req.Topic)
	}
	if req.Kind == "activity" {
		since := strings.TrimSpace(req.Since)
		if since == "" {
			since = "2 weeks"
		}
		annotations["board.gemini.google.com/explore-since"] = since
	}
	requests := map[string]string{}
	if raw := annotations[annoBoardRequests]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &requests)
	}
	// The claim carries its click time: the controller judges served-ness
	// against the runner's per-kind result (member|RFC3339).
	requests["explore-"+req.Kind] = namespace + "|" + nowRFC3339()
	b, _ := json.Marshal(requests)
	annotations[annoBoardRequests] = string(b)
	board.SetAnnotations(annotations)
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record exploration request", "details": err.Error()})
		return
	}
	invalidateWorkFeed(board.GetNamespace(), board.GetName())
	c.Status(http.StatusOK)
}

// getBoardExploration reads the exploration state: the docs on the
// member's fork branch (git is the record — renders even with the
// sandbox paused or deleted) plus the explore sandbox's task state.
func (s *Server) getBoardExploration(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}

	out := gin.H{
		"branch":    "exploration/notes",
		"forkOwner": member,
		"gcpProject": func() string {
			if sec, serr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, GcpSecretName, v1.GetOptions{}); serr == nil {
				return string(sec.Data["project"])
			}
			return ""
		}(),
		"branchURL": fmt.Sprintf("https://github.com/%s/%s/tree/exploration/notes/docs-exploration", member, repo),
		"docs":      []gin.H{},
	}

	sbName := "explore-" + strings.ToLower(repo)
	var completedAt time.Time
	if sb, serr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sbName, v1.GetOptions{}); serr == nil {
		annotations := sb.GetAnnotations()
		// The engine annotation is only stamped on relaunches into an
		// existing sandbox; a first run's icon comes from the board.
		engine := annotations["board.gemini.google.com/engine"]
		if engine == "" {
			boardEngine, _, _ := unstructured.NestedString(board.Object, "spec", "sandbox", "engine")
			engine = engineOrDefault(boardEngine)
		}
		out["sandbox"] = gin.H{
			"name":      sbName,
			"taskState": annotations[annoTaskState],
			"taskType":  annotations["sandbox.gemini.google.com/last-task-type"],
			"engine":    engine,
		}
		completedAt, _ = time.Parse(time.RFC3339, annotations["sandbox.gemini.google.com/completion-time"])
	}

	// A standing mailbox claim means "requested, not yet served" — the
	// UI's queued state. Claims carry their click time (member|RFC3339);
	// one already served (a completion newer than the click, awaiting the
	// controller's trim) must not re-show as queued. Sorted so two
	// standing claims pick the same one every poll instead of flickering
	// with map order.
	if raw := board.GetAnnotations()[annoBoardRequests]; raw != "" {
		requests := map[string]string{}
		_ = json.Unmarshal([]byte(raw), &requests)
		keys := make([]string, 0, len(requests))
		for key := range requests {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			kind, ok := strings.CutPrefix(key, "explore-")
			if !ok {
				continue
			}
			if _, at, hasTS := strings.Cut(requests[key], "|"); hasTS {
				if clickedAt, terr := time.Parse(time.RFC3339, at); terr == nil && !completedAt.IsZero() && completedAt.After(clickedAt) {
					continue
				}
			}
			out["pending"] = kind
			break
		}
	}

	token, terr := s.memberToken(ctx, namespace)
	if terr == nil {
		gh := githubClientForToken(ctx, token)
		_, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, "docs-exploration",
			&github.RepositoryContentGetOptions{Ref: "exploration/notes"})
		if derr == nil {
			docs := []gin.H{}
			for _, entry := range dir {
				if entry.GetType() == "file" && strings.HasSuffix(entry.GetName(), ".md") {
					docs = append(docs, gin.H{"name": entry.GetName(), "path": entry.GetPath(), "htmlURL": entry.GetHTMLURL(), "size": entry.GetSize()})
				}
				// One level of subdirectories: activity/, comparisons/,
				// sessions/ — that's where digests and deep-dives land.
				if entry.GetType() == "dir" {
					_, sub, _, suberr := gh.Repositories.GetContents(ctx, member, repo, entry.GetPath(),
						&github.RepositoryContentGetOptions{Ref: "exploration/notes"})
					if suberr != nil {
						continue
					}
					for _, f := range sub {
						if f.GetType() == "file" && strings.HasSuffix(f.GetName(), ".md") {
							docs = append(docs, gin.H{"name": entry.GetName() + "/" + f.GetName(), "path": f.GetPath(), "htmlURL": f.GetHTMLURL(), "size": f.GetSize()})
						}
					}
				}
			}
			out["docs"] = docs
		}
	}
	c.JSON(http.StatusOK, out)
}

// engineOrDefault normalizes the gear's engine choice; anything but an
// explicit "claude" is gemini (the CRD enum rejects other values anyway).
// getBoardExplorationDoc returns one exploration doc's raw markdown from
// the member's fork branch; rendering happens in the browser. The path
// is constrained to the docs-exploration tree.
func (s *Server) getBoardExplorationDoc(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}
	path := c.Query("path")
	if !strings.HasPrefix(path, "docs-exploration/") || strings.Contains(path, "..") || !strings.HasSuffix(path, ".md") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path must be a markdown file under docs-exploration/"})
		return
	}
	token, terr := s.memberToken(ctx, namespace)
	if terr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "No member token"})
		return
	}
	gh := githubClientForToken(ctx, token)
	file, _, _, gerr := gh.Repositories.GetContents(ctx, member, repo, path,
		&github.RepositoryContentGetOptions{Ref: "exploration/notes"})
	if gerr != nil || file == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "doc not found on exploration/notes"})
		return
	}
	content, cerr := file.GetContent()
	if cerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not decode doc"})
		return
	}
	c.String(http.StatusOK, content)
}

func engineOrDefault(engine string) string {
	if engine == "claude" {
		return "claude"
	}
	return "gemini"
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

// rejectBoardTriage discards a triage draft: breadcrumbs are cleared so
// the row returns to its resting stage (Triage / Plan / Fix again), and
// the tombstone stops auto-triage from redoing thrown-away work. A fresh
// Triage click re-arms.
func (s *Server) rejectBoardTriage(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	name := fmt.Sprintf("triage-%s-%d", repo, number)
	for _, ns := range []string{board.GetNamespace(), s.Auth.GetNamespaceFromContext(c)} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		sb, found := sandboxes[name]
		if !found || sb.GetAnnotations()["agentDraft"] == "" {
			continue
		}
		for _, key := range []string{"agentDraft", "agentDraftType", "board.gemini.google.com/triaged-at", annoTriagePublished} {
			if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, key, ""); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear triage draft", "details": err.Error()})
				return
			}
		}
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, "board.gemini.google.com/triage-rejected-at", nowRFC3339()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reject triage", "details": err.Error()})
			return
		}
		_ = s.K8sManager.ScaledownSandboxByName(ctx, ns, name)
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "no triage suggestion to reject"})
}

// putBoardPlanDraft saves a member-edited plan back onto the plan sandbox
// — quick refinement by hand, alongside the chat loop. Plans are markdown:
// the only validation is non-emptiness. The plan EXECUTES from
// /workspaces/plan-issue-N.md inside the sandbox (fix --with-plan) and a
// continued chat reads it there, so the edit must land in the file too —
// which needs the pod up.
func (s *Server) putBoardPlanDraft(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var req struct {
		Plan string `json:"plan"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Plan) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan text is required"})
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to edit"})
		return
	}
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sb.GetName(), ns)
	if err != nil || podID == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "sandbox is paused — wake it from the agent card first (the plan executes from a file inside the sandbox, so edits must reach it)"})
		return
	}
	plan := strings.TrimSpace(req.Plan)
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", fmt.Sprintf("cat > /workspaces/plan-issue-%d.md", number)},
		Stdin:   []byte(plan + "\n"),
	}); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to write the plan into the sandbox", "details": err.Error()})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlanDraft, plan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save plan", "details": err.Error()})
		return
	}
	// The edit is the newest human word on the plan: bump planned-at so a
	// stale feedback stamp cannot trigger a refine that overwrites it.
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlannedAt, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save plan", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// planBoardRefresh re-reads the plan file from the running sandbox — the
// read-time half of the chat tieback: an agent edits the file during a
// continued session, and the board picks it up when the plan panel opens.
// No session-end event exists or is needed: the file is the source of
// truth, the annotation is its cache for paused pods. An approved plan is
// frozen — approval covers exactly what was seen.
func (s *Server) planBoardRefresh(c *gin.Context) {
	ctx, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to refresh"})
		return
	}
	annotations := sb.GetAnnotations()
	draft := annotations[annoPlanDraft]
	if annotations[annoPlanApproved] != "" {
		c.JSON(http.StatusOK, gin.H{"changed": false, "plan": draft})
		return
	}
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sb.GetName(), ns)
	if err != nil || podID == nil {
		c.JSON(http.StatusOK, gin.H{"changed": false, "plan": draft, "paused": true})
		return
	}
	var stdout bytes.Buffer
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", fmt.Sprintf("cat /workspaces/plan-issue-%d.md 2>/dev/null", number)},
		Stdout:  &stdout,
	}); err != nil {
		c.JSON(http.StatusOK, gin.H{"changed": false, "plan": draft})
		return
	}
	fresh := strings.TrimSpace(stdout.String())
	if fresh == "" || fresh == strings.TrimSpace(draft) {
		c.JSON(http.StatusOK, gin.H{"changed": false, "plan": draft})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlanDraft, fresh); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store the refreshed plan", "details": err.Error()})
		return
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), annoPlannedAt, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store the refreshed plan", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"changed": true, "plan": fresh})
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
