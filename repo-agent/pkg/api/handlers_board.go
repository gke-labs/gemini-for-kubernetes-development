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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	yamlv3 "go.yaml.in/yaml/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/ghquota"
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
	attentionNeedsYou = "needs-you"
	attentionWorking  = "working"
	attentionWaiting  = "waiting"
	attentionDone     = "done"
)

// Factory CLI wire contract: sandbox labels and annotations the board reads
// to resolve work state and request re-runs.
const (
	labelFactoryPR      = "factory.gemini.google.com/pr"
	labelFactoryManaged = "factory.gemini.google.com/managed"
	labelRepoWatch      = "review.gemini.google.com/repowatch"
	annoTaskState       = "sandbox.gemini.google.com/last-task-state"
	annoCompletionTime  = "sandbox.gemini.google.com/completion-time"
	annoLastTaskType    = "sandbox.gemini.google.com/last-task-type"
	// annoBoard is the board whose controller stored the sandbox's draft.
	annoBoard = "board.gemini.google.com/board"
)

// nowRFC3339 timestamps re-run request annotations.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// reviewRequestFreshWindow bounds how long a bare review request counts as
// needs-you; older requests stay listed but out of UP NEXT.
const reviewRequestFreshWindow = 14 * 24 * time.Hour

// doneFreshWindow bounds how long finished work (a run done, a review
// submitted, a fix's PR open) counts as done, at the top of the feed;
// older, it rests with everything else.
const doneFreshWindow = 30 * 24 * time.Hour

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
// oauth_pat > pat, then factory-user's GITHUB_TOKEN) from their namespace,
// skipping any GitHub has just rejected: a stale manual PAT must not hide a
// working login token behind it.
func (s *Server) memberToken(ctx context.Context, namespace string) (string, error) {
	sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "github-pat", v1.GetOptions{})
	if err != nil {
		return "", err
	}
	var tokens []string
	for _, key := range []string{"manual_pat", "oauth_pat", "pat"} {
		if v, ok := sec.Data[key]; ok && len(v) > 0 {
			tokens = append(tokens, string(v))
		}
	}
	// Secret Manager reference mode: the controller (the only component
	// with GSM access) resolves the reference and materializes the token
	// into factory-user — read it from there.
	if fu, ferr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{}); ferr == nil {
		if v, ok := fu.Data["GITHUB_TOKEN"]; ok && len(v) > 0 {
			tokens = append(tokens, string(v))
		}
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("no github token in secret %s/github-pat", namespace)
	}
	for _, t := range tokens {
		if !tokenRejected(t) {
			return t, nil
		}
	}
	return "", errGitHubTokenRejected
}

// errGitHubUnauthorized is GitHub's 401: the token is expired or revoked.
var errGitHubUnauthorized = errors.New("401 Bad credentials")

// errGitHubTokenRejected is a member none of whose tokens GitHub accepts.
var errGitHubTokenRejected = errors.New("GitHub rejected your token (401 Bad credentials): update it in Settings")

// rejectedTokens are the tokens GitHub answered 401 for, by hash, until
// when they are skipped. A new token is a new hash, so one pasted into
// Settings is tried at once.
var rejectedTokens = struct {
	sync.Mutex
	until map[[32]byte]time.Time
}{until: map[[32]byte]time.Time{}}

const rejectedTokenTTL = 15 * time.Minute

func markTokenRejected(token string) {
	rejectedTokens.Lock()
	defer rejectedTokens.Unlock()
	rejectedTokens.until[sha256.Sum256([]byte(token))] = time.Now().Add(rejectedTokenTTL)
}

func tokenRejected(token string) bool {
	rejectedTokens.Lock()
	defer rejectedTokens.Unlock()
	return time.Now().Before(rejectedTokens.until[sha256.Sum256([]byte(token))])
}

