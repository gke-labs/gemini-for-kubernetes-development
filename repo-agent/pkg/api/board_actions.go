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
// POST …/actions/:verb {kind: Triage|Plan, run, revise, text}, text being
// an edit's new draft.
// The action must be on the row (offered, and enabled just now, for the
// viewer). The draft verbs and follow-ups are the handlers the board's
// buttons already call; any other verb is a write, filed for the
// controller's factory apply.
func (s *Server) boardIssueAction(c *gin.Context) {
	var req struct {
		Kind string `json:"kind"`
		Verb string `json:"-"`
		Run  string `json:"run"`
		// Revise is which of the output's revises, for a revise.
		Revise string `json:"revise"`
		Text   string `json:"text"`
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
	var runKey string
	switch req.Kind {
	case "Triage":
		runKey = factorycli.AnnotationTriageRun
		if sb, _ = s.findTriageDraft(c, board, owner, repo, number); sb != nil {
			actions = triageWorkActions(sb.GetAnnotations())
		}
	case "Plan":
		runKey = factorycli.AnnotationPlanRun
		if sb, _ = s.findPlanSandbox(c, board, owner, repo, number); sb != nil {
			a := sb.GetAnnotations()
			if factorycli.PlanDraft(a) != "" && !planApproved(a) {
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
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	actions = actionsFor(actions, s.repoPermissions(c.Request.Context(), s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), repoURL))
	arg := req.Run
	if req.Verb == "revise" {
		arg = req.Revise
	}
	action, offered := findWorkAction(actions, req.Verb, arg)
	if !offered {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the %s does not offer %s", strings.ToLower(req.Kind), strings.TrimSpace(req.Verb+" "+arg))})
		return
	}
	if !action.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("cannot %s now: %s", req.Verb, action.Reason)})
		return
	}

	if factorycli.IsApplyVerb(req.Verb) {
		s.fileApply(c, board, sb.GetName(), number, factorycli.RunTaskType(runKey), req.Verb)
		return
	}
	switch req.Kind + "/" + req.Verb {
	case "Triage/edit":
		rebody(c, gin.H{"draft": req.Text})
		s.putBoardTriageDraft(c)
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
	case "Plan/revise":
		s.reviseSandbox(c, sb, board.GetName(), number, action.Revise, nil)
	}
}

// fileApply files the write of action, of the draft of run on sandbox,
// for the controller, which runs factory apply --action with the
// clicker's token and marks the draft once it is done: the board writes
// nothing to GitHub itself. number is the issue whose row follows it, 0
// for none. 202, since the write is the controller's next pass.
func (s *Server) fileApply(c *gin.Context, board *unstructured.Unstructured, sandbox string, number int, run, action string) {
	filed, err := s.fileRequest(c.Request.Context(), board, boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbApply,
		Member:  s.Auth.GetNamespaceFromContext(c),
		Sandbox: sandbox,
		Number:  number,
		Apply:   &boardv1alpha1.ApplyRequest{Run: run, Action: action},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the write", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name})
}

// postingReason is a write's action's reason while it stands.
const postingReason = "posting"

// revisingReason is a revise's action's reason while it stands; the
// draft's other actions wait for the plan it writes (planRevisingReason).
const revisingReason = "revising"

// markApplies says on each row's actions what the apply and revise
// Requests say of them: a write filed and not yet done is "posting", a
// revise "revising"; one whose last attempt failed carries why, and stays
// clickable — a retry is a click.
func (s *Server) markApplies(ctx context.Context, board *unstructured.Unstructured, items map[string]*models.WorkItem) {
	reqs, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + board.GetName() + "," +
			boardv1alpha1.LabelVerb + " in (" + boardv1alpha1.VerbApply + "," + boardv1alpha1.VerbRevise + ")",
	})
	if err != nil {
		return
	}
	seen := map[string]bool{}
	// Newest first: the newest Request for a write is the word on it.
	for _, req := range reqs {
		spec := req.Spec
		if seen[spec.Key()] {
			continue
		}
		seen[spec.Key()] = true
		item := items["issue-"+strconv.Itoa(spec.Number)]
		if item == nil {
			continue
		}
		if spec.Verb == boardv1alpha1.VerbRevise {
			// The plan's own: the issue's sandbox has other runs' too.
			if _, ok := findWorkAction(item.PlanActions, "revise", spec.Revise); ok {
				markRevise(item.PlanActions, req)
			}
			continue
		}
		if spec.Apply == nil {
			continue
		}
		var actions []models.WorkAction
		switch spec.Apply.Run {
		case factorycli.RunTaskType(factorycli.AnnotationTriageRun):
			actions = item.TriageActions
		case factorycli.RunTaskType(factorycli.AnnotationPlanRun):
			actions = item.PlanActions
		}
		for i := range actions {
			a := &actions[i]
			if a.Verb != spec.Apply.Action {
				continue
			}
			switch {
			case req.Active() && a.Enabled:
				a.Enabled, a.Reason = false, postingReason
			case req.Status.Phase == boardv1alpha1.RequestFailed:
				a.Error = req.Status.Message
			}
		}
	}
}

