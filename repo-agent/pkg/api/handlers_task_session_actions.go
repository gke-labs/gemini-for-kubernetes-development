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

// What a task session offers, whichever recipe it is: the recipes of its
// session tag's group, each a run into the same conversation, and the
// draft its task output is, with the output's actions. Clicking one files
// a Request where the draft lives: the issue's or PR's row when the
// sandbox is for one, the sandbox itself when it is not (a research
// conversation). The browser never picks; it posts to the session.

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
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// taskSessionDraft is a session's draft: its task output, as the board
// stores it, with the actions it offers and what the clicks on them say.
type taskSessionDraft struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
	// Spec is the draft of a kind whose draft is not markdown, as YAML: a
	// triage's triage: block, any other's spec. An edit's text is the same.
	Spec      string `json:"spec,omitempty"`
	DraftedAt string `json:"draftedAt,omitempty"`
	// SavedAt is when a Notes draft was last pushed to research/notes, and
	// Note the file it went to.
	SavedAt string `json:"savedAt,omitempty"`
	Note    string `json:"note,omitempty"`
	// AppliedAt is when an overseer's CLI applied the run's result, from
	// the mark it leaves beside it (overseerRunDraft).
	AppliedAt string              `json:"appliedAt,omitempty"`
	Actions   []models.WorkAction `json:"actions"`

	// runKey is the run the draft is the output of; board and number are
	// where its writes are filed (a research conversation's: its board,
	// and no number).
	runKey string
	board  *unstructured.Unstructured
	item   string
	number int
}

// appliedReason is a write's reason once it was applied to the draft.
const appliedReason = "applied"

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

// pendingReviewURL is where a review sandbox's pending review is
// finalized on GitHub, "" when it has none there.
func pendingReviewURL(sb *unstructured.Unstructured) string {
	a := sb.GetAnnotations()
	if _, _, ok := sessionReview(sb); !ok || a["reviewState"] != "pending" || !strings.Contains(a["htmlURL"], "/pull/") {
		return ""
	}
	return strings.TrimSuffix(a["htmlURL"], "/") + "/files"
}

// sessionResearch is the research conversation the sandbox is, false for
// any other sandbox.
func sessionResearch(sb *unstructured.Unstructured) (researchSandboxView, bool) {
	view, ok := researchViewFromSandbox(sb)
	return view, ok && !view.Legacy
}

// sessionGroupActions are the recipes of the group task's recipe is in,
// if it has a session tag (sessionGroup), each with what stands in its
// way and what its newest Request says.
func (s *Server) sessionGroupActions(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, task string) []models.WorkAction {
	run, ok := factorycli.SessionRun(sb.GetAnnotations(), task)
	if !ok {
		return nil
	}
	group, _ := s.sessionGroup(ctx, c, sb, run)
	return group.actions
}

// sessionRecipes is what a session of a recipe group (a session tag)
// offers: every recipe of the group, each a run into the same
// conversation, on the PR whose sandbox it is.
type sessionRecipes struct {
	board   *unstructured.Unstructured
	pr      int
	actions []models.WorkAction
}

// sessionGroup is the group run's recipe is in, if it has a session tag,
// with what stands in the way of each of its recipes and what its newest
// Request says; false for a recipe in none.
func (s *Server) sessionGroup(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, run factorycli.RecordedRun) (sessionRecipes, bool) {
	a := sb.GetAnnotations()
	if a[annoBoard] == "" {
		return sessionRecipes{}, false
	}
	board, _, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), a[annoBoard])
	if err != nil {
		return sessionRecipes{}, false
	}
	catalog, err := boardRecipes(board)
	if err != nil {
		return sessionRecipes{}, false
	}
	tag := recipeSessionTag(catalog, run.Recipe)
	if tag == "" {
		return sessionRecipes{}, false
	}
	group := sessionRecipes{board: board}
	for _, rec := range catalog {
		if rec.Session != tag {
			continue
		}
		action := models.WorkAction{Verb: "recipe", Recipe: rec.Name, Label: recipeLabel(catalog, rec.Name), Inputs: launchInputs(rec), Enabled: true}
		// The recipe named like the session first: New conversation runs it.
		if rec.Name == tag {
			group.actions = append([]models.WorkAction{action}, group.actions...)
		} else {
			group.actions = append(group.actions, action)
		}
	}
	disable := func(reason string) (sessionRecipes, bool) {
		for i := range group.actions {
			group.actions[i].Enabled, group.actions[i].Reason = false, reason
		}
		return group, true
	}
	pr, err := strconv.Atoi(sb.GetLabels()[factorycli.LabelPR])
	switch {
	case err != nil || pr <= 0 || !strings.Contains(a["htmlURL"], "/pull/"):
		return disable("the sandbox has no PR")
	case a[annoTaskState] == "Running":
		return disable(fmt.Sprintf("%s is running", recipeLabel(catalog, run.Recipe)))
	}
	group.pr = pr
	reqs, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelVerb + "=" + boardv1alpha1.VerbRecipe,
	})
	if err != nil {
		return group, true
	}
	seen := map[string]bool{}
	// Newest first: the newest Request for a recipe is the word on it.
	for _, req := range reqs {
		spec := req.Spec
		if spec.Board != board.GetName() || spec.Item != "pr" || spec.Number != pr || recipeSessionTag(catalog, spec.Recipe) != tag || seen[spec.Recipe] {
			continue
		}
		seen[spec.Recipe] = true
		markRecipe(group.actions, req)
	}
	return group, true
}

