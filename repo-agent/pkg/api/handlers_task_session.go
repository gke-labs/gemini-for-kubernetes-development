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

// Task sessions: the agent conversation a factory task (plan, triage,
// research) ran its asks in, opened in the session view.
//
// factory's daemon keeps a task's session in the task's directory, named
// after the task. While the task runs, only the task may drive it, so
// this is a live watch; once it has ended, creating the session again
// loads the conversation (session/load) and the member continues it. Only
// daemons that host sessions have them, reached over the port-forward;
// a sandbox on an older image answers `legacy`, and the UI falls back to
// the terminal.
//
// Routes are keyed by the sandbox and the task. The sandbox is looked up
// in the member's own namespace, so naming one reaches nobody else's.

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// safeTaskID mirrors factory's rule for task ids: one path element.
var safeTaskID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// safeSandboxName is a Kubernetes object name.
var safeSandboxName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// taskSessionClientForPod is the dial seam: the client for the sessions
// the pod's daemon hosts, false when it hosts none.
var taskSessionClientForPod = func(ctx context.Context, d *podacpd.Dialer, pod *corev1.Pod) (*acpd.Client, bool) {
	return d.Sessions(ctx, pod)
}

// taskSessionConn is a resolved, reachable task session.
type taskSessionConn struct {
	namespace string
	sandbox   *unstructured.Unstructured
	task      string
	client    *acpd.Client
}

// repo is the repository the task ran on, as factory records it.
func (t *taskSessionConn) repo() string { return t.sandbox.GetAnnotations()["repo"] }

// cwd is where the task's agent ran: factory recipes run in the checkout
// at /workspaces/<repo>. The session loads only in the same place.
func (t *taskSessionConn) cwd() string {
	if repo := t.repo(); repo != "" {
		return "/workspaces/" + repo
	}
	return ""
}

// engine is the engine the sandbox's tasks run on: a research sandbox's
// own (acpd.ResearchEngine), else the board's. The session loads only on
// the engine it ran on; a sandbox whose engine changed since starts the
// agent fresh, and says so (loaded false).
func (t *taskSessionConn) engine() string {
	annotations := t.sandbox.GetAnnotations()
	if annotations[researchSessionIDAnnotation] != "" {
		return acpd.ResearchEngine(annotations)
	}
	return sandboxEngine(annotations, "gemini")
}

// taskSessionSandbox is the sandbox in the URL, looked up in the member's
// namespace, with the task. 404: no such sandbox. A paused one is fine:
// what reads only its annotations needs no pod.
func (s *Server) taskSessionSandbox(c *gin.Context) (*unstructured.Unstructured, string, bool) {
	name, task := c.Param("sandbox"), c.Param("task")
	if !safeSandboxName.MatchString(name) || !safeTaskID.MatchString(task) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sandbox or task"})
		return nil, "", false
	}
	sb, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(s.Auth.GetNamespaceFromContext(c)).Get(c.Request.Context(), name, v1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sandbox not found"})
			return nil, "", false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the sandbox", "details": err.Error()})
		return nil, "", false
	}
	return sb, task, true
}

// resolveTaskSession turns the sandbox and task in the URL into a session
// to talk to. 404: no such sandbox. 409: not up yet (worth retrying), or
// `legacy`, an image whose daemon hosts no sessions (not worth it).
func (s *Server) resolveTaskSession(c *gin.Context) (*taskSessionConn, bool) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sb, task, ok := s.taskSessionSandbox(c)
	if !ok {
		return nil, false
	}
	name := sb.GetName()
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "the sandbox is paused", "sandbox": name, "paused": true})
		return nil, false
	}
	pod, err := s.researchPod(ctx, namespace, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to find the sandbox's pod", "details": err.Error()})
		return nil, false
	}
	if pod == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "the sandbox is still starting", "sandbox": name, "starting": true})
		return nil, false
	}
	client, ok := taskSessionClientForPod(ctx, s.ACPD, pod)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "this sandbox's image keeps no agent sessions for its tasks",
			"sandbox": name, "namespace": namespace, "legacy": true,
		})
		return nil, false
	}
	return &taskSessionConn{namespace: namespace, sandbox: sb, task: task, client: client}, true
}

