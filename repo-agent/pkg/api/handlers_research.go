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

// The research conversation proxy: everything that happens after the
// sandbox exists.
//
// Creating one goes through the board's mailbox, because only the
// controller's image carries the factory CLI (see
// handlers_board_research.go). Talking to one does not: acpd is plain
// HTTP on the sandbox pod, so this process dials it directly and the
// board is never involved again. That is the same split the chat
// terminal uses — create elsewhere, attach from here — with a lighter
// attach: an HTTP request instead of pods/exec.
//
// Routes are keyed by session id alone, with no board and no sandbox
// name in the path. The sandbox carries the session's short id as a
// label precisely so it can be found that way, and the member's
// namespace comes from their session, so a caller cannot reach into
// another member's conversation by naming it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// Labels and annotations factory stamps on a research sandbox. Mirrored
// rather than imported, like every other factory name in repo-agent:
// the two meet over HTTP and the CLI, never as one Go program.
const (
	// researchTypeLabel marks the sandbox kind. Research sandboxes are
	// deliberately their own type so the board's task-slot accounting
	// ignores them.
	researchTypeLabel = "sandbox.gemini.google.com/type"
	researchType      = "research"
	// researchSessionLabel holds the session's SHORT id — a label value
	// cannot hold a UUID's full charset budget alongside the rest, and
	// the short form is what the sandbox name is built from anyway.
	researchSessionLabel = "sandbox.gemini.google.com/research-session"
	// researchSessionIDAnnotation holds the full session id. It is the
	// authoritative match: the label is a digest prefix, so it narrows
	// the search but does not prove identity.
	researchSessionIDAnnotation = "sandbox.gemini.google.com/research-session-id"
)

// engineAPIKeySecretKey is the field of the member's factory-user Secret
// that holds the engine credential.
//
// Only gemini is registered in acpd, deliberately, so there is no engine
// switch here: an unsupported engine should fail at session creation
// rather than be quietly papered over with the wrong key.
const engineAPIKeySecretKey = "GEMINI_API_KEY"

// safeResearchSessionID mirrors the controller's claim-key check. The id
// reaches acpd inside a URL path and names a Kubernetes label value, so
// it is validated before either.
var safeResearchSessionID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// acpdClientForPodIP is the dial seam. Production dials the pod; tests
// point it at an httptest server, which is the only way to exercise
// these handlers without a cluster.
var acpdClientForPodIP = func(ip string) *acpd.Client { return acpd.NewForPodIP(ip) }

// researchSandboxView is one session as the board sees it: what the
// sandbox object says, with no call to acpd.
//
// Deliberately answerable from the Kubernetes API alone, because the
// list has to render for paused and still-booting sessions too — the
// ones acpd cannot answer for.
type researchSandboxView struct {
	SessionID string `json:"sessionId"`
	Sandbox   string `json:"sandbox"`
	Namespace string `json:"namespace"`
	Repo      string `json:"repo"`
	HTMLURL   string `json:"htmlUrl,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	// Title is what the session is called: the canned exploration's
	// name, the topic it was started with, or the first thing the member
	// said. Empty until one of those has happened.
	Title string `json:"title,omitempty"`
	// Opening reports that a canned first turn is still owed — the
	// controller sends it once the pod is up, which is a minute or two
	// after the row first appears.
	Opening bool `json:"opening,omitempty"`
	// OpeningError is why an owed first turn was given up on.
	OpeningError string `json:"openingError,omitempty"`
	// Requested marks a session that has been asked for but has no
	// sandbox yet: a standing claim on the board, not an object.
	Requested bool `json:"requested,omitempty"`
	// Paused is a sandbox scaled to zero: the conversation's transcript
	// survives on the PVC but the engine is gone, so resuming means a
	// fresh session over the same history.
	Paused bool `json:"paused"`
	// PodIP is empty until the pod is running. Its presence is what
	// "reachable" means for every other call here.
	PodIP string `json:"-"`
}

// cwd is the checkout the conversation is about, matching what `factory
// research start` cloned.
func (v researchSandboxView) cwd() string {
	if v.Repo == "" {
		return ""
	}
	return "/workspaces/" + v.Repo
}

// researchViewFromSandbox reads one sandbox object, or reports false if
// it is not a research sandbox with a session id on it.
func researchViewFromSandbox(sb *unstructured.Unstructured) (researchSandboxView, bool) {
	annotations := sb.GetAnnotations()
	sessionID := annotations[researchSessionIDAnnotation]
	if sessionID == "" {
		// A research sandbox with no session id cannot be addressed:
		// every route here starts from the id. Skipping it keeps a
		// hand-made or half-written object out of the list rather than
		// showing a row nothing can open.
		return researchSandboxView{}, false
	}
	view := researchSandboxView{
		SessionID:    sessionID,
		Sandbox:      sb.GetName(),
		Namespace:    sb.GetNamespace(),
		Repo:         annotations["repo"],
		HTMLURL:      annotations["htmlURL"],
		Title:        annotations[research.TitleAnnotation],
		Opening:      annotations[research.KickoffAnnotation] != "",
		OpeningError: annotations[research.KickoffErrorAnnotation],
	}
	if ts := sb.GetCreationTimestamp(); !ts.IsZero() {
		view.CreatedAt = ts.UTC().Format(time.RFC3339)
	}
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		view.Paused = true
	}
	return view, true
}