// isUnauthorized is a GitHub answer of 401, from GraphQL or go-github.
func isUnauthorized(err error) bool {
	if errors.Is(err, errGitHubUnauthorized) {
		return true
	}
	var er *github.ErrorResponse
	return errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusUnauthorized
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
	var repository *github.Repository
	// A rejected token is skipped, and the check made again with the next.
	for range 2 {
		token, terr := s.memberToken(ctx, namespace)
		if terr != nil {
			return s.stalePermsOrNone(key)
		}
		repository, _, err = githubClientForToken(ctx, token).Repositories.Get(ctx, owner, repo)
		if !isUnauthorized(err) {
			break
		}
		markTokenRejected(token)
	}
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
		engine, _, _ := unstructured.NestedString(item.Object, "spec", "sandbox", "engine")
		boards = append(boards, models.Board{
			Name:       item.GetName(),
			Namespace:  item.GetNamespace(),
			RepoURL:    repoURL,
			NeedsHuman: int(needsHuman),
			Active:     int(active),
			Role:       role,
			Engine:     engineOrDefault(engine),
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
	// posting is whether any row's write was standing when the feed was
	// built (workFeedPostingFreshFor).
	posting bool
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
	// workFeedPostingFreshFor is how long a feed with a write standing
	// ("posting") stays fresh. The controller does the write, in another
	// process, and says so only on the sandbox: nothing here hears of it,
	// so a minute's freshness kept a done write reading "posting" for as
	// long. A write takes seconds. What changes meanwhile is Kubernetes
	// state (the apply Request, the run's applied stamp), so these
	// rebuilds read Kubernetes only, over the board's last GitHub
	// snapshot (boardSnapshotCache): no GraphQL while a write stands.
	workFeedPostingFreshFor = 5 * time.Second
)

// boardSnapshotCache keeps the GitHub half of each board's last feed
// build, per member (the snapshot carries the member's own reviews), for
// a rebuild while a write stands to reuse: it re-reads Kubernetes and
// takes GitHub from here while the snapshot is younger than
// workFeedFreshFor. Invalidation drops it with the feed entry: a click
// may itself have changed GitHub (an assignment, a promoted PR, a
// discarded pending review), so the build after a click reads GitHub
// once, and the posting rebuilds behind it reuse that.
var boardSnapshotCache = struct {
	sync.Mutex
	entries map[string]boardSnapshotEntry
}{entries: map[string]boardSnapshotEntry{}}

type boardSnapshotEntry struct {
	snap *boardSnapshot
	at   time.Time
}

func boardSnapshotKey(key, member string) string { return key + "/" + member }

// cachedBoardSnapshot is the board's snapshot for member when one younger
// than workFeedFreshFor is held.
func cachedBoardSnapshot(key, member string) (*boardSnapshot, bool) {
	boardSnapshotCache.Lock()
	defer boardSnapshotCache.Unlock()
	e, ok := boardSnapshotCache.entries[boardSnapshotKey(key, member)]
	if !ok || time.Since(e.at) > workFeedFreshFor {
		return nil, false
	}
	return e.snap, true
}

func putBoardSnapshot(key, member string, snap *boardSnapshot) {
	boardSnapshotCache.Lock()
	boardSnapshotCache.entries[boardSnapshotKey(key, member)] = boardSnapshotEntry{snap: snap, at: time.Now()}
	boardSnapshotCache.Unlock()
}

// workFeedPosting reports whether the board's held feed has a write
// standing: its rebuild may reuse the GitHub snapshot.
func workFeedPosting(key string) bool {
	workFeedCache.Lock()
	defer workFeedCache.Unlock()
	return workFeedCache.entries[key].posting
}

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
	freshFor := workFeedFreshFor
	if e.posting {
		freshFor = workFeedPostingFreshFor
	}
	if age <= freshFor {
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
	workFeedCache.entries[key] = workFeedEntry{items: items, at: time.Now(), posting: slices.ContainsFunc(items, func(item models.WorkItem) bool { return item.Posting })}
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
	key := namespace + "/" + boardName
	workFeedCache.Lock()
	delete(workFeedCache.entries, key)
	workFeedCache.Unlock()
	boardSnapshotCache.Lock()
	for k := range boardSnapshotCache.entries {
		if strings.HasPrefix(k, key+"/") {
			delete(boardSnapshotCache.entries, k)
		}
	}
	boardSnapshotCache.Unlock()
}

func (s *Server) refreshWorkFeed(ctx context.Context, key string, board *unstructured.Unstructured, member, namespace string) {
	defer func() {
		workFeedCache.Lock()
		delete(workFeedCache.refreshing, key)
		workFeedCache.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// A write standing: only Kubernetes moves, so GitHub comes from the
	// last snapshot (workFeedPostingFreshFor).
	items, err := s.buildBoardWork(ctx, board, member, namespace, workFeedPosting(key))
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

	s.stampViewed(ctx, board, time.Now())

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

	items, err := s.buildBoardWork(ctx, board, member, namespace, false)
	if isUnauthorized(err) {
		// That token is now skipped: build again with the next one.
		items, err = s.buildBoardWork(ctx, board, member, namespace, false)
	}
	if err != nil {
		// Nothing cached and no budget to build with: say so. An empty
		// feed would read as "no work", which is a lie the board cannot
		// afford to tell.
		if isUnauthorized(err) || errors.Is(err, errGitHubTokenRejected) {
			c.JSON(http.StatusFailedDependency, gin.H{"error": errGitHubTokenRejected.Error(), "details": err.Error()})
			return
		}
		if ghquota.IsRateLimited(err) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitHub rate limit reached", "details": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build board feed", "details": err.Error()})
		return
	}
	workFeedPut(key, items)
	c.JSON(http.StatusOK, items)
}

// AnnotationViewedAt is when someone last read a board's feed: the
// controller reconciles a board fast while it is viewed, and slowly
// otherwise (github-load.md). The UI polls the feed only while its tab is
// visible, active and not idle, so a fresh stamp means someone is looking.
const AnnotationViewedAt = "board.gemini.google.com/viewed-at"

// viewedStampEvery is how stale the stamp may get before a feed read
// stamps it again: often enough that the controller sees a viewer within
// its viewed window, rarely enough that a polling tab is one patch every
// two minutes.
const viewedStampEvery = 2 * time.Minute

// stampViewed merge-patches the board's viewed-at when it is missing or
// older than viewedStampEvery, read from the board already fetched. A
// merge patch, not an update: it cannot conflict with a settings save.
// Best-effort: a failed stamp only leaves the board on the slow cadence.
// The patch wakes the board's reconcile, so opening a board refreshes it.
func (s *Server) stampViewed(ctx context.Context, board *unstructured.Unstructured, now time.Time) {
	if at, err := time.Parse(time.RFC3339, board.GetAnnotations()[AnnotationViewedAt]); err == nil && now.Sub(at) < viewedStampEvery {
		return
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{AnnotationViewedAt: now.UTC().Format(time.RFC3339)}}})
	if err != nil {
		return
	}
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Patch(ctx, board.GetName(), types.MergePatchType, patch, v1.PatchOptions{}); err != nil {
		klog.FromContext(ctx).Info("stamping the board viewed failed", "board", board.GetName(), "err", err)
	}
}

