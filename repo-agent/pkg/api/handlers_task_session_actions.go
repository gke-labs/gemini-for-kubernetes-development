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

// What a task session offers, whichever recipe it is: the recipe's
// revises (Update plan, Save notes), from the run factory recorded on the
// sandbox, and the draft its task output is, with the output's actions.
// Clicking one files a Request where the draft lives: the issue's row when
// the sandbox is for an issue, the sandbox itself when it is not (a
// research conversation). The browser never picks; it posts to the
// session.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// taskSessionDraft is a session's draft: its task output, as the board
// stores it, with the actions it offers and what the clicks on them say.
// The output's revises are not among them; they are the session's.
type taskSessionDraft struct {
	Kind      string `json:"kind"`
	Markdown  string `json:"markdown"`
	DraftedAt string `json:"draftedAt,omitempty"`
	// SavedAt is when a Notes draft was last pushed to research/notes, and
	// Note the file it went to.
	SavedAt string              `json:"savedAt,omitempty"`
	Note    string              `json:"note,omitempty"`
	Actions []models.WorkAction `json:"actions"`
}

// notesRewritingReason is why a notes draft's actions wait while Save
// notes rewrites it.
const notesRewritingReason = "the notes are being rewritten"

// sessionIssue is the issue the sandbox is for, and the board its row is
// on; false for a sandbox that has none (a research conversation's).
func sessionIssue(sb *unstructured.Unstructured) (int, string, bool) {
	a := sb.GetAnnotations()
	n, ok := factorycli.IssueOf(sb, a["repo"])
	return n, a[annoBoard], ok && a[annoBoard] != ""
}

// sessionReview is the PR the sandbox reviews, and the board its row is
// on; false for any sandbox but a review's.
func sessionReview(sb *unstructured.Unstructured) (int, string, bool) {
	a := sb.GetAnnotations()
	n, ok := factorycli.ReviewPROf(sb, a["repo"])
	return n, a[annoBoard], ok && a["repo"] != ""
}

// sessionResearch is the research conversation the sandbox is, false for
// any other sandbox.
func sessionResearch(sb *unstructured.Unstructured) (researchSandboxView, bool) {
	view, ok := researchViewFromSandbox(sb)
	return view, ok && !view.Legacy
}

// sessionRevises are the revises task's recipe offers, from its recorded
// run, each with what stands in its way and what its newest Request says.
// A recipe whose draft cannot be revised from here offers them disabled,
// with why.
func (s *Server) sessionRevises(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, task string) []models.WorkAction {
	a := sb.GetAnnotations()
	run, kind, ok := factorycli.SessionRun(a, task)
	if !ok || len(run.Revises) == 0 {
		return nil
	}
	revises := make([]models.WorkAction, 0, len(run.Revises))
	for _, rv := range run.Revises {
		revises = append(revises, models.WorkAction{Verb: "revise", Revise: rv.ID, Label: rv.Label, Enabled: true})
	}
	disable := func(reason string) []models.WorkAction {
		for i := range revises {
			revises[i].Enabled, revises[i].Reason = false, reason
		}
		return revises
	}
	if number, board, ok := sessionIssue(sb); ok {
		if kind == "Change" {
			// The fix's follow-ups push to its PR and answer on it: there
			// must be one, and the fix's sandbox must be idle.
			switch {
			case a[annoTaskState] == "Running":
				return disable("the fix is running")
			case !strings.Contains(a["htmlURL"], "/pull/"):
				return disable("the fix has no PR yet")
			}
			for i := range revises {
				revises[i].Inputs = reviseInputs[revises[i].Revise]
			}
			s.markSandboxRevises(ctx, s.Auth.GetNamespaceFromContext(c), sb.GetName(), revises)
			for i := range revises {
				if revises[i].Reason == planRevisingReason {
					revises[i].Reason = "another follow-up is running"
				}
			}
			return revises
		}
		if kind != "Plan" {
			return disable(fmt.Sprintf("a %s is not revised from here", kind))
		}
		switch {
		case a[annoPlanApproved] != "":
			return disable("the plan is approved")
		case a[annoPlanDraft] == "":
			return disable("there is no plan draft yet")
		}
		if from := factorycli.TaskOutputSession("Plan", a[factorycli.AnnotationPlanOutput]); from != "" && from != task {
			return disable("the plan draft came from another session")
		}
		// As the row offers them: waiting on a running task or a revise.
		for i := range revises {
			if act, ok := findWorkAction(planWorkActions(a, planIsRevising(a), a[annoTaskState] == "Running"), "revise", revises[i].Revise); ok {
				revises[i].Enabled, revises[i].Reason = act.Enabled, act.Reason
			}
		}
		s.markRevises(ctx, s.Auth.GetNamespaceFromContext(c), board, number, revises)
		return revises
	}
	if _, board, ok := sessionReview(sb); ok {
		// The review is GitHub's pending review: an Update review posts
		// over it, so there must be one, and the review must be done.
		switch {
		case kind != "Review":
			return disable(fmt.Sprintf("a %s is not revised from here", kind))
		case a[annoTaskState] == "Running":
			return disable("the review is running")
		case a["reviewState"] == "" || board == "":
			return disable("the review is not on GitHub yet")
		}
		s.markSandboxRevises(ctx, s.Auth.GetNamespaceFromContext(c), sb.GetName(), revises)
		return revises
	}
	view, ok := sessionResearch(sb)
	if !ok || kind != "Notes" {
		return disable("the sandbox has no issue to file it on")
	}
	notes := s.researchNotes(ctx, view)
	for i := range revises {
		switch {
		case notes.Writing:
			revises[i].Enabled, revises[i].Reason = false, revisingReason
		case notes.WriteError != "":
			revises[i].Error = notes.WriteError
		}
	}
	return revises
}

