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
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	yamlv3 "go.yaml.in/yaml/v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/ghquota"
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

// githubClientForToken is injectable for tests. ghquota owns the transport
// stack: conditional requests, where a 304 costs ZERO rate-limit quota,
// and a gate that stops calling once GitHub says the member's budget is
// spent. Cache entries key per token — critical here, where review
// responses carry viewer-private pending reviews.
var githubClientForToken = func(ctx context.Context, token string) *github.Client {
	return github.NewClient(ghquota.HTTPClient(token))
}

// githubHTTPForToken is the same transport without go-github on top, for
// the GraphQL feed query. Injectable alongside githubClientForToken so a
// test answers both through one RoundTripper.
var githubHTTPForToken = func(token string) *http.Client {
	return ghquota.HTTPClient(token)
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

// repoPermCache caches the access each user's token has on a repo
// (design §4.1: ~15 min, so GitHub-side revocation propagates).
var repoPermCache = struct {
	sync.Mutex
	entries map[string]repoPermEntry
}{entries: map[string]repoPermEntry{}}

type repoPermEntry struct {
	perms   repoPerms
	expires time.Time
}

// repoPerms is what a user may do on a repo: push for the board's own
// buttons (Fix, Promote, …), triage for labelling issues.
type repoPerms struct {
	push   bool
	triage bool
}

// The viewer's review states used to live in a 60s cache here, refilled by
// one ListReviews per open PR. They now arrive nested in the board's single
// GraphQL query (see board_graphql.go), fresh on every rebuild, so there is
// nothing left to cache and nothing left to invalidate.

func (s *Server) hasPushPermission(ctx context.Context, namespace, sessionUser, repoURL string) bool {
	return s.repoPermissions(ctx, namespace, sessionUser, repoURL).push
}

func (s *Server) repoPermissions(ctx context.Context, namespace, sessionUser, repoURL string) repoPerms {
	key := sessionUser + "|" + repoURL
	repoPermCache.Lock()
	if e, ok := repoPermCache.entries[key]; ok && time.Now().Before(e.expires) {
		repoPermCache.Unlock()
		return e.perms
	}
	repoPermCache.Unlock()

	// Only a definitive GitHub answer is cached for the full TTL. A
	// transient failure (token fetch, network, rate limit) must not poison
	// the verdict: it would silently strip Fix/Promote/Publish from the UI
	// for 15 minutes. On error, keep any previous verdict and retry soon.
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return repoPerms{}
	}
	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		return s.stalePermsOrNone(key)
	}
	gh := githubClientForToken(ctx, token)
	repository, _, err := gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		klog.FromContext(ctx).Info("repo-permission check failed; keeping previous verdict", "repo", repoURL, "err", err)
		return s.stalePermsOrNone(key)
	}
	p := repository.GetPermissions()
	push := p["push"] || p["maintain"] || p["admin"]
	perms := repoPerms{push: push, triage: push || p["triage"]}
	repoPermCache.Lock()
	repoPermCache.entries[key] = repoPermEntry{perms: perms, expires: time.Now().Add(15 * time.Minute)}
	repoPermCache.Unlock()
	return perms
}

// stalePermsOrNone returns the last cached verdict (even expired) when a
// fresh check could not be made, extending it briefly so the next request
// retries soon.
func (s *Server) stalePermsOrNone(key string) repoPerms {
	repoPermCache.Lock()
	defer repoPermCache.Unlock()
	if e, ok := repoPermCache.entries[key]; ok {
		repoPermCache.entries[key] = repoPermEntry{perms: e.perms, expires: time.Now().Add(30 * time.Second)}
		return e.perms
	}
	return repoPerms{}
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
	// blockedUntil is when GitHub said this board's token gets its budget
	// back. Until then a rebuild cannot learn anything the entry does not
	// already know, so the entry keeps being served however old it is.
	blockedUntil time.Time
}

