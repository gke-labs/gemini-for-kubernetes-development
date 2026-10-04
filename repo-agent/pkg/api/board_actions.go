package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// boardIssueAction takes one action a draft's task output offers:
// POST …/actions/:verb {kind: Triage|Plan, run, text}, text being an
// edit's new draft.
// The action must be on the row (offered, and enabled just now); each is
// the handler the board's buttons already call.
func (s *Server) boardIssueAction(c *gin.Context) {
	var req struct {
		Kind string `json:"kind"`
		Verb string `json:"-"`
		Run  string `json:"run"`
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	req.Verb = c.Param("verb")
	_, board, owner, repo, _, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	var sb *unstructured.Unstructured
	var actions []models.WorkAction
	switch req.Kind {
	case "Triage":
		if sb, _ = s.findTriageDraft(c, board, owner, repo, number); sb != nil {
			actions = triageWorkActions(sb.GetAnnotations())
		}
	case "Plan":
		if sb, _ = s.findPlanSandbox(c, board, owner, repo, number); sb != nil {
			a := sb.GetAnnotations()
			if a[annoPlanDraft] != "" && a[annoPlanApproved] == "" {
				actions = planWorkActions(a, planIsRevising(a), a[annoTaskState] == "Running")
			}
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be Triage or Plan"})
		return
	}
	if sb == nil || actions == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("no %s draft on #%d", strings.ToLower(req.Kind), number)})
		return
	}
	action, offered := findWorkAction(actions, req.Verb, req.Run)
	if !offered {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the %s does not offer %s", strings.ToLower(req.Kind), strings.TrimSpace(req.Verb+" "+req.Run))})
		return
	}
	if !action.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("cannot %s now: %s", req.Verb, action.Reason)})
		return
	}

	switch req.Kind + "/" + req.Verb {
	case "Triage/edit":
		rebody(c, gin.H{"draft": req.Text})
		s.putBoardTriageDraft(c)
	case "Triage/label":
		s.applyBoardTriage(c, true, false)
	case "Triage/comment":
		s.applyBoardTriage(c, false, true)
	case "Triage/reject":
		s.rejectBoardTriage(c)
	case "Plan/edit":
		rebody(c, gin.H{"plan": req.Text})
		s.putBoardPlanDraft(c)
	case "Plan/comment":
		s.commentBoardPlan(c)
	case "Plan/run":
		// The only follow-up offered is fix: approving the plan launches
		// it.
		s.planBoardApprove(c)
	case "Plan/reject":
		s.planBoardReject(c)
	}
}

// commentBoardPlan posts a plan draft on its issue under the clicker's
// token, marked as factory apply marks it so that neither posts it twice.
func (s *Server) commentBoardPlan(c *gin.Context) {
	ctx, board, owner, repo, token, number, ok := s.boardWriteContext(c)
	if !ok {
		return
	}
	sb, ns := s.findPlanSandbox(c, board, owner, repo, number)
	if sb == nil || sb.GetAnnotations()[annoPlanDraft] == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plan to post"})
		return
	}
	a := sb.GetAnnotations()
	mark := planCommentMarker(factorycli.TaskOutputTask("Plan", a[factorycli.AnnotationPlanOutput]))
	gh := githubClientForToken(ctx, token)
	posted := false
	if mark != "" {
		var err error
		if posted, err = hasIssueComment(c, gh, owner, repo, number, mark); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read the issue's comments", "details": err.Error()})
			return
		}
	}
	if !posted {
		body := planComment(a[annoPlanDraft]) + mark
		if _, _, err := gh.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{Body: &body}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to post the plan", "details": err.Error()})
			return
		}
	}
	if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.AnnotationPlanCommented, nowRFC3339()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark the plan posted", "details": err.Error()})
		return
	}
	c.Status(http.StatusOK)
}

// planComment is the comment a plan is posted as, as factory apply posts
// it (taskoutput.PlanComment).
func planComment(plan string) string {
	return "**Implementation plan**\n\n" + strings.TrimSpace(plan)
}

// planCommentMarker is factory apply's hidden marker for a plan of task,
// or "" without one.
func planCommentMarker(task string) string {
	if task == "" {
		return ""
	}
	return fmt.Sprintf("\n\n<!-- factory:task-output kind=Plan task=%s -->", task)
}

func hasIssueComment(c *gin.Context, gh *github.Client, owner, repo string, number int, mark string) (bool, error) {
	mark = strings.TrimSpace(mark)
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := gh.Issues.ListComments(c.Request.Context(), owner, repo, number, opts)
		if err != nil {
			return false, err
		}
		for _, cm := range comments {
			if strings.Contains(cm.GetBody(), mark) {
				return true, nil
			}
		}
		if resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
}

// rebody replaces the request's body, for a handler that binds its own.
func rebody(c *gin.Context, body any) {
	b, _ := json.Marshal(body)
	c.Request.Body = io.NopCloser(bytes.NewReader(b))
	c.Request.ContentLength = int64(len(b))
}

func findWorkAction(actions []models.WorkAction, verb, run string) (models.WorkAction, bool) {
	for _, a := range actions {
		if a.Verb == verb && (run == "" || a.Run == run) {
			return a, true
		}
	}
	return models.WorkAction{}, false
}

// planIsRevising is whether feedback newer than the stored plan means a
// refinement round is queued or running: agent motion, not the member's
// move.
func planIsRevising(annotations map[string]string) bool {
	fb, err := time.Parse(time.RFC3339, annotations[annoPlanFeedbackAt])
	if err != nil {
		return false
	}
	planned, err := time.Parse(time.RFC3339, annotations[annoPlannedAt])
	return err != nil || fb.After(planned)
}

// triageWorkActions are the actions a triage draft's task output offers,
// with what its sandbox's annotations say of each just now.
func triageWorkActions(annotations map[string]string) []models.WorkAction {
	published := annotations[annoTriagePublished] != ""
	labeled := annotations[factorycli.AnnotationTriageLabeled] != ""
	return workActions(factorycli.OfferedActions("Triage", annotations[factorycli.AnnotationTriageOutput]), func(a factorycli.Action) string {
		switch {
		case a.Verb == "label" && labeled:
			return "labels added"
		case a.Verb == "label" && published:
			return "already posted"
		case a.Verb == "comment" && published:
			return "assessment posted"
		case a.Verb == "edit" && published:
			return "already posted"
		}
		return ""
	})
}

// planWorkActions are the actions a plan draft's task output offers, with
// what its sandbox's annotations and task say of each just now.
func planWorkActions(annotations map[string]string, revising, running bool) []models.WorkAction {
	commented := annotations[factorycli.AnnotationPlanCommented] != ""
	return workActions(factorycli.OfferedActions("Plan", annotations[factorycli.AnnotationPlanOutput]), func(a factorycli.Action) string {
		switch {
		case a.Verb == "reject":
			return ""
		case revising:
			return "the plan is being revised"
		case a.Verb == "run" && running:
			return "a task is running"
		case a.Verb == "comment" && commented:
			return "plan posted"
		}
		return ""
	})
}

func workActions(offered []factorycli.Action, disabled func(factorycli.Action) string) []models.WorkAction {
	var out []models.WorkAction
	for _, a := range offered {
		reason := disabled(a)
		out = append(out, models.WorkAction{
			Verb: a.Verb, Run: a.Run, Label: a.Label, Field: a.Field, Format: a.Format,
			Enabled: reason == "", Reason: reason,
		})
	}
	return out
}