// reviseInputs are the inputs a recipe's revise asks for. factory does not
// record them on the run, so the board knows them: the fix's iterate is
// asked with the member's instruction.
var reviseInputs = map[string][]string{"iterate": {"instruction"}}

// sessionDraft is task's draft, nil when it has none: an issue's plan, or
// a research conversation's notes. A triage's draft is YAML for the board
// row's form, not a document, and stays there.
func (s *Server) sessionDraft(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, task string) *taskSessionDraft {
	a := sb.GetAnnotations()
	_, kind, ok := factorycli.SessionRun(a, task)
	if !ok {
		return nil
	}
	if number, boardName, ok := sessionIssue(sb); ok {
		if kind != "Plan" || a[annoPlanDraft] == "" || a[annoPlanApproved] != "" {
			return nil
		}
		actions := planWorkActions(a, planIsRevising(a), a[annoTaskState] == "Running")
		board, _, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), boardName)
		if err == nil {
			item := &models.WorkItem{PlanActions: actions}
			s.markApplies(ctx, board, map[string]*models.WorkItem{"issue-" + strconv.Itoa(number): item})
			actions = item.PlanActions
		}
		return &taskSessionDraft{Kind: "Plan", Markdown: a[annoPlanDraft], DraftedAt: a[annoPlannedAt], Actions: withoutRevises(actions)}
	}
	view, ok := sessionResearch(sb)
	if !ok || kind != "Notes" || view.Notes.Markdown == "" {
		return nil
	}
	notes := s.researchNotes(ctx, view)
	actions := workActions(factorycli.OfferedActions("Notes", a[factorycli.AnnotationNotesOutput]), func(act factorycli.Action) string {
		switch {
		case notes.Writing && act.Verb != "reject":
			return notesRewritingReason
		case act.Verb == "push-notes" && notes.Saving:
			return postingReason
		}
		return ""
	})
	for i := range actions {
		if actions[i].Verb == "push-notes" && !notes.Saving {
			actions[i].Error = notes.SaveError
		}
	}
	return &taskSessionDraft{
		Kind: "Notes", Markdown: notes.Markdown, DraftedAt: notes.DraftedAt, SavedAt: notes.SavedAt, Note: notes.Note,
		Actions: withoutRevises(actions),
	}
}

// withoutRevises drops the revise actions: a session shows its revises
// apart from its draft.
func withoutRevises(actions []models.WorkAction) []models.WorkAction {
	out := []models.WorkAction{}
	for _, a := range actions {
		if a.Verb != "revise" {
			out = append(out, a)
		}
	}
	return out
}

// asIssueAction points the request at the issue row's action route,
// :verb on #number of board, with body, and takes it there: the row's
// checks and permissions are the session's.
func (s *Server) asIssueAction(c *gin.Context, board string, number int, verb string, body gin.H) {
	c.Params = append(c.Params,
		gin.Param{Key: "board", Value: board},
		gin.Param{Key: "id", Value: strconv.Itoa(number)},
		gin.Param{Key: "verb", Value: verb},
	)
	rebody(c, body)
	s.boardIssueAction(c)
}