// getResearchSessions lists the member's research sessions: every
// research sandbox, plus the ones that have been asked for and do not
// exist yet.
//
// The pending rows matter more here than in any other list. A sandbox
// is minutes away — image pull, PVC, clone — and a member who clicked
// "overview" and saw nothing appear would reasonably click again, which
// costs another sandbox and another engine.
func (s *Server) getResearchSessions(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)

	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: researchTypeLabel + "=" + researchType,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list research sandboxes", "details": err.Error()})
		return
	}

	views := []researchSandboxView{}
	exists := map[string]bool{}
	for i := range list.Items {
		if view, ok := researchViewFromSandbox(&list.Items[i]); ok {
			views = append(views, view)
			exists[view.SessionID] = true
		}
	}
	views = append(views, s.requestedResearchSessions(ctx, namespace, exists)...)
	// Newest first: a session list is read from the top, and the one you
	// just started is the one you want.
	sort.Slice(views, func(i, j int) bool {
		if views[i].CreatedAt != views[j].CreatedAt {
			return views[i].CreatedAt > views[j].CreatedAt
		}
		return views[i].Sandbox < views[j].Sandbox
	})
	c.JSON(http.StatusOK, gin.H{"sessions": views})
}

// requestedResearchSessions reads the standing research claims off the
// member's boards and renders them as rows.
//
// Read from the boards rather than remembered here because the API
// server is stateless and replicated: the claim on the board is the
// only record of a click between the POST and the sandbox appearing.
// A board that cannot be read is skipped rather than failing the list —
// the sessions that DO exist are the more important half of the answer.
func (s *Server) requestedResearchSessions(ctx context.Context, namespace string, exists map[string]bool) []researchSandboxView {
	boards, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		klog.V(2).Infof("research: cannot list boards for pending sessions in %s: %v", namespace, err)
		return nil
	}
	var out []researchSandboxView
	for i := range boards.Items {
		board := &boards.Items[i]
		raw := board.GetAnnotations()[annoBoardRequests]
		if raw == "" {
			continue
		}
		requests := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &requests); err != nil {
			continue
		}
		repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
		_, repo, _ := parseRepoURL(repoURL)
		for key, value := range requests {
			if !strings.HasPrefix(key, "research-") {
				continue
			}
			sessionID := strings.TrimPrefix(key, "research-")
			// A claim whose sandbox has arrived is about to be trimmed
			// by the controller; showing both would double the row.
			if exists[sessionID] || !safeResearchSessionID.MatchString(sessionID) {
				continue
			}
			claim, ok := research.DecodeClaim(value)
			if !ok || claim.Member != namespace {
				continue
			}
			out = append(out, researchSandboxView{
				SessionID: sessionID,
				Sandbox:   factorycli.ResearchSandboxName(repo, sessionID),
				Namespace: namespace,
				Repo:      repo,
				HTMLURL:   repoURL,
				CreatedAt: claim.At.UTC().Format(time.RFC3339),
				Title:     claim.Kickoff.ResolvedTitle(),
				Opening:   claim.Kickoff != research.Kickoff{},
				Requested: true,
			})
		}
	}
	return out
}

