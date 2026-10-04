package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
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
	case "Triage/label", "Triage/comment", "Plan/comment":
		s.fileApply(c, board, number, req.Kind, req.Verb)
	case "Triage/reject":
		s.rejectBoardTriage(c)
	case "Plan/edit":
		rebody(c, gin.H{"plan": req.Text})
		s.putBoardPlanDraft(c)
	case "Plan/run":
		// The only follow-up offered is fix: approving the plan launches
		// it.
		s.planBoardApprove(c)
	case "Plan/reject":
		s.planBoardReject(c)
	}
}

// fileApply files the write for the controller, which runs factory apply
// --action with the clicker's token and marks the draft once it is done:
// the board writes nothing to GitHub itself. 202, since the write is the
// controller's next pass.
func (s *Server) fileApply(c *gin.Context, board *unstructured.Unstructured, number int, kind, action string) {
	filed, err := s.fileRequest(c.Request.Context(), board, boardv1alpha1.RequestSpec{
		Verb:   boardv1alpha1.VerbApply,
		Member: s.Auth.GetNamespaceFromContext(c),
		Number: number,
		Apply:  &boardv1alpha1.ApplyRequest{Kind: kind, Action: action},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the write", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name})
}

// markApplies says on each row's actions what the apply Requests say of
// them: a write filed and not yet done is "posting"; one whose last
// attempt failed carries why, and stays clickable — a retry is a click.
func (s *Server) markApplies(ctx context.Context, board *unstructured.Unstructured, items map[string]*models.WorkItem) {
	reqs, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + board.GetName() + "," + boardv1alpha1.LabelVerb + "=" + boardv1alpha1.VerbApply,
	})
	if err != nil {
		return
	}
	seen := map[string]bool{}
	// Newest first: the newest Request for a write is the word on it.
	for _, req := range reqs {
		spec := req.Spec
		if spec.Apply == nil || seen[spec.Key()] {
			continue
		}
		seen[spec.Key()] = true
		item := items["issue-"+strconv.Itoa(spec.Number)]
		if item == nil {
			continue
		}
		actions := item.TriageActions
		if spec.Apply.Kind == "Plan" {
			actions = item.PlanActions
		}
		for i := range actions {
			a := &actions[i]
			if a.Verb != spec.Apply.Action {
				continue
			}
			switch {
			case req.Active() && a.Enabled:
				a.Enabled, a.Reason = false, "posting"
			case req.Status.Phase == boardv1alpha1.RequestFailed:
				a.Error = req.Status.Message
			}
		}
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
