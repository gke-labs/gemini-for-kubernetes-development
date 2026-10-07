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
	"time"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// taskSessionDraft is a session's draft: its task output, as the board
// stores it, with the actions it offers and what the clicks on them say.
// The output's revises are not among them; they are the session's.
type taskSessionDraft struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
	// Spec is the draft of a kind whose draft is not markdown, as YAML: a
	// triage's triage: block, any other's spec. An edit's text is the same.
	Spec      string `json:"spec,omitempty"`
	DraftedAt string `json:"draftedAt,omitempty"`
	// SavedAt is when a Notes draft was last pushed to research/notes, and
	// Note the file it went to.
	SavedAt string              `json:"savedAt,omitempty"`
	Note    string              `json:"note,omitempty"`
	Actions []models.WorkAction `json:"actions"`

	// runKey is the run the draft is the output of, for a kind the board
	// keeps no draft of its own for (anyDraft); board and number are
	// where its writes are filed.
	runKey string
	board  *unstructured.Unstructured
	item   string
	number int
}

// appliedReason is a write's reason once it was applied to the draft.
const appliedReason = "applied"

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

// sessionPR is the PR the sandbox is the fix sandbox factory made for, and
// the board its row is on; false for any other sandbox.
func sessionPR(sb *unstructured.Unstructured) (int, string, bool) {
	a := sb.GetAnnotations()
	n, ok := factorycli.PRSandboxOf(sb, a["repo"])
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
	run, ok := factorycli.SessionRun(a, task)
	if !ok || len(run.Revises) == 0 {
		return nil
	}
	kind := run.Kind
	revises := reviseActions(run)
	disable := func(reason string) []models.WorkAction {
		for i := range revises {
			revises[i].Enabled, revises[i].Reason = false, reason
		}
		return revises
	}
	_, _, onIssue := sessionIssue(sb)
	_, _, onPR := sessionPR(sb)
	if onIssue || onPR {
		if kind == "Change" {
			// The fix's follow-ups push to its PR and answer on it: there
			// must be one, and the fix's sandbox must be idle.
			switch {
			case a[annoTaskState] == "Running":
				return disable("the fix is running")
			case !strings.Contains(a["htmlURL"], "/pull/"):
				return disable("the fix has no PR yet")
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
		doc := a[factorycli.OutputAnnotation(run.Key)]
		switch {
		case factorycli.IsApplied(a, factorycli.AppliedAnnotation(run.Key), "run"):
			return disable("its follow-up was started")
		case factorycli.Draft(kind, doc) == "":
			return disable("there is no draft yet")
		case a[annoTaskState] == "Running":
			return disable("a task is running")
		}
		if from := factorycli.TaskOutputSession(kind, doc); from != "" && from != task {
			return disable("the draft came from another session")
		}
		s.markSandboxRevises(ctx, s.Auth.GetNamespaceFromContext(c), sb.GetName(), revises)
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

// reviseActions are run's revises as buttons, with the inputs each asks
// for.
func reviseActions(run factorycli.RecordedRun) []models.WorkAction {
	revises := make([]models.WorkAction, 0, len(run.Revises))
	for _, rv := range run.Revises {
		revises = append(revises, models.WorkAction{Verb: "revise", Revise: rv.ID, Label: rv.Label, Inputs: rv.Inputs, Enabled: true})
	}
	return revises
}

// sessionDraft is task's draft, nil when it has none: a research
// conversation's notes, and any other run's stored task output
// (anyDraft).
func (s *Server) sessionDraft(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, task string) *taskSessionDraft {
	a := sb.GetAnnotations()
	run, ok := factorycli.SessionRun(a, task)
	if !ok {
		return nil
	}
	kind := run.Kind
	view, ok := sessionResearch(sb)
	if !ok {
		return s.anyDraft(ctx, c, sb, run)
	}
	if kind != "Notes" || view.Notes.Markdown == "" {
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

// anyDraft is a run's draft, whichever recipe it is (a triage, a plan, a
// fix's Change, a review's Review, a new recipe's): its stored task
// output, as markdown or its spec as YAML, with the writes, follow-ups,
// edit and reject it offers. A write or a follow-up is filed on the
// board of the issue or PR the sandbox is for; a sandbox for neither
// offers none.
func (s *Server) anyDraft(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, run factorycli.RecordedRun) *taskSessionDraft {
	a := sb.GetAnnotations()
	doc := a[factorycli.OutputAnnotation(run.Key)]
	kind := factorycli.TaskOutputKind(doc)
	text := factorycli.Draft(kind, doc)
	if kind == "" || text == "" {
		return nil
	}
	draft := &taskSessionDraft{Kind: kind, runKey: run.Key}
	if factorycli.DraftIsMarkdown(kind, doc) {
		draft.Markdown = text
	} else {
		draft.Spec = text
	}
	if run.EndedAt != nil {
		draft.DraftedAt = run.EndedAt.UTC().Format(time.RFC3339)
	}
	item := "issue"
	number, boardName, ok := sessionIssue(sb)
	if !ok {
		item = "pr"
		number, boardName, ok = sessionReview(sb)
	}
	if !ok {
		number, boardName, ok = sessionPR(sb)
	}
	if ok {
		draft.board, _, _ = s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), boardName)
		draft.item, draft.number = item, number
	}
	applied := factorycli.Applied(a, factorycli.AppliedAnnotation(run.Key))
	var offered []factorycli.Action
	for _, act := range factorycli.OfferedActions(kind, doc) {
		if act.Verb == "edit" || act.Verb == "reject" || ((factorycli.IsApplyVerb(act.Verb) || act.Verb == "run") && draft.board != nil) {
			offered = append(offered, act)
		}
	}
	draft.Actions = workActions(offered, func(act factorycli.Action) string {
		switch {
		case applied[act.Verb] != "":
			return appliedReason
		case run.State == "Running" && act.Verb != "reject":
			return "the run is running"
		}
		return ""
	})
	if draft.board != nil {
		repoURL, _, _ := unstructured.NestedString(draft.board.Object, "spec", "repoURL")
		draft.Actions = actionsFor(draft.Actions, s.repoPermissions(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), repoURL))
		s.markRunApplies(ctx, draft.board, sb.GetName(), factorycli.RunTaskType(run.Key), draft.Actions)
	}
	if draft.Actions == nil {
		draft.Actions = []models.WorkAction{}
	}
	return draft
}

// markRunApplies says on a run's draft's actions what the apply Requests
// filed on it say, as markApplies does on a row's.
func (s *Server) markRunApplies(ctx context.Context, board *unstructured.Unstructured, sandbox, run string, actions []models.WorkAction) {
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
		if spec.Apply == nil || spec.Sandbox != sandbox || spec.Apply.Run != run || seen[spec.Apply.Action] {
			continue
		}
		seen[spec.Apply.Action] = true
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

// anyDraftAction takes one of an anyDraft's actions: an edit rewrites its
// spec (or markdown), and makes it unapplied, a reject discards it — a review's abandons its
// pending review, as the PR row's Abandon does — a write is filed on its
// run, and a run starts the recipe it names with the draft.
func (s *Server) anyDraftAction(c *gin.Context, sb *unstructured.Unstructured, draft *taskSessionDraft, verb, run, text string) {
	ctx := c.Request.Context()
	ns := s.Auth.GetNamespaceFromContext(c)
	switch {
	case verb == "edit":
		if draft.Kind == "Triage" {
			// What label and comment act on: refused up front if they
			// could not.
			text = factorycli.NormalizeTriageDraft(text)
			if err := validateTriageDraft(text); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "draft does not match the triage schema", "details": err.Error()})
				return
			}
		}
		if err := s.editDraft(ctx, ns, sb, draft.Kind, factorycli.OutputAnnotation(draft.runKey), text); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// An edited draft is a new one: what was applied was the old.
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.AppliedAnnotation(draft.runKey), ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset what was applied", "details": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"edited": true})
	case verb == "reject" && draft.Kind == "Review" && draft.board != nil:
		c.Params = append(c.Params,
			gin.Param{Key: "board", Value: draft.board.GetName()},
			gin.Param{Key: "id", Value: strconv.Itoa(draft.number)},
		)
		s.abandonBoardReview(c)
	case verb == "reject":
		for _, key := range []string{factorycli.OutputAnnotation(draft.runKey), factorycli.AppliedAnnotation(draft.runKey)} {
			if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), key, ""); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to discard the draft", "details": err.Error()})
				return
			}
		}
		// The tombstone keeps a pass that starts the recipe on its own
		// (auto-triage) from redoing what the member threw away.
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, ns, sb.GetName(), factorycli.RejectedAnnotation(draft.runKey), nowRFC3339()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to discard the draft", "details": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"discarded": true})
	case verb == "run" && draft.board != nil:
		s.runFollowUp(c, sb, draft, run)
	case factorycli.IsApplyVerb(verb) && draft.board != nil:
		s.fileApply(c, draft.board, sb.GetName(), draft.number, factorycli.RunTaskType(draft.runKey), verb)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the %s does not take %s here", strings.ToLower(draft.Kind), verb)})
	}
}