// markRevise says on a plan's actions what a revise Request says: while
// it stands, the revise is "revising" and the rest wait for the plan it
// writes; failed, the revise carries why.
func markRevise(actions []models.WorkAction, req boardv1alpha1.Request) {
	for i := range actions {
		a := &actions[i]
		mine := a.Verb == "revise" && a.Revise == req.Spec.Revise
		switch {
		case req.Active() && mine:
			a.Enabled, a.Reason = false, revisingReason
		case req.Active() && a.Enabled && a.Verb != "reject":
			a.Enabled, a.Reason = false, planRevisingReason
		case req.Status.Phase == boardv1alpha1.RequestFailed && mine:
			a.Error = req.Status.Message
		}
	}
}

// Why a viewer may not take a write (factorycli.VerbNeeds): adding labels
// needs triage access; a write the board does not know needs push.
// Comments, pending reviews and pushes to the viewer's own fork need
// nothing.
const (
	needsTriageAccess = "needs triage access on the repo"
	needsPushAccess   = "needs push access on the repo"
)

// forViewer is the feed as the viewer may act on it. The feed is built and
// cached per board, for whoever polls; what a viewer may write to GitHub
// is theirs, so it is applied here, on a copy, as the feed is served.
func (s *Server) forViewer(ctx context.Context, board *unstructured.Unstructured, namespace, sessionUser string, items []models.WorkItem) []models.WorkItem {
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	perms := s.repoPermissions(ctx, namespace, sessionUser, repoURL)
	if perms.push && perms.triage {
		return items
	}
	out := make([]models.WorkItem, len(items))
	for i := range items {
		out[i] = items[i]
		if len(items[i].TriageActions) > 0 {
			out[i].TriageActions = actionsFor(items[i].TriageActions, perms)
		}
		if len(items[i].PlanActions) > 0 {
			out[i].PlanActions = actionsFor(items[i].PlanActions, perms)
		}
	}
	return out
}

// actionsFor is a copy of a draft's actions with what perms say of each:
// one whose verb needs access the viewer lacks cannot be taken.
func actionsFor(actions []models.WorkAction, perms repoPerms) []models.WorkAction {
	out := append([]models.WorkAction(nil), actions...)
	for i := range out {
		if !out[i].Enabled {
			continue
		}
		switch factorycli.VerbNeeds(out[i].Verb) {
		case factorycli.NeedsTriage:
			if !perms.triage {
				out[i].Enabled, out[i].Reason = false, needsTriageAccess
			}
		case factorycli.NeedsPush:
			if !perms.push {
				out[i].Enabled, out[i].Reason = false, needsPushAccess
			}
		}
	}
	return out
}

// rebody replaces the request's body, for a handler that binds its own.
func rebody(c *gin.Context, body any) {
	b, _ := json.Marshal(body)
	c.Request.Body = io.NopCloser(bytes.NewReader(b))
	c.Request.ContentLength = int64(len(b))
}

// findWorkAction is the offered action verb, with arg its run or revise
// when given.
func findWorkAction(actions []models.WorkAction, verb, arg string) (models.WorkAction, bool) {
	for _, a := range actions {
		if a.Verb == verb && (arg == "" || a.Run == arg || a.Revise == arg) {
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

// triagePublished reports whether a triage draft's assessment was posted:
// its comment action applied.
func triagePublished(annotations map[string]string) bool {
	return factorycli.IsApplied(annotations, factorycli.AnnotationTriageApplied, "comment")
}

// planApproved reports whether a plan draft was approved: its run action,
// a fix with this plan, applied.
func planApproved(annotations map[string]string) bool {
	return factorycli.IsApplied(annotations, factorycli.AnnotationPlanApplied, "run")
}

// triageWorkActions are the actions a triage draft's task output offers,
// with what its sandbox's annotations say of each just now.
func triageWorkActions(annotations map[string]string) []models.WorkAction {
	published := triagePublished(annotations)
	labeled := factorycli.IsApplied(annotations, factorycli.AnnotationTriageApplied, "label")
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

// planRevisingReason is why a plan's actions wait while the agent
// rewrites it.
const planRevisingReason = "the plan is being revised"

// planWorkActions are the actions a plan draft's task output offers, with
// what its sandbox's annotations and task say of each just now.
func planWorkActions(annotations map[string]string, revising, running bool) []models.WorkAction {
	commented := factorycli.IsApplied(annotations, factorycli.AnnotationPlanApplied, "comment")
	return workActions(factorycli.OfferedActions("Plan", annotations[factorycli.AnnotationPlanOutput]), func(a factorycli.Action) string {
		switch {
		case a.Verb == "reject":
			return ""
		case revising:
			return planRevisingReason
		case (a.Verb == "run" || a.Verb == "revise") && running:
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
			Verb: a.Verb, Run: a.Run, Revise: a.Revise, Label: a.Label, Field: a.Field, Format: a.Format,
			Enabled: reason == "", Reason: reason,
		})
	}
	return out
}