// findResearchSandbox locates the sandbox hosting one session.
//
// The label narrows the list server-side; the annotation then confirms
// it. Both are needed: the label holds only eight hex characters of a
// digest, so it is a filter, not proof.
func (s *Server) findResearchSandbox(ctx context.Context, namespace, sessionID string) (researchSandboxView, bool, error) {
	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: researchSessionLabel + "=" + factorycli.ResearchShortID(sessionID),
	})
	if err != nil {
		return researchSandboxView{}, false, err
	}
	for i := range list.Items {
		view, ok := researchViewFromSandbox(&list.Items[i])
		if ok && view.SessionID == sessionID {
			return view, true, nil
		}
	}
	return researchSandboxView{}, false, nil
}

// researchPodIP returns the sandbox pod's IP, or "" when there is no
// running pod to dial yet.
func (s *Server) researchPodIP(ctx context.Context, namespace, sandboxName string) (string, error) {
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox=" + sandboxName,
	})
	if err != nil {
		return "", err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Only a running pod with an IP: an address taken from a pending
		// or terminating pod fails on connect, which looks like acpd
		// being broken rather than the sandbox not being up.
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod.Status.PodIP, nil
		}
	}
	return "", nil
}

// researchConn is a resolved, reachable conversation.
type researchConn struct {
	view   researchSandboxView
	client *acpd.Client
}

// resolveResearch turns a session id from the URL into something to talk
// to, or an HTTP status and a message saying why not.
//
// The status codes are the contract with the UI: 404 means the session
// does not exist, 409 means it exists but is not up yet — the first is
// permanent, the second is worth retrying.
func (s *Server) resolveResearch(c *gin.Context) (*researchConn, bool) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return nil, false
	}

	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return nil, false
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return nil, false
	}
	if view.Paused {
		c.JSON(http.StatusConflict, gin.H{"error": "research session is paused", "sandbox": view.Sandbox, "paused": true})
		return nil, false
	}

	podIP, err := s.researchPodIP(ctx, namespace, view.Sandbox)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to find the session's pod", "details": err.Error()})
		return nil, false
	}
	if podIP == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "research sandbox is still starting", "sandbox": view.Sandbox, "starting": true})
		return nil, false
	}
	view.PodIP = podIP

	return &researchConn{view: view, client: acpdClientForPodIP(podIP)}, true
}

// engineAPIKey reads the member's engine credential.
//
// Read fresh on every session create rather than cached: acpd holds the
// key only until the engine child is spawned and drops it, so this is
// the only place it lives, and it must be re-supplied after an acpd
// restart. Caching it here would be a second copy for no gain.
func (s *Server) engineAPIKey(ctx context.Context, namespace string) (string, error) {
	secret, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading the factory-user secret: %w", err)
	}
	key := string(secret.Data[engineAPIKeySecretKey])
	if key == "" {
		return "", fmt.Errorf("no %s in this namespace's factory-user secret", engineAPIKeySecretKey)
	}
	return key, nil
}

// ensureResearchSession returns the live acpd session, creating it if
// acpd does not have one.
//
// Lazily, rather than at sandbox creation, for two reasons. `factory
// research start` has no engine key and must not be given one — it would
// land on the sandbox's disk. And a session does not survive an acpd
// restart, because the key is fixed in the engine child's environment at
// exec time; re-creating on demand is therefore not just the first path
// but the recovery path, and making them the same code means the
// recovery is exercised every time anyone opens a conversation.
func (s *Server) ensureResearchSession(ctx context.Context, conn *researchConn) (*acpd.Session, error) {
	session, err := conn.client.GetSession(ctx, conn.view.SessionID)
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, acpd.ErrNotFound) {
		return nil, err
	}

	apiKey, err := s.engineAPIKey(ctx, conn.view.Namespace)
	if err != nil {
		return nil, err
	}
	return conn.client.CreateSession(ctx, acpd.CreateSessionRequest{
		ID:     conn.view.SessionID,
		Engine: acpd.EngineGemini,
		CWD:    conn.view.cwd(),
	}, apiKey)
}

// researchError maps an acpd failure onto a status for the caller.
//
// A pod that has gone away mid-conversation surfaces as a dial error,
// not an HTTP status, and reporting that as 500 would tell the UI to
// show a crash when the honest answer is that the session is over.
func researchError(c *gin.Context, err error) {
	var acpErr *acpd.Error
	if errors.As(err, &acpErr) {
		c.JSON(acpErr.StatusCode, gin.H{"error": acpErr.Message})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "the conversation server is unreachable", "details": err.Error()})
}