const (
	// workFeedFreshFor must EXCEED the UI's poll interval (20s), or every
	// poll lands past fresh, serves the cache and kicks a rebuild behind
	// it — the cache buys latency and no quota at all. A minute of
	// freshness costs the board nothing that matters: clicks invalidate
	// the entry directly, so only changes made elsewhere wait, and the
	// budget is shared with the member's own GitHub use.
	workFeedFreshFor      = time.Minute
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
	if time.Now().Before(e.blockedUntil) {
		// Out of budget: what we hold is the best there is until it comes
		// back, and asking again only deepens the hole. Serve it at any
		// age rather than rebuild into a wall or blank the board.
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
	if !found || (time.Since(e.at) > workFeedServeStaleFor && !time.Now().Before(e.blockedUntil)) {
		return nil, false
	}
	return e.items, true
}

// workFeedPut stores a build that reached GitHub, which also clears any
// block: the budget is demonstrably back.
func workFeedPut(key string, items []models.WorkItem) {
	workFeedCache.Lock()
	workFeedCache.entries[key] = workFeedEntry{items: items, at: time.Now()}
	delete(workFeedCache.refreshing, key)
	workFeedCache.Unlock()
}

// workFeedBlock records that a rebuild could not reach GitHub because the
// budget was spent. The items and their age are left exactly as they were
// — this says "do not come back before then", not "this is fresh".
func workFeedBlock(key string, until time.Time) {
	workFeedCache.Lock()
	defer workFeedCache.Unlock()
	e, found := workFeedCache.entries[key]
	if !found {
		return
	}
	e.blockedUntil = until
	workFeedCache.entries[key] = e
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
		if until, ok := ghquota.ResetAt(err); ok {
			workFeedBlock(key, until)
		}
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
		c.JSON(http.StatusOK, s.forViewer(ctx, board, namespace, sessionUser, items))
		return
	}

	items, err := s.buildBoardWork(ctx, board, member, namespace)
	if err != nil {
		// Nothing cached and no budget to build with: say so. An empty
		// feed would read as "no work", which is a lie the board cannot
		// afford to tell.
		if ghquota.IsRateLimited(err) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitHub rate limit reached", "details": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build board feed", "details": err.Error()})
		return
	}
	workFeedPut(key, items)
	c.JSON(http.StatusOK, s.forViewer(ctx, board, namespace, sessionUser, items))
}

// buildBoardWork assembles the feed universe from one GraphQL request
// (board_graphql.go), capped at 100 per surface and newest first: the
// board is a work queue, not an archive — on huge repos the tail belongs
// on GitHub search, not in every poll.
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
	// Sandboxes live where claims point: the board namespace plus every
	// namespace named by an assignee claim on this repo's items.
	sandboxNamespaces := map[string]bool{}
	sandboxes := map[string]*unstructured.Unstructured{}
	// Every sandbox loaded, whatever its namespace: sandboxes is by name,
	// and two members' sandboxes for one issue share it. Triage, run in
	// the clicker's namespace, is looked up here.
	var allSandboxes []*unstructured.Unstructured
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
			allSandboxes = append(allSandboxes, v)
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
			s.mergeIssueRow(items, sandboxes, allSandboxes, issue, repo, member, viewLabels, autoIterateDefault(board))
		}
	}
	// One request for the whole board. A hole in the universe would show
	// fewer rows than exist — "nothing needs you" is the one wrong answer
	// a work queue must not give — so anything short of a complete answer
	// fails the rebuild and the previous feed keeps standing.
	snap, err := fetchBoardSnapshot(ctx, githubHTTPForToken(token), owner, repo, member)
	if err != nil {
		if ghquota.IsRateLimited(err) {
			return nil, fmt.Errorf("github budget spent while building the feed: %w", err)
		}
		return nil, fmt.Errorf("failed to read the board from github: %w", err)
	}
	assigned, created, allIssues, prs := snap.assigned, snap.created, snap.triage, snap.prs
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
		s.mergeIssueRow(items, sandboxes, allSandboxes, issue, repo, member, viewLabels, autoIterateDefault(board))
	}

	// GitHub is the only durable record of the member's reviews, so both a
	// parked pending review and a submitted one must survive lost sandbox
	// breadcrumbs. (Requested-only gating could never see the submitted
	// case: submitting clears the reviewer request.) This used to be one
	// ListReviews per non-authored PR, fanned out — up to a hundred calls
	// a rebuild, and the single largest thing the board spent. It now
	// arrives nested in the same query the PRs did, so it is free, fresh
	// every rebuild, and needs no cache of its own.
	statesByPR := snap.reviews
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

	// A standing Request is a click the controller hasn't materialized
	// yet (launch window is up to a reconcile): render those items as
	// starting so the member sees immediate feedback and no second
	// kickoff is invited.
	if requests := s.boardRequests(ctx, board); len(requests) > 0 {
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
		for _, req := range requests {
			n := strconv.Itoa(req.Spec.Number)
			switch req.Spec.Verb {
			case boardv1alpha1.VerbReview:
				if item, found := items["pr-"+n]; found && preRunPR[item.Stage] {
					mark(item, "review-starting")
				}
			case boardv1alpha1.VerbFix:
				if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
					mark(item, "fix-starting")
				}
			case boardv1alpha1.VerbTriage:
				if item, found := items["issue-"+n]; found && (item.Stage == "untriaged" || item.Stage == "open") {
					// Triage is only board-capacity gated, not per-user.
					item.Stage, item.Attention = "triaging", attentionWorking
				}
			case boardv1alpha1.VerbPlan:
				if item, found := items["issue-"+n]; found && preRunIssue[item.Stage] {
					mark(item, "planning")
				}
			case boardv1alpha1.VerbIterate, boardv1alpha1.VerbAddress, boardv1alpha1.VerbInvestigate:
				stageName := map[string]string{
					boardv1alpha1.VerbIterate:     "iterating",
					boardv1alpha1.VerbAddress:     "addressing",
					boardv1alpha1.VerbInvestigate: "investigating",
				}[req.Spec.Verb]
				if item, found := items["pr-"+n]; found {
					item.Stage, item.Attention = stageName, attentionWorking
				}
			}
		}
	}

	s.markApplies(ctx, board, items)

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