// buildBoardWork assembles the feed universe from one GraphQL request
// (board_graphql.go), capped at 100 per surface and newest first: the
// board is a work queue, not an archive — on huge repos the tail belongs
// on GitHub search, not in every poll.
//
// With reuseSnapshot (a rebuild while a write stands), GitHub comes from
// the board's last snapshot when it is younger than workFeedFreshFor
// (boardSnapshotCache), and only Kubernetes is read.
func (s *Server) buildBoardWork(ctx context.Context, board *unstructured.Unstructured, member, namespace string, reuseSnapshot bool) ([]models.WorkItem, error) {
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
			s.mergeIssueRow(items, sandboxes, allSandboxes, issue, repo, member, viewLabels)
		}
	}
	// One request for the whole board. A hole in the universe would show
	// fewer rows than exist — "nothing needs you" is the one wrong answer
	// a work queue must not give — so anything short of a complete answer
	// fails the rebuild and the previous feed keeps standing.
	key := board.GetNamespace() + "/" + board.GetName()
	snap, cached := (*boardSnapshot)(nil), false
	if reuseSnapshot {
		snap, cached = cachedBoardSnapshot(key, member)
	}
	if !cached {
		snap, err = fetchBoardSnapshot(ctx, githubHTTPForToken(token), owner, repo, member)
		if err != nil {
			if ghquota.IsRateLimited(err) {
				return nil, fmt.Errorf("github budget spent while building the feed: %w", err)
			}
			if isUnauthorized(err) {
				markTokenRejected(token)
			}
			return nil, fmt.Errorf("failed to read the board from github: %w", err)
		}
		putBoardSnapshot(key, member, snap)
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
		s.mergeIssueRow(items, sandboxes, allSandboxes, issue, repo, member, viewLabels)
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
		s.mergePRRow(items, sandboxes, pr, repo, member, st.pending, st.reviewed, viewLabels)
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
				s.mergePRRow(items, sandboxes, pr, repo, member, false, false, viewLabels)
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

	// Every row by the same rules, whichever recipes ran on it: its runs,
	// its launch buttons, the clicks not started yet, and its attention.
	catalog, err := boardRecipes(board)
	if err != nil {
		log.Error(err, "invalid recipes on board", "board", board.GetName())
	}
	addRunSessions(items, allSandboxes, repo, catalog)
	running := 0
	for _, sb := range sandboxes {
		if replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas"); err == nil && found && replicas > 0 {
			running++
		}
	}
	// Clicks beyond the launch limit are honestly queued, not starting:
	// the controller defers them until a slot frees.
	atCapacity := int64(running) >= boardLimit(board, "maxActive", 5)
	all := s.allBoardRequests(ctx, board)
	requests := activeRequests(all)
	markFailedRecipes(items, catalog, all)
	applyRowRules(items, catalog, requests, atCapacity, time.Now())
	markAutos(items, requests, namespace)
	s.markApplies(ctx, board, items)

	work := make([]models.WorkItem, 0, len(items))
	for _, item := range items {
		work = append(work, *item)
	}
	sortWork(work)
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

