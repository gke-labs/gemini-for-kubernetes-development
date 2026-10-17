package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// The Overseer page's sessions and sandbox cards
// (docs/design/overseer-work-view.md): the board's task-session and
// sandbox-card handlers, served under /api/overseers/:name for the
// sandboxes in overseer-<name>. overseerScope says so on the request;
// sessionNamespace is where those handlers look.

const (
	// ctxSessionNamespace is the namespace an overseerScope request's
	// sandboxes are in.
	ctxSessionNamespace = "sessionNamespace"
	// ctxOverseerScope marks a request overseerScope served: read-only
	// beyond the conversation, as overseer sandboxes are on no board.
	ctxOverseerScope = "overseerScope"
)

// overseerScope puts a request under /api/overseers/:name in that
// overseer's namespace. It goes after AdminMiddleware.
func (s *Server) overseerScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if !safeTaskName.MatchString(name) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid overseer name"})
			return
		}
		c.Set(ctxSessionNamespace, "overseer-"+name)
		c.Set(ctxOverseerScope, true)
		c.Next()
	}
}

// sessionNamespace is the namespace the task-session and sandbox-card
// handlers look sandboxes up in: the overseer's under overseerScope, else
// the member's.
func (s *Server) sessionNamespace(c *gin.Context) string {
	if ns := c.GetString(ctxSessionNamespace); ns != "" {
		return ns
	}
	return s.Auth.GetNamespaceFromContext(c)
}

// cardSandbox is the sandbox a sandbox-card route names: :name on the
// board's, :sandbox under /api/overseers/:name, where :name is the
// overseer.
func cardSandbox(c *gin.Context) string {
	if name := c.Param("sandbox"); name != "" {
		return name
	}
	return c.Param("name")
}

// inOverseerScope is whether overseerScope served the request.
func inOverseerScope(c *gin.Context) bool {
	return c.GetBool(ctxOverseerScope)
}

// sessionKeySecret is the secret whose engine key opens a conversation on
// sb when none is live: the member's factory-user on the board; on an
// overseer's sandbox, the bot it ran as (user-<login>, its
// factory.gemini.google.com/user label), whose key its runs used.
func sessionKeySecret(c *gin.Context, sb *unstructured.Unstructured) string {
	if inOverseerScope(c) {
		if login := sb.GetLabels()[labelFactoryUser]; login != "" && safeTaskName.MatchString(login) {
			return "user-" + login
		}
	}
	return "factory-user"
}

const labelFactoryUser = "factory.gemini.google.com/user"

// Files a run leaves in its task directory (factory's taskoutput.File,
// taskoutput.AppliedFile and taskoutput.ApplyFailedFile).
const (
	taskOutputFile      = "task-output.yaml"
	taskAppliedFile     = "task-output.applied"
	taskApplyFailedFile = "task-output.apply-failed"
)

// overseerRunDraft is an overseer's run's result, to read: the board's
// controller stores no draft of an overseer's runs, so it is read from
// the run's task directory, through the sandbox's task server, with when
// the overseer's CLI applied it. nil when the run left none, or the task
// server cannot say.
func overseerRunDraft(ctx context.Context, conn *taskSessionConn) *taskSessionDraft {
	run, ok := factorycli.SessionRun(conn.sandbox.GetAnnotations(), conn.task)
	if !ok || run.Task == "" {
		return nil
	}
	data, err := conn.client.ReadTaskFile(ctx, run.Task, taskOutputFile)
	if err != nil {
		return nil
	}
	doc := string(data)
	kind := factorycli.TaskOutputKind(doc)
	text := factorycli.Draft(kind, doc)
	if kind == "" || text == "" {
		return nil
	}
	draft := &taskSessionDraft{Kind: kind, Actions: []models.WorkAction{}}
	if factorycli.DraftIsMarkdown(kind, doc) {
		draft.Markdown = text
	} else {
		draft.Spec = text
	}
	if run.EndedAt != nil {
		draft.DraftedAt = run.EndedAt.UTC().Format(time.RFC3339)
	}
	if mark, err := conn.client.ReadTaskFile(ctx, run.Task, taskAppliedFile); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(mark))); err == nil {
			draft.AppliedAt = t.UTC().Format(time.RFC3339)
		}
	}
	if mark, err := conn.client.ReadTaskFile(ctx, run.Task, taskApplyFailedFile); err == nil {
		draft.ApplyFailed = parseApplyFailure(mark)
	}
	return draft
}

// parseApplyFailure reads the mark factory leaves when applying a run's
// result failed (its taskoutput.ApplyFailure); nil when it is empty (a
// later apply succeeded) or says nothing.
func parseApplyFailure(data []byte) *taskApplyFailure {
	var f taskApplyFailure
	if json.Unmarshal(data, &f) != nil || f.Error == "" {
		return nil
	}
	return &f
}