// getResearchSession reports one session's state: the sandbox's, always,
// and the engine's when there is one to ask.
//
// It does not create the session. A status call that spawned an engine
// would make polling the list expensive and surprising; opening the
// event stream is the deliberate act that starts one.
func (s *Server) getResearchSession(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	body := gin.H{
		"sessionId": conn.view.SessionID,
		"sandbox":   conn.view.Sandbox,
		"namespace": conn.view.Namespace,
		"repo":      conn.view.Repo,
		"cwd":       conn.view.cwd(),
		"live":      false,
		// The title and the state of any owed opening turn, so a
		// conversation opened straight from a click can name itself and
		// say what it is waiting for without also fetching the list.
		"title":        conn.view.Title,
		"opening":      conn.view.Opening,
		"openingError": conn.view.OpeningError,
	}
	session, err := conn.client.GetSession(c.Request.Context(), conn.view.SessionID)
	switch {
	case err == nil:
		body["live"] = true
		body["busy"] = session.Busy
		body["offset"] = session.Offset
		body["engine"] = session.Engine
		body["createdAt"] = session.CreatedAt
	case errors.Is(err, acpd.ErrNotFound):
		// The sandbox is up but no engine is running in it: the normal
		// state of a session nobody has opened yet, and of one whose
		// acpd restarted. Not an error.
	default:
		body["unreachable"] = err.Error()
	}
	c.JSON(http.StatusOK, body)
}

// promptResearchSession sends one turn.
//
// The reply is not in the response: it arrives as events on the stream
// the caller is already following. What comes back is the transcript
// offset after the prompt was recorded, so a caller that is not yet
// following knows where to start without replaying its own message.
func (s *Server) promptResearchSession(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}

	ctx := c.Request.Context()
	if _, err := s.ensureResearchSession(ctx, conn); err != nil {
		researchError(c, err)
		return
	}
	offset, err := conn.client.Prompt(ctx, conn.view.SessionID, req.Text)
	if err != nil {
		// A 409 from acpd means a turn is already in flight. It travels
		// through researchError unchanged, so the UI can disable the
		// composer rather than treat it as a failure.
		researchError(c, err)
		return
	}
	// A session nobody named is named by what was asked of it. Only the
	// first turn does this, and only when nothing else has: a canned
	// session already has its title, and a renamed one keeps it.
	if conn.view.Title == "" {
		if title := research.Truncate(req.Text); title != "" {
			if err := s.setResearchTitle(ctx, conn.view.Namespace, conn.view.Sandbox, title); err != nil {
				// Best effort. The turn is already delivered, and an
				// untitled row is a cosmetic loss.
				klog.V(2).Infof("research: could not title %s: %v", conn.view.Sandbox, err)
			}
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"offset": offset})
}

// renameResearchSession sets what a session is called.
//
// It does not go through resolveResearch: renaming a paused or
// still-booting session is reasonable, and neither has a pod to dial.
func (s *Server) renameResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	// Truncated rather than rejected: the member pasted something long
	// and meant it as a name.
	title := research.Truncate(req.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return
	}
	if err := s.setResearchTitle(ctx, view.Namespace, view.Sandbox, title); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename the session", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessionId": sessionID, "title": title})
}

// setResearchTitle writes the title annotation onto the sandbox.
//
// On the sandbox rather than in a table because the sandbox is the
// session: deleting it must take the name with it, and nothing else
// here has a database.
func (s *Server) setResearchTitle(ctx context.Context, namespace, sandboxName, title string) error {
	sandboxes := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace)
	sb, err := sandboxes.Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if annotations[research.TitleAnnotation] == title {
		return nil
	}
	annotations[research.TitleAnnotation] = title
	sb.SetAnnotations(annotations)
	_, err = sandboxes.Update(ctx, sb, v1.UpdateOptions{})
	return err
}

