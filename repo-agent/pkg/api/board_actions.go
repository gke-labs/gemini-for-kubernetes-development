package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

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

// markApplies marks the rows a write or a revise filed on their runs
// still stands on (WorkItem.Posting). Requests carry the number of the
// issue or PR whose row follows them.
func (s *Server) markApplies(ctx context.Context, board *unstructured.Unstructured, items map[string]*models.WorkItem) {
	reqs, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + board.GetName() + "," +
			boardv1alpha1.LabelVerb + " in (" + boardv1alpha1.VerbApply + "," + boardv1alpha1.VerbRevise + ")",
	})
	if err != nil {
		return
	}
	for _, req := range reqs {
		if !req.Active() {
			continue
		}
		for _, key := range []string{"issue-", "pr-"} {
			if item := items[key+strconv.Itoa(req.Spec.Number)]; item != nil {
				item.Posting = true
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

// planRevisingReason is why a plan's actions wait while the agent
// rewrites it.
const planRevisingReason = "the plan is being revised"

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