// reviseTaskSession is a session's revise button: POST …/revise {revise,
// inputs}. 202 with the Request filed.
func (s *Server) reviseTaskSession(c *gin.Context) {
	sb, task, ok := s.taskSessionSandbox(c)
	if !ok {
		return
	}
	var req struct {
		Revise string            `json:"revise"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Revise == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "revise is required"})
		return
	}
	action, offered := findWorkAction(s.sessionRevises(c.Request.Context(), c, sb, task), "revise", req.Revise)
	switch {
	case !offered:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the session does not offer revise %s", req.Revise)})
		return
	case !action.Enabled:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("cannot revise now: %s", action.Reason)})
		return
	}
	// The inputs are the revise's own, all of them given: the one there
	// is (Iterate's instruction) rides the Request as its Instruction.
	for name := range req.Inputs {
		if !slices.Contains(action.Inputs, name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("revise %s takes no input %s", req.Revise, name)})
			return
		}
	}
	for _, name := range action.Inputs {
		if strings.TrimSpace(req.Inputs[name]) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("revise %s needs %s", req.Revise, name)})
			return
		}
	}
	if _, kind, _ := factorycli.SessionRun(sb.GetAnnotations(), task); kind == "Change" {
		_, board, _ := sessionIssue(sb)
		s.reviseSandbox(c, sb, board, req.Revise, strings.TrimSpace(req.Inputs["instruction"]))
		return
	}
	if number, board, ok := sessionIssue(sb); ok {
		s.asIssueAction(c, board, number, "revise", gin.H{"kind": "Plan", "revise": req.Revise})
		return
	}
	if _, board, ok := sessionReview(sb); ok {
		s.reviseSandbox(c, sb, board, req.Revise, "")
		return
	}
	view, _ := sessionResearch(sb)
	s.saveRecipeNotes(c, view, req.Revise)
}

// reviseSandbox files a revise keyed by its sandbox, as a Save notes is: a
// review's Update review, which the controller posts over the pending
// review, or a fix's follow-up, which pushes to its PR and posts its
// replies there. 202.
func (s *Server) reviseSandbox(c *gin.Context, sb *unstructured.Unstructured, boardName, revise, instruction string) {
	ctx := c.Request.Context()
	board, member, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), boardName)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	filed, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:        boardv1alpha1.VerbRevise,
		Member:      member,
		Sandbox:     sb.GetName(),
		Revise:      revise,
		Instruction: instruction,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the revise", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name})
}

// markSandboxRevises marks revises with the newest revise Request filed for
// the sandbox, as markRevises does an issue's.
func (s *Server) markSandboxRevises(ctx context.Context, namespace, sandbox string, revises []models.WorkAction) {
	reqs, err := s.listRequests(ctx, namespace, v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelVerb + "=" + boardv1alpha1.VerbRevise,
	})
	if err != nil {
		return
	}
	seen := map[string]bool{}
	// Newest first: the newest Request for a revise is the word on it.
	for _, req := range reqs {
		if req.Spec.Sandbox != sandbox || seen[req.Spec.Revise] {
			continue
		}
		seen[req.Spec.Revise] = true
		markRevise(revises, req)
	}
}

// taskSessionDraftAction takes one of the session's draft's actions:
// POST …/draft/:verb {run, text}, text being an edit's new draft.
func (s *Server) taskSessionDraftAction(c *gin.Context) {
	sb, task, ok := s.taskSessionSandbox(c)
	if !ok {
		return
	}
	var req struct {
		Run  string `json:"run"`
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	verb := c.Param("verb")
	draft := s.sessionDraft(c.Request.Context(), c, sb, task)
	if draft == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "the session has no draft"})
		return
	}
	action, offered := findWorkAction(draft.Actions, verb, req.Run)
	switch {
	case !offered:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the draft does not offer %s", verb)})
		return
	case !action.Enabled:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("cannot %s now: %s", verb, action.Reason)})
		return
	}
	if number, board, ok := sessionIssue(sb); ok {
		s.asIssueAction(c, board, number, verb, gin.H{"kind": draft.Kind, "run": req.Run, "text": req.Text})
		return
	}
	view, _ := sessionResearch(sb)
	switch verb {
	case "edit":
		s.editResearchNotes(c, view, req.Text)
	case "reject":
		s.discardResearchNotes(c, view)
	case "push-notes":
		s.saveResearchNotes(c, view)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("notes do not take %s here", verb)})
	}
}