// runFollowUp is a draft's run: <recipe>: the recipe, on the draft's
// issue or PR, with the draft as it stands as the input the recipe takes
// from its kind (fix's plan, from: Plan), filed as the launch click is.
// Once filed, the run is stamped applied: the draft has gone where it was
// going.
func (s *Server) runFollowUp(c *gin.Context, sb *unstructured.Unstructured, draft *taskSessionDraft, recipe string) {
	recipes, err := boardRecipes(draft.board)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid recipes on board", "details": err.Error()})
		return
	}
	input := ""
	if i := slices.IndexFunc(recipes, func(rec boardv1alpha1.BoardRecipe) bool { return rec.Name == recipe }); i >= 0 {
		for _, in := range recipes[i].Inputs {
			if in.From == draft.Kind {
				input = in.Name
			}
		}
	}
	if input == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the board's %s takes no %s", recipe, strings.ToLower(draft.Kind))})
		return
	}
	text := draft.Markdown
	if text == "" {
		text = draft.Spec
	}
	c.Params = append(c.Params,
		gin.Param{Key: "board", Value: draft.board.GetName()},
		gin.Param{Key: "id", Value: strconv.Itoa(draft.number)},
	)
	s.kickoff(c, draft.item, recipe, map[string]string{input: text})
	if c.Writer.Status() != http.StatusOK {
		return
	}
	if err := s.markApplied(c.Request.Context(), s.Auth.GetNamespaceFromContext(c), sb, factorycli.AppliedAnnotation(draft.runKey), "run"); err != nil {
		klog.FromContext(c.Request.Context()).Error(err, "the follow-up is filed; stamping its run applied failed", "sandbox", sb.GetName())
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
	// The inputs are the revise's own, all of them given.
	inputs := map[string]string{}
	for name, value := range req.Inputs {
		if !slices.Contains(action.Inputs, name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("revise %s takes no input %s", req.Revise, name)})
			return
		}
		inputs[name] = strings.TrimSpace(value)
	}
	for _, name := range action.Inputs {
		if inputs[name] == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("revise %s needs %s", req.Revise, name)})
			return
		}
	}
	if view, ok := sessionResearch(sb); ok {
		s.saveRecipeNotes(c, view, req.Revise)
		return
	}
	boardName := sb.GetAnnotations()[annoBoard]
	number, _, _ := sessionIssue(sb)
	if _, board, ok := sessionReview(sb); ok {
		boardName = board
	}
	s.reviseSandbox(c, sb, boardName, number, req.Revise, inputs)
}