func (s *Server) boardSandboxes(ctx context.Context, namespace, owner, repo string) (map[string]*unstructured.Unstructured, error) {
	list, err := s.K8sManager.ListSandboxes(ctx, namespace, "factory.gemini.google.com/managed=true")
	if err != nil {
		return nil, err
	}
	byName := map[string]*unstructured.Unstructured{}
	repoHint := fmt.Sprintf("github.com/%s/%s/", owner, repo)
	for i := range list.Items {
		sb := &list.Items[i]
		if factorycli.LaunchedElsewhere(sb.GetLabels()) {
			continue
		}
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

// annoTaskEngine is where factory records the engine of the task it last
// started in a sandbox, in the same write that marks it Running.
const annoTaskEngine = "sandbox.gemini.google.com/last-task-engine"

// sandboxEngine is the engine that ran in a sandbox, or fallback when
// nothing recorded it.
//
// factory's own record comes first: it is written by the invocation
// that ran the task, including the one that created the sandbox. The
// controller's stamp cannot cover that first launch — it can only
// annotate a sandbox that already exists — so a new PR on an antigravity
// or claude board used to read as gemini until its second run.
func sandboxEngine(annotations map[string]string, fallback string) string {
	if e := annotations[annoTaskEngine]; e != "" {
		return e
	}
	if e := annotations["board.gemini.google.com/engine"]; e != "" {
		return e
	}
	return fallback
}

func workSandbox(sb *unstructured.Unstructured, autoIterateDefault bool) *models.WorkSandbox {
	if sb == nil {
		return nil
	}
	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	engine := sandboxEngine(sb.GetAnnotations(), "gemini") // pre-stamp sandboxes only ever ran gemini
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

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, allSandboxes []*unstructured.Unstructured, issue *github.Issue, repo, member string, viewLabels []string, autoDefault bool) {
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

	sb := factorycli.IssueSandbox(maps.Values(sandboxes), repo, issue.GetNumber())
	state := ""
	prURL := ""
	taskType := ""
	planDraft := ""
	planApproved := false
	planRevising := false
	var planActions []models.WorkAction
	if sb != nil {
		annotations := sb.GetAnnotations()
		state = annotations[annoTaskState]
		taskType = annotations[annoLastTaskType]
		planDraft = annotations[annoPlanDraft]
		planApproved = annotations[annoPlanApproved] != ""
		planRevising = planIsRevising(annotations)
		if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
			prURL = u
		}
		if planDraft != "" && !planApproved {
			planActions = planWorkActions(annotations, planRevising, state == "Running")
		}
	}
	triageDraft := ""
	triageState := ""
	triagePublished := false
	var triageActions []models.WorkAction
	triageSB := factorycli.TriageSandbox(slices.Values(allSandboxes), repo, issue.GetNumber())
	if len(viewLabels) > 0 && !hasAnyLabel(issue.Labels, viewLabels) && sb == nil && triageSB == nil {
		return
	}
	if triageSB != nil {
		triageDraft = factorycli.TriageDraft(triageSB)
		triageState = factorycli.TriageState(triageSB)
		triagePublished = triageSB.GetAnnotations()[annoTriagePublished] != ""
		if triageDraft != "" {
			triageActions = triageWorkActions(triageSB.GetAnnotations())
		}
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
		// Approved: the fix Request is in flight.
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
	if sb != nil && sb == triageSB && taskType == "" {
		// The issue's sandbox has only been triaged in: it is the triage's
		// on the row, not a fix's.
		sb = nil
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
		TriageActions:   triageActions,
		PlanActions:     planActions,
		Sandbox:         workSandbox(sb, autoDefault),
		UpdatedAt:       issue.GetUpdatedAt().UTC().Format(time.RFC3339),
	}
	if ws := items[key].Sandbox; ws != nil && sb == triageSB && ws.TaskState == "" {
		ws.TaskState = triageState
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
			if candidate := factorycli.IssueSandbox(maps.Values(sandboxes), repo, n); candidate != nil && !factorycli.OnlyTriaged(candidate) {
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
// (assignment) and trigger label, plus the authoritative Request the
// controller consumes. Consent is the click — the session user is the
// executor.
func (s *Server) kickoffFix(c *gin.Context) {
	s.kickoff(c, "issue")
}

// kickoffReview handles the Review click: best-effort self-requested review
// and trigger label, plus the Request.
func (s *Server) kickoffReview(c *gin.Context) {
	s.kickoff(c, "pr")
}

// kickoffTriage handles the Triage click: a Request only — triage is
// draft-only, so there is no GitHub-side claim to make.
// kickoffPlan handles the Plan click: a Request only — planning is
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
	// proceeds via the Request even when the token lacks triage rights.
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
		log.Info("member token unavailable; request-only kickoff", "err", err)
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

	// Authoritative record of the click; the controller serves it and
	// writes back what came of it.
	verb := map[string]string{
		"issue":  boardv1alpha1.VerbFix,
		"pr":     boardv1alpha1.VerbReview,
		"triage": boardv1alpha1.VerbTriage,
		"plan":   boardv1alpha1.VerbPlan,
	}[kind]
	if verb == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown kickoff kind"})
		return
	}
	if _, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:   verb,
		Member: member,
		Number: number,
	}); err != nil {
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
	issue, _ := strconv.Atoi(number)
	for _, ns := range []string{namespace, board.GetNamespace()} {
		if sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo); err == nil {
			if sb := factorycli.IssueSandbox(maps.Values(sandboxes), repo, issue); sb != nil && !factorycli.OnlyTriaged(sb) {
				sandboxNS, sandboxName = ns, sb.GetName()
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

	// Nothing to invalidate: the next rebuild reads the member's review
	// states from GitHub as part of the board query, so a discarded review
	// stops being announced as soon as the feed refreshes.

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
			_, isIssue := factorycli.IssueOf(sb, repo)
			if (isIssue || strings.HasPrefix(sb.GetName(), "factory-pr-")) &&
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
					sb := factorycli.IssueSandbox(maps.Values(sandboxes), repo, ref)
					if sb == nil || factorycli.OnlyTriaged(sb) {
						continue
					}
					name := sb.GetName()
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
// the controller drives from, so there is no Request left standing to
// strand on a restart.
func (s *Server) kickoffPRTask(c *gin.Context, reqKey, instructionKey string) {
	// boardWriteContext's fifth return is the member TOKEN, not the member
	// — the Request records the executor namespace (a credential in a CR
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
		// Hand-made PR: no sandbox yet. Bridge with a Request — the
		// controller launches the factory verb, factory ensures the
		// factory-pr sandbox itself (gh pr checkout attaches the branch),
		// and the claim converts to the durable sandbox annotation once
		// the sandbox exists.
		//
		// The instruction rides on the Request rather than a per-PR
		// board annotation. That annotation was written on the click and
		// deleted by nothing: it outlived the iteration it was typed for
		// and steered the next one.
		verb := map[string]string{
			"board.gemini.google.com/iterate-requested-at":     boardv1alpha1.VerbIterate,
			"board.gemini.google.com/address-requested-at":     boardv1alpha1.VerbAddress,
			"board.gemini.google.com/investigate-requested-at": boardv1alpha1.VerbInvestigate,
		}[reqKey]
		if _, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
			Verb:        verb,
			Member:      member,
			Number:      number,
			Instruction: strings.TrimSpace(req.Instruction),
		}); err != nil {
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

// engineOrDefault normalizes the gear's engine choice; anything but an
// explicit "claude" or "antigravity" is gemini (the CRD enum rejects other
// values anyway).
func engineOrDefault(engine string) string {
	switch engine {
	case "claude", "antigravity":
		return engine
	}
	return "gemini"
}

// findPlanSandbox locates the issue's fix sandbox carrying a plan draft,
// checking the viewer's namespace then the board's.
func (s *Server) findPlanSandbox(c *gin.Context, board *unstructured.Unstructured, owner, repo string, number int) (*unstructured.Unstructured, string) {
	ctx := c.Request.Context()
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		if sb := factorycli.IssueSandbox(maps.Values(sandboxes), repo, number); sb != nil && sb.GetAnnotations()[annoPlanDraft] != "" {
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
	// The fix kickoff does the rest: GitHub claim (assignment) + the Request.
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
	for _, key := range []string{annoPlanDraft, annoPlannedAt, annoPlanFeedback, annoPlanFeedbackAt, annoPlanApproved, factorycli.AnnotationPlanOutput, factorycli.AnnotationPlanCommented} {
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
	if sb, ns := s.findTriageDraft(c, board, owner, repo, number); sb != nil {
		name := sb.GetName()
		for _, key := range []string{factorycli.AnnotationTriageDraft, "board.gemini.google.com/triaged-at", annoTriagePublished, factorycli.AnnotationTriageOutput, factorycli.AnnotationTriageLabeled} {
			if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, key, ""); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear triage draft", "details": err.Error()})
				return
			}
		}
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, name, "board.gemini.google.com/triage-rejected-at", nowRFC3339()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reject triage", "details": err.Error()})
			return
		}
		s.scaledownTriaged(ctx, sb)
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
	req.Draft = factorycli.NormalizeTriageDraft(req.Draft)
	if err := validateTriageDraft(req.Draft); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "draft does not match the triage schema", "details": err.Error()})
		return
	}

	if sb, ns := s.findTriageDraft(c, board, owner, repo, number); sb != nil {
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.AnnotationTriageDraft, strings.TrimSpace(req.Draft)+"\n"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save draft", "details": err.Error()})
			return
		}
		c.Status(http.StatusOK)
		return
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

// findTriageDraft locates the sandbox holding issue number's triage draft:
// the issue's sandbox in the viewer's namespace, where their triage ran,
// then the board's, where auto-triage runs.
func (s *Server) findTriageDraft(c *gin.Context, board *unstructured.Unstructured, owner, repo string, number int) (*unstructured.Unstructured, string) {
	ctx := c.Request.Context()
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sandboxes, err := s.boardSandboxes(ctx, ns, owner, repo)
		if err != nil {
			continue
		}
		if sb := factorycli.TriageSandbox(maps.Values(sandboxes), repo, number); sb != nil && factorycli.TriageDraft(sb) != "" {
			return sb, ns
		}
	}
	return nil, ""
}

// scaledownTriaged parks a sandbox once its triage is published or thrown
// away, unless a plan or fix is running in it.
func (s *Server) scaledownTriaged(ctx context.Context, sb *unstructured.Unstructured) {
	if sb.GetAnnotations()[annoTaskState] == "Running" {
		return
	}
	_ = s.K8sManager.ScaledownSandboxByName(ctx, sb.GetNamespace(), sb.GetName())
}