// reviewSandbox is the PR's review sandbox in namespace
// (factorycli.ReviewSandboxName), or nil.
func (s *Server) reviewSandbox(ctx context.Context, namespace, owner, repo string, pr int) *unstructured.Unstructured {
	sandboxes, err := s.boardSandboxes(ctx, namespace, owner, repo)
	if err != nil {
		return nil
	}
	return sandboxes[factorycli.ReviewSandboxName(repo, pr)]
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

func workSandbox(sb *unstructured.Unstructured) *models.WorkSandbox {
	if sb == nil {
		return nil
	}
	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	engine := sandboxEngine(sb.GetAnnotations(), "gemini") // pre-stamp sandboxes only ever ran gemini
	return &models.WorkSandbox{
		Name:      sb.GetName(),
		Replicas:  fmt.Sprintf("%d", replicas),
		TaskState: sb.GetAnnotations()[annoTaskState],
		Engine:    engine,
	}
}

// addRunSessions gives every item the runs recorded on its sandboxes: an
// issue's own sandbox's on the issue, and on the PR its fix opened (the
// sandbox's URL turns to it); a PR's (by its URL, or a review sandbox's
// name) on the PR.
func addRunSessions(items map[string]*models.WorkItem, sandboxes []*unstructured.Unstructured, repo string, catalog []boardv1alpha1.BoardRecipe) {
	// An issue folded into the PR that fixes it has no row of its own: its
	// runs are the PR row's.
	folded := map[string]*models.WorkItem{}
	for _, item := range items {
		for _, n := range item.Fixes {
			folded["issue-"+strconv.Itoa(n)] = item
		}
	}
	for _, sb := range sandboxes {
		sessions := recordedRunSessions(sb, catalog)
		seen := map[*models.WorkItem]bool{}
		for _, key := range sandboxItems(sb, repo) {
			item := items[key]
			if item == nil {
				item = folded[key]
			}
			if item != nil && !seen[item] {
				seen[item] = true
				item.Sessions = append(item.Sessions, sessions...)
			}
		}
	}
	for _, item := range items {
		slices.SortStableFunc(item.Sessions, func(a, b models.RunSession) int {
			return strings.Compare(b.StartedAt, a.StartedAt)
		})
	}
}

// recordedRunSessions are the runs recorded on sb, newest first, each
// labelled from catalog.
func recordedRunSessions(sb *unstructured.Unstructured, catalog []boardv1alpha1.BoardRecipe) []models.RunSession {
	var sessions []models.RunSession
	annotations := sb.GetAnnotations()
	for _, run := range factorycli.Runs(annotations) {
		session := models.RunSession{
			Recipe:    run.Recipe,
			Label:     recipeLabel(catalog, run.Recipe),
			Session:   recipeSessionTag(catalog, run.Recipe),
			Sandbox:   sb.GetName(),
			Engine:    sandboxEngine(annotations, "gemini"),
			Task:      run.SessionOf(),
			Run:       run.Key,
			Kind:      run.Kind,
			Source:    run.Source,
			State:     run.State,
			StartedAt: run.StartedAt.UTC().Format(time.RFC3339),
			Output:    annotations[factorycli.OutputAnnotation(run.Key)] != "",
			Applied:   factorycli.Applied(annotations, factorycli.AppliedAnnotation(run.Key)),
		}
		if run.EndedAt != nil {
			session.EndedAt = run.EndedAt.UTC().Format(time.RFC3339)
		}
		session.Status = runStatus(session)
		if doc := annotations[factorycli.OutputAnnotation(run.Key)]; doc != "" {
			session.Actions = runActions(run.Kind, doc)
			session.Preview = factorycli.Preview(run.Kind, doc)
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// runActions are the actions a run's output offers but the draft verbs
// (models.RunSession.Actions).
func runActions(kind, doc string) []models.RunAction {
	var out []models.RunAction
	for _, a := range factorycli.OfferedActions(kind, doc) {
		switch a.Verb {
		case "edit", "reject":
			continue
		}
		out = append(out, models.RunAction{Verb: a.Verb, Label: a.Label})
	}
	return out
}

// runStatus is a run's status on the row (models.RunSession.Status), as
// the board's counts see it (factorycli.RunStatus).
func runStatus(s models.RunSession) string {
	return factorycli.RunStatus(s.State, s.Output, s.Applied)
}

// sandboxItems are the items keys of the issue and the PR sb is for.
func sandboxItems(sb *unstructured.Unstructured, repo string) []string {
	var keys []string
	if n, ok := factorycli.IssueOf(sb, repo); ok {
		keys = append(keys, "issue-"+strconv.Itoa(n))
	}
	if n, ok := factorycli.ReviewPROf(sb, repo); ok {
		return append(keys, "pr-"+strconv.Itoa(n))
	}
	if _, rest, ok := strings.Cut(sb.GetAnnotations()["htmlURL"], "/pull/"); ok {
		if n, err := strconv.Atoi(strings.TrimRight(rest, "/")); err == nil && n > 0 {
			keys = append(keys, "pr-"+strconv.Itoa(n))
		}
	}
	return keys
}

// headOnMemberFork is whether pr's head is a branch on member's fork of
// the repository: the only place a recipe pushes to (fix's push step
// refuses any other).
func headOnMemberFork(pr *github.PullRequest, member string) bool {
	head := pr.GetHead().GetRepo()
	return head != nil && head.GetFork() &&
		strings.EqualFold(head.GetOwner().GetLogin(), member) &&
		!strings.EqualFold(head.GetFullName(), pr.GetBase().GetRepo().GetFullName())
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

func (s *Server) mergeIssueRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, allSandboxes []*unstructured.Unstructured, issue *github.Issue, repo, member string, viewLabels []string) {
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
	prURL := ""
	taskType := ""
	if sb != nil {
		annotations := sb.GetAnnotations()
		taskType = annotations[annoLastTaskType]
		if u := annotations["htmlURL"]; strings.Contains(u, "/pull/") {
			prURL = u
		}
	}
	triageDraft := ""
	triageState := ""
	triageSB := factorycli.TriageSandbox(slices.Values(allSandboxes), repo, issue.GetNumber())
	if len(viewLabels) > 0 && !hasAnyLabel(issue.Labels, viewLabels) && sb == nil && triageSB == nil {
		return
	}
	if triageSB != nil {
		triageDraft = factorycli.TriageDraft(triageSB)
		triageState = factorycli.TriageState(triageSB)
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
		Type:      "issue",
		Group:     "issues",
		Number:    issue.GetNumber(),
		Author:    issue.GetUser().GetLogin(),
		Title:     issue.GetTitle(),
		HTMLURL:   issue.GetHTMLURL(),
		Assignee:  claimedBy,
		PRURL:     prURL,
		Labels:    labels,
		Sandbox:   workSandbox(sb),
		UpdatedAt: issue.GetUpdatedAt().UTC().Format(time.RFC3339),
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
// friendlyRunError rewrites known run failures into an instruction the
// member can act on; anything unrecognized passes through verbatim.
func friendlyRunError(msg string) string {
	switch {
	case strings.Contains(msg, "OAuth App access restrictions"):
		return "This organization blocks OAuth app tokens. Add a personal access token (manual_pat key in your github-pat secret), then run it again. Agent said: " + msg
	case strings.Contains(msg, "Bad credentials"):
		return "GitHub rejected your token — sign in again or add a personal access token (manual_pat), then run it again. Agent said: " + msg
	default:
		return msg
	}
}

func (s *Server) mergePRRow(items map[string]*models.WorkItem, sandboxes map[string]*unstructured.Unstructured, pr *github.PullRequest, repo, member string, pendingOnGitHub, reviewedOnGitHub bool, viewLabels []string) {
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

	if len(viewLabels) > 0 && !hasAnyLabel(pr.Labels, viewLabels) && sb == nil &&
		sandboxes[factorycli.ReviewSandboxName(repo, pr.GetNumber())] == nil {
		return
	}

	reviewRequested := false
	for _, reviewer := range pr.RequestedReviewers {
		if strings.EqualFold(reviewer.GetLogin(), member) {
			reviewRequested = true
		}
	}
	authored := strings.EqualFold(pr.GetUser().GetLogin(), member)

	// The board's review runs in a sandbox of its own (the review
	// recipe's); the PR's sandbox carries the fix and its follow-ups.
	rsb := sandboxes[factorycli.ReviewSandboxName(repo, pr.GetNumber())]
	reviewTaskState := ""
	if rsb != nil {
		reviewTaskState = rsb.GetAnnotations()[annoTaskState]
	}
	// The pending review is the member's to finalize on GitHub, as GitHub
	// says (the board query reads the member's review states). A
	// submitted one is a receipt until someone asks for another review
	// (submitting clears the request; a re-request re-adds it).
	reviewPending := pendingOnGitHub && reviewTaskState != "Running"
	reviewed := reviewedOnGitHub && !reviewRequested && !reviewPending

	// Every pull request is a row of the PRs group; what the member may
	// do on it follows from whether it is theirs (mine) and whether they
	// can push to it (myPR).
	myPR := authored && headOnMemberFork(pr, member)

	var prLabels []string
	for _, l := range pr.Labels {
		prLabels = append(prLabels, l.GetName())
	}

	key := fmt.Sprintf("pr-%d", pr.GetNumber())
	items[key] = &models.WorkItem{
		Type:            "pr",
		Group:           "prs",
		Mine:            authored,
		MyPR:            myPR,
		Number:          pr.GetNumber(),
		Author:          pr.GetUser().GetLogin(),
		Title:           pr.GetTitle(),
		Labels:          prLabels,
		ReviewRequested: reviewRequested,
		HTMLURL:         pr.GetHTMLURL(),
		ReviewPending:   reviewPending,
		Reviewed:        reviewed,
		PRURL:           pr.GetHTMLURL(),
		DraftPR:         pr.GetDraft(),
		Fixes:           closingRefs(pr.GetBody()),
		Sandbox:         workSandbox(sb),
		UpdatedAt:       pr.GetUpdatedAt().UTC().Format(time.RFC3339),
	}
	if sb == nil {
		items[key].Sandbox = workSandbox(rsb)
	}
}

// launchRecipe handles any recipe's launch button on an issue or PR row:
// the recipe Request, with the inputs the member gave. The recipe must be
// one the board's factory lists (status.recipes) and start on the row.
func (s *Server) launchRecipe(item string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload struct {
			Inputs map[string]string `json:"inputs"`
		}
		if c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&payload); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body", "details": err.Error()})
				return
			}
		}
		s.kickoff(c, item, c.Param("recipe"), payload.Inputs)
	}
}

// getBoardRecipes lists the recipes the board's rows offer, in its order:
// what the controller's factory runs (factory recipe list), published on
// the board's status.
func (s *Server) getBoardRecipes(c *gin.Context) {
	ctx := c.Request.Context()
	board, _, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	recipes, err := boardRecipes(board)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid recipes on board", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, recipes)
}

// boardRecipes is the catalog on board's status; empty until the
// controller has read it.
func boardRecipes(board *unstructured.Unstructured) ([]boardv1alpha1.BoardRecipe, error) {
	raw, found, err := unstructured.NestedSlice(board.Object, "status", "recipes")
	if err != nil || !found {
		return []boardv1alpha1.BoardRecipe{}, err
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	recipes := []boardv1alpha1.BoardRecipe{}
	return recipes, json.Unmarshal(b, &recipes)
}

// boardStarts reports whether the board starts recipe on item. A PR may be
// the member's: whether it is, factory checks when the run starts.
func (s *Server) boardStarts(board *unstructured.Unstructured, recipe, item string) (bool, error) {
	recipes, err := boardRecipes(board)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(recipes, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == recipe })
	return i >= 0 && recipeRunsOn(recipes[i], item, true), nil
}

// recipeRunsOn reports whether rec runs on a row of item (no on: runs
// anywhere); myPR is a PR the member authored from their fork, where a
// recipe on [my-pr] runs as well as one on [pr] (factory's on: rule).
func recipeRunsOn(rec boardv1alpha1.BoardRecipe, item string, myPR bool) bool {
	return len(rec.On) == 0 || slices.Contains(rec.On, item) ||
		(item == "pr" && myPR && slices.Contains(rec.On, "my-pr"))
}

func (s *Server) kickoff(c *gin.Context, item, recipe string, inputs map[string]string) {
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
	if ok, err := s.boardStarts(board, recipe, item); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid recipes on board", "details": err.Error()})
		return
	} else if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the board has no recipe %q to start on this %s", recipe, item)})
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
	// What the recipe makes says what it claims: a Change on an issue
	// assigns the clicker, a Review requests them as reviewer. The trigger
	// label is deliberately NOT written: a click is a one-time consent,
	// while a label is a standing trigger that would relaunch the item
	// forever after cleanup.
	kind := ""
	if catalog, err := boardRecipes(board); err == nil {
		kind = recipeKind(catalog, recipe)
	}
	assign, requestReview := item == "issue" && kind == "Change", kind == "Review"
	if token, err := s.memberToken(ctx, namespace); err != nil {
		log.Info("member token unavailable; request-only kickoff", "err", err)
	} else if assign || requestReview {
		gh := githubClientForToken(ctx, token)
		if assign {
			if _, _, err := gh.Issues.AddAssignees(ctx, owner, repo, number, []string{member}); err != nil {
				log.Info("best-effort assignment failed", "issue", number, "err", err)
			}
		} else {
			if _, _, err := gh.PullRequests.RequestReviewers(ctx, owner, repo, number, github.ReviewersRequest{Reviewers: []string{member}}); err != nil {
				log.Info("best-effort self review-request failed", "pr", number, "err", err)
			}
		}
	}

	// Authoritative record of the click; the controller serves it and
	// writes back what came of it.
	if _, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:   boardv1alpha1.VerbRecipe,
		Recipe: recipe,
		Item:   item,
		Member: member,
		Number: number,
		Inputs: inputs,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record request", "details": err.Error()})
		return
	}
	invalidateWorkFeed(board.GetNamespace(), board.GetName())
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
// is discarded) and clears the board's memory of the run, its stored
// Review included.
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

	// Clear the sandbox's stored Review so the row returns to its plain
	// stage. Its run stays recorded, so automation does not review the PR
	// again.
	for _, ns := range []string{s.Auth.GetNamespaceFromContext(c), board.GetNamespace()} {
		sb := s.reviewSandbox(ctx, ns, owner, repo, number)
		if sb == nil {
			continue
		}
		_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.OutputAnnotation(factorycli.AnnotationReviewRun), "")
		_ = s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.AppliedAnnotation(factorycli.AnnotationReviewRun), "")
		_ = s.K8sManager.ScaledownSandboxByName(ctx, ns, sb.GetName())
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
// gear panel. Access and prepIdentity stay kubectl-only (owner-level
// governance); of the sandbox, the image does.
type boardSpecView struct {
	Editable   bool     `json:"editable"`
	ViewLabels []string `json:"viewLabels"`
	// AutoRules are the board's automation rules (spec.auto.rules, or
	// what the deprecated settings mean until the controller converts
	// them), and AutoRuleErrors the ones it skips and why (read-only).
	AutoRules      []boardv1alpha1.AutoRule `json:"autoRules"`
	AutoRuleErrors []string                 `json:"autoRuleErrors,omitempty"`
	// Recipes are the catalog a rule picks from (read-only).
	Recipes           []specRecipe `json:"recipes"`
	AutoLabels        []string     `json:"autoLabels"`
	AutoExcludeLabels []string     `json:"autoExcludeLabels"`
	RecencyDays       int64        `json:"recencyDays"`
	MaxActive         int64        `json:"maxActive"`
	IdleMinutes       int64        `json:"idleMinutes"`
	Engine            string       `json:"engine"`
	AutoIterate       bool         `json:"autoIterate"`
	Disclose          bool         `json:"disclose"`
	// The size the board's sandboxes are made with (spec.sandbox); empty
	// is factory's default.
	DiskSize         string `json:"diskSize"`
	StorageClass     string `json:"storageClass"`
	EphemeralStorage string `json:"ephemeralStorage"`
	CPURequest       string `json:"cpuRequest"`
	CPULimit         string `json:"cpuLimit"`
	MemoryRequest    string `json:"memoryRequest"`
	MemoryLimit      string `json:"memoryLimit"`
}