// resolveResearchPermission answers a permission request the engine is
// blocked on. Until this lands the turn makes no progress, so it does
// not create a session: if there is no engine there is nothing waiting.
func (s *Server) resolveResearchPermission(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	var res acpd.PermissionResolution
	if err := c.ShouldBindJSON(&res); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permission resolution", "details": err.Error()})
		return
	}
	if err := conn.client.ResolvePermission(c.Request.Context(), conn.view.SessionID, res); err != nil {
		researchError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// cancelResearchSession interrupts the turn in flight. ACP models cancel
// as a notification, so the turn ends with a cancelled stopReason on the
// event stream rather than this call reporting the outcome.
func (s *Server) cancelResearchSession(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	if err := conn.client.Cancel(c.Request.Context(), conn.view.SessionID); err != nil {
		researchError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteResearchSession ends a conversation for good.
//
// It deletes the sandbox, not the acpd session, because the sandbox IS
// the session: the transcript lives on its PVC and nothing is kept
// anywhere else. Deleting the acpd session alone would stop the engine
// and leave a sandbox that costs a PVC and answers no questions.
func (s *Server) deleteResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return
	}
	// Resolved from the object rather than from resolveResearch: a
	// paused or half-booted session is exactly the one a member most
	// wants to be able to delete, and that path refuses both.
	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return
	}
	if err := s.K8sManager.DeleteSandbox(ctx, namespace, view.Sandbox); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed", "details": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// Frames sent to the browser on the event websocket.
const (
	researchFrameOpen   = "open"
	researchFrameEvent  = "event"
	researchFrameClosed = "closed"
)

// researchFrame is one message on the event websocket.
//
// Every event frame carries the offset AFTER it, so a browser that
// stores the last one it saw and reconnects with ?offset= sees each
// event exactly once across a dropped connection. That is the whole
// resumption contract; there is no sequence number to reconcile.
type researchFrame struct {
	Type    string        `json:"type"`
	Offset  int64         `json:"offset"`
	Event   *acpd.Event   `json:"event,omitempty"`
	Session *acpd.Session `json:"session,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Error   string        `json:"error,omitempty"`
}

const (
	researchPingInterval = 20 * time.Second
	researchReadTimeout  = 70 * time.Second
)

// streamResearchEvents follows one conversation over a websocket.
//
// A websocket rather than SSE for the reason the terminal learned the
// hard way: this path idles for as long as the member is thinking, and
// the intermediaries between here and a browser drop idle connections
// without saying so. Ping/pong keeps it open and makes a real death
// detectable.
//
// Traffic is one-way. Prompts, permissions and cancels are ordinary
// POSTs, so this socket never has to multiplex a request onto a stream
// it is also reading.
//
// This is the call that starts the engine. Opening the conversation is
// the deliberate act; the status and list routes stay cheap because they
// do not.
func (s *Server) streamResearchEvents(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}

	offset := int64(0)
	if raw := c.Query("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
			return
		}
		offset = parsed
	}

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		klog.Errorf("research: websocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = ws.Close() }()

	// Detached from the HTTP request: the follow lives as long as the
	// websocket, and only as long.
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Request.Context()))
	defer cancel()

	out := &wsJSON{ws: ws}

	session, err := s.ensureResearchSession(ctx, conn)
	if err != nil {
		_ = out.send(researchFrame{Type: researchFrameClosed, Error: err.Error()})
		return
	}
	if err := out.send(researchFrame{Type: researchFrameOpen, Session: session, Offset: offset}); err != nil {
		return
	}

	// Keepalive, and the reader that keeps the read deadline alive. The
	// browser sends nothing here, so without this loop the socket would
	// be closed by its own read timeout while the agent was working.
	_ = ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
	})
	go func() {
		defer cancel()
		for {
			if _, _, rerr := ws.ReadMessage(); rerr != nil {
				return
			}
			_ = ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
		}
	}()
	go func() {
		ticker := time.NewTicker(researchPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := out.ping(); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Events rather than Follow: Follow hands the callback an event but
	// not the offset after it, and the offset is what makes a reconnect
	// exact.
	stream, err := conn.client.Events(ctx, conn.view.SessionID, offset, true)
	if err != nil {
		_ = out.send(researchFrame{Type: researchFrameClosed, Offset: offset, Error: err.Error()})
		return
	}
	defer func() { _ = stream.Close() }()

	for stream.Next() {
		event := stream.Event()
		if err := out.send(researchFrame{Type: researchFrameEvent, Offset: stream.Offset(), Event: &event}); err != nil {
			return
		}
	}
	if ctx.Err() != nil {
		// The browser went away, or the ping failed. Nothing to report:
		// there is nobody left to report it to.
		return
	}
	frame := researchFrame{Type: researchFrameClosed, Offset: stream.Offset(), Reason: "stream ended"}
	if err := stream.Err(); err != nil {
		frame.Error = err.Error()
	}
	_ = out.send(frame)
}

// wsJSON serializes frames onto one websocket.
//
// The mutex is not optional: the ping goroutine and the event loop both
// write, and gorilla/websocket permits exactly one concurrent writer.
type wsJSON struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *wsJSON) send(frame researchFrame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteJSON(frame)
}

func (w *wsJSON) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}