// reviseSandbox files a revise keyed by its sandbox, whichever recipe's:
// the controller runs it in the session of the run that offers it. An
// issue's sandbox names the issue too, so its row follows the revise.
// 202.
func (s *Server) reviseSandbox(c *gin.Context, sb *unstructured.Unstructured, boardName string, number int, revise string, inputs map[string]string) {
	ctx := c.Request.Context()
	board, member, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), boardName)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	if len(inputs) == 0 {
		inputs = nil
	}
	filed, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbRevise,
		Member:  member,
		Sandbox: sb.GetName(),
		Number:  number,
		Revise:  revise,
		Inputs:  inputs,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the revise", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name})
}

// markSandboxRevises says on a session's revises what the revise Requests
// filed for its sandbox say: one standing is "revising", and the last one
// failed carries why. This view is where a revise is clicked, so it is
// where it is followed.
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
	if draft.runKey != "" {
		s.anyDraftAction(c, sb, draft, verb, req.Run, req.Text)
		return
	}
	view, _ := sessionResearch(sb)
	switch {
	case verb == "edit":
		s.editResearchNotes(c, view, req.Text)
	case verb == "reject":
		s.discardResearchNotes(c, view)
	case factorycli.IsApplyVerb(verb):
		s.applyResearchNotes(c, view, verb)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("notes do not take %s here", verb)})
	}
}