// sandboxSizeFields are the spec.sandbox fields of a board's sandbox size,
// each with where it is in the spec view.
func sandboxSizeFields(v *boardSpecView) []struct {
	key   string
	value *string
} {
	return []struct {
		key   string
		value *string
	}{
		{"diskSize", &v.DiskSize},
		{"storageClass", &v.StorageClass},
		{"ephemeralStorage", &v.EphemeralStorage},
		{"cpuRequest", &v.CPURequest},
		{"cpuLimit", &v.CPULimit},
		{"memoryRequest", &v.MemoryRequest},
		{"memoryLimit", &v.MemoryLimit},
	}
}

// checkSandboxSize trims the view's sandbox size and refuses what a pod
// would: a size that is no quantity, a storage class that is no name, a
// request above its limit. It says so at save, not when a sandbox pod
// fails to be made.
func checkSandboxSize(v *boardSpecView) error {
	for _, f := range sandboxSizeFields(v) {
		*f.value = strings.TrimSpace(*f.value)
		if *f.value == "" {
			continue
		}
		if f.key == "storageClass" {
			if errs := validation.IsDNS1123Subdomain(*f.value); len(errs) > 0 {
				return fmt.Errorf("storage class %q: %s", *f.value, strings.Join(errs, "; "))
			}
			continue
		}
		if _, err := resource.ParseQuantity(*f.value); err != nil {
			return fmt.Errorf("%s %q is not a size: %v", f.key, *f.value, err)
		}
	}
	for _, pair := range [][2]string{{v.CPURequest, v.CPULimit}, {v.MemoryRequest, v.MemoryLimit}} {
		if pair[0] == "" || pair[1] == "" {
			continue
		}
		req, limit := resource.MustParse(pair[0]), resource.MustParse(pair[1])
		if req.Cmp(limit) > 0 {
			return fmt.Errorf("request %s is more than its limit %s", pair[0], pair[1])
		}
	}
	return nil
}