// sessionDraft is task's draft, nil when it has none: its run's stored
// task output (anyDraft).
func (s *Server) sessionDraft(ctx context.Context, c *gin.Context, sb *unstructured.Unstructured, task string) *taskSessionDraft {
	run, ok := factorycli.SessionRun(sb.GetAnnotations(), task)
	if !ok {
		return nil
	}
	return s.anyDraft(ctx, c, sb, run)
}

// anyDraft is a run's draft, whichever recipe it is (a triage, a plan, a
// fix's Change, a review's Review, a research conversation's Notes, a new
// recipe's): its stored task output, as markdown or its spec as YAML,
// with the writes, follow-ups, edit and reject it offers. A write or a
// follow-up is filed on the board of the issue or PR the sandbox is for,
// or of a research conversation's repository; a sandbox for none offers
// none.
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
	if inOverseerScope(c) {
		// To read: an overseer's runs are applied by its own CLI, and its
		// sandboxes are on no board to file an edit or a write on.
		draft.Actions = []models.WorkAction{}
		return draft
	}
	applied := factorycli.Applied(a, factorycli.AppliedAnnotation(run.Key))
	if ok {
		draft.board, _, _ = s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), boardName)
		draft.item, draft.number = item, number
	} else if view, isResearch := sessionResearch(sb); isResearch {
		// Notes are saved to the member's fork of the conversation's
		// repository, under the file name it pins at the first save.
		draft.board, _ = s.researchBoard(ctx, view)
		draft.Note, draft.SavedAt = view.Note, applied["push-notes"]
	}
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
		if view, ok := sessionResearch(sb); ok && verb == "push-notes" {
			// Pinned at the first save, so a second one updates the file.
			note := s.researchNoteName(ctx, view, s.Auth.GetUserFromContext(c))
			if err := s.updateResearchAnnotations(ctx, view.Namespace, view.Sandbox, map[string]string{research.NoteAnnotation: note}); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to pin the note's name", "details": err.Error()})
				return
			}
		}
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

// taskSessionRecipe runs one of the session's group's recipes into its
// conversation: POST …/recipe {recipe, inputs, newSession}. 202 with the
// Request filed.
func (s *Server) taskSessionRecipe(c *gin.Context) {
	sb, task, ok := s.taskSessionSandbox(c)
	if !ok {
		return
	}
	var req struct {
		Recipe string            `json:"recipe"`
		Inputs map[string]string `json:"inputs"`
		// NewSession runs it in a new conversation of the session.
		NewSession bool `json:"newSession"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Recipe == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recipe is required"})
		return
	}
	action, offered := findWorkAction(s.sessionGroupActions(c.Request.Context(), c, sb, task), "recipe", req.Recipe)
	switch {
	case !offered:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the session does not offer %s", req.Recipe)})
		return
	case !action.Enabled:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("cannot run %s now: %s", req.Recipe, action.Reason)})
		return
	}
	s.runSessionRecipe(c, sb, task, action, req.Inputs, req.NewSession)
}

// runSessionRecipe files a launch of one of the session's group's recipes
// on its PR, as the row's button does. 202.
func (s *Server) runSessionRecipe(c *gin.Context, sb *unstructured.Unstructured, task string, action models.WorkAction, given map[string]string, newSession bool) {
	ctx := c.Request.Context()
	run, _ := factorycli.SessionRun(sb.GetAnnotations(), task)
	group, _ := s.sessionGroup(ctx, c, sb, run)
	inputs := map[string]string{}
	for name, value := range given {
		if !slices.Contains(action.Inputs, name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s takes no input %s", action.Recipe, name)})
			return
		}
		inputs[name] = strings.TrimSpace(value)
	}
	for _, name := range action.Inputs {
		if inputs[name] == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s needs %s", action.Recipe, name)})
			return
		}
	}
	if len(inputs) == 0 {
		inputs = nil
	}
	_, member, err := s.resolveBoard(ctx, s.Auth.GetNamespaceFromContext(c), s.Auth.GetUserFromContext(c), group.board.GetName())
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	filed, err := s.fileRequest(ctx, group.board, boardv1alpha1.RequestSpec{
		Verb:       boardv1alpha1.VerbRecipe,
		Recipe:     action.Recipe,
		Item:       "pr",
		Member:     member,
		Number:     group.pr,
		Inputs:     inputs,
		NewSession: newSession,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the run", "details": err.Error()})
		return
	}
	invalidateWorkFeed(group.board.GetNamespace(), group.board.GetName())
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name})
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
	s.anyDraftAction(c, sb, draft, verb, req.Run, req.Text)
}