// ensureTaskSession returns the task's live session, creating it when the
// daemon has none, which loads the recorded conversation. The daemon
// refuses the create while the task runs (only the task may start its
// session), and that refusal is passed on as the acpd 409 it is.
func (s *Server) ensureTaskSession(ctx context.Context, conn *taskSessionConn) (*acpd.Session, error) {
	session, err := conn.client.GetSession(ctx, conn.task)
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, acpd.ErrNotFound) {
		return nil, err
	}
	apiKey, err := s.engineAPIKey(ctx, conn.namespace)
	if err != nil {
		return nil, err
	}
	return conn.client.CreateSession(ctx, acpd.CreateSessionRequest{
		ID:     conn.task,
		Task:   conn.task,
		Engine: conn.engine(),
		CWD:    conn.cwd(),
		// The member is reading this one, but it starts as research does,
		// for the same reasons; the approvals control tightens it.
		Mode:        acpd.ResearchMode,
		AutoApprove: acpd.ResearchAutoApprove,
	}, apiKey)
}

// getTaskSession reports the session's state without starting an engine:
// opening the event stream is what does that.
func (s *Server) getTaskSession(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	annotations := conn.sandbox.GetAnnotations()
	body := gin.H{
		"sessionId": conn.task,
		"task":      conn.task,
		"sandbox":   conn.sandbox.GetName(),
		"namespace": conn.namespace,
		"repo":      conn.repo(),
		// The issue or pull request the sandbox is for.
		"htmlUrl": annotations["htmlURL"],
		"cwd":     conn.cwd(),
		"live":    false,
	}
	ctx := c.Request.Context()
	// What the session offers, whichever recipe it is: the recipe's
	// revises, and the draft its output is.
	if revises := s.sessionRevises(ctx, c, conn.sandbox, conn.task); len(revises) > 0 {
		body["revises"] = revises
	}
	if draft := s.sessionDraft(ctx, c, conn.sandbox, conn.task); draft != nil {
		body["draft"] = draft
	}
	// A research conversation's name, and its id for renaming and
	// deleting it.
	if id := annotations[researchSessionIDAnnotation]; id != "" {
		body["research"], body["title"] = id, annotations[research.TitleAnnotation]
	}
	session, err := conn.client.GetSession(ctx, conn.task)
	switch {
	case err == nil:
		body["live"] = true
		body["busy"] = session.Busy
		body["waiting"] = session.Waiting
		body["offset"] = session.Offset
		body["engine"] = session.Engine
		body["createdAt"] = session.CreatedAt
		body["mode"] = session.Mode
		body["availableModes"] = session.AvailableModes
		body["modeError"] = session.ModeError
		body["autoApprove"] = session.AutoApprove
		body["loaded"] = session.Loaded
		body["held"] = session.Held
	case errors.Is(err, acpd.ErrNotFound):
		// The task ended and nobody has continued it yet, or it has not
		// asked anything yet.
	default:
		body["unreachable"] = err.Error()
	}
	c.JSON(http.StatusOK, body)
}

// promptTaskSession sends one turn, continuing the session first if need
// be. A running task's session answers 409: it can be watched, not driven.
func (s *Server) promptTaskSession(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
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
	if _, err := s.ensureTaskSession(ctx, conn); err != nil {
		researchError(c, err)
		return
	}
	offset, err := conn.client.Prompt(ctx, conn.task, req.Text)
	if err != nil {
		researchError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"offset": offset})
}

func (s *Server) resolveTaskSessionPermission(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	var res acpd.PermissionResolution
	if err := c.ShouldBindJSON(&res); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permission resolution", "details": err.Error()})
		return
	}
	if err := conn.client.ResolvePermission(c.Request.Context(), conn.task, res); err != nil {
		researchError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) cancelTaskSession(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	if err := conn.client.Cancel(c.Request.Context(), conn.task); err != nil {
		researchError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) setTaskSessionMode(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Mode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode is required"})
		return
	}
	session, err := conn.client.SetMode(c.Request.Context(), conn.task, req.Mode)
	if err != nil {
		researchError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"sessionId":      conn.task,
		"mode":           session.Mode,
		"availableModes": session.AvailableModes,
		"autoApprove":    session.AutoApprove,
	})
}

// streamTaskSessionEvents follows the session over a websocket, as the
// research stream does. Opening it continues an ended task's session; a
// running task's that the task has not created yet closes quietly, and
// the UI's probe tries again.
func (s *Server) streamTaskSessionEvents(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	offset, ok := eventOffset(c)
	if !ok {
		return
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		klog.Errorf("task session: websocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = ws.Close() }()

	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Request.Context()))
	defer cancel()

	followSession(ctx, cancel, ws, conn.client, conn.task, offset, func(ctx context.Context) (*acpd.Session, error) {
		session, err := s.ensureTaskSession(ctx, conn)
		var acpErr *acpd.Error
		if errors.As(err, &acpErr) && acpErr.StatusCode == http.StatusConflict {
			return nil, errQuietClose
		}
		return session, err
	})
}