// specRecipe is a catalog recipe as the gear's rules offer it: what it
// runs on, and what it applies when it finishes.
type specRecipe struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	On       []string `json:"on,omitempty"`
	OnFinish string   `json:"onFinish,omitempty"`
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
	view := boardSpecView{Editable: true, AutoRules: []boardv1alpha1.AutoRule{}, RecencyDays: 7}
	view.ViewLabels, _, _ = unstructured.NestedStringSlice(board.Object, "spec", "view", "labels")
	var typed boardv1alpha1.RepoBoard
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(board.Object, &typed); err == nil {
		if rules := typed.Spec.Auto.EffectiveRules(); len(rules) > 0 {
			view.AutoRules = rules
		}
		view.AutoRuleErrors = typed.Status.AutoRuleErrors
	}
	view.Recipes = []specRecipe{}
	if catalog, err := boardRecipes(board); err == nil {
		for _, rec := range catalog {
			view.Recipes = append(view.Recipes, specRecipe{Name: rec.Name, Label: rec.Label, On: rec.On, OnFinish: rec.OnFinish})
		}
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
	view.Disclose, _, _ = unstructured.NestedBool(board.Object, "spec", "policy", "disclose")
	for _, f := range sandboxSizeFields(&view) {
		*f.value, _, _ = unstructured.NestedString(board.Object, "spec", "sandbox", f.key)
	}
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
	var target *unstructured.Unstructured
	set := func(value interface{}, fields ...string) bool {
		if err := unstructured.SetNestedField(target.Object, value, fields...); err != nil {
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
	recency := payload.RecencyDays
	if recency <= 0 {
		recency = 7
	}
	idleMinutes := payload.IdleMinutes
	if idleMinutes <= 0 {
		idleMinutes = 60
	}
	rules, err := autoRulesField(payload.AutoRules)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := checkSandboxSize(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// An empty size is no field, so factory's default (the CRD's, for the
	// disk) applies.
	setSize := func() bool {
		for _, f := range sandboxSizeFields(&payload) {
			if *f.value == "" {
				unstructured.RemoveNestedField(target.Object, "spec", "sandbox", f.key)
			} else if !set(*f.value, "spec", "sandbox", f.key) {
				return false
			}
		}
		return true
	}
	// The rules replace the deprecated settings, which are set off.
	apply := func(b *unstructured.Unstructured) bool {
		target = b
		return set(cleanLabels(payload.ViewLabels), "spec", "view", "labels") &&
			set(rules, "spec", "auto", "rules") &&
			set("off", "spec", "auto", "triage") &&
			set("off", "spec", "auto", "fix") &&
			set("off", "spec", "auto", "review") &&
			set(cleanLabels(payload.AutoLabels), "spec", "auto", "labels") &&
			set(cleanLabels(payload.AutoExcludeLabels), "spec", "auto", "excludeLabels") &&
			set(recency, "spec", "auto", "recencyDays") &&
			set(payload.MaxActive, "spec", "limits", "maxActive") &&
			set(idleMinutes, "spec", "sandbox", "idleMinutes") &&
			set(engineOrDefault(payload.Engine), "spec", "sandbox", "engine") &&
			set(payload.AutoIterate, "spec", "policy", "autoIterate") &&
			set(payload.Disclose, "spec", "policy", "disclose") &&
			setSize()
	}
	if !apply(board) {
		return
	}
	boards := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace())
	_, err = boards.Update(ctx, board, v1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		// The board moved since it was read — the controller's status, or
		// a feed read stamping it viewed: save onto the fresh one, once.
		if board, err = s.getBoard(ctx, board.GetNamespace(), board.GetName()); err == nil {
			if !apply(board) {
				return
			}
			_, err = boards.Update(ctx, board, v1.UpdateOptions{})
		}
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save board settings", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// autoRulesField is rules as the board's unstructured spec holds them,
// each checked for what the CRD would refuse: a recipe, an item type of
// issue or pr, no negative cap. Whether the catalog runs it is the
// controller's to say (status.autoRuleErrors).
func autoRulesField(rules []boardv1alpha1.AutoRule) ([]interface{}, error) {
	out := make([]interface{}, 0, len(rules))
	for i, rule := range rules {
		rule.Recipe = strings.TrimSpace(rule.Recipe)
		switch {
		case rule.Recipe == "":
			return nil, fmt.Errorf("rule %d names no recipe", i+1)
		case rule.On != "issue" && rule.On != "pr":
			return nil, fmt.Errorf("rule %d (%s) runs on %q, not issue or pr", i+1, rule.Recipe, rule.On)
		case rule.MaxPerDay < 0:
			return nil, fmt.Errorf("rule %d (%s) has a negative daily cap", i+1, rule.Recipe)
		}
		var labels []string
		for _, l := range rule.When.Labels {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
		rule.When.Labels = labels
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&rule)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// autoBoardPR turns a PR of the member's auto on or off: {"on": bool}.
// On files their watch Request for it, and the controller keeps a
// factory pr watch running; off deletes it, and the watch stops.
func (s *Server) autoBoardPR(c *gin.Context) {
	ctx, board, _, _, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.On == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "on must be true or false"})
		return
	}
	member := s.Auth.GetNamespaceFromContext(c)
	spec := boardv1alpha1.RequestSpec{Board: board.GetName(), Verb: boardv1alpha1.VerbWatch, Item: "pr", Number: number, Member: member}
	if *req.On {
		if _, err := s.fileRequest(ctx, board, spec); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to turn auto on", "details": err.Error()})
			return
		}
		c.Status(http.StatusOK)
		return
	}
	for {
		existing, err := s.findActiveRequest(ctx, board.GetNamespace(), spec)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to turn auto off", "details": err.Error()})
			return
		}
		if existing == nil {
			break
		}
		if err := s.K8sManager.Client.Resource(requestGVR).Namespace(board.GetNamespace()).
			Delete(ctx, existing.Name, v1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to turn auto off", "details": err.Error()})
			return
		}
	}
	c.Status(http.StatusOK)
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

// editDraft stores draft, as a member edited it, as the spec of the task
// output stored on sb under key.
func (s *Server) editDraft(ctx context.Context, ns string, sb *unstructured.Unstructured, kind, key, draft string) error {
	doc, err := factorycli.WithDraft(kind, sb.GetAnnotations()[key], draft)
	if err != nil {
		return err
	}
	return s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), key, doc)
}

// markApplied stamps action as applied to the task output stored on sb,
// in its run's applied annotation (key).
func (s *Server) markApplied(ctx context.Context, ns string, sb *unstructured.Unstructured, key, action string) error {
	annotations := maps.Clone(sb.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	factorycli.MarkApplied(annotations, key, action, time.Now())
	return s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), key, annotations[key])
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
