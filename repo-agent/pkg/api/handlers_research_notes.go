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

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// Saving a recipe research conversation's notes: 💾 files a revise (the
// recipe's notes revise, asked into the conversation's session) and the
// controller stores the Notes it writes on the sandbox as a draft; Save to
// research/notes files an apply (push-notes), which factory runs with the
// member's token. Both Requests carry the sandbox instead of an issue
// number. The draft can be edited or discarded in between, as a plan's.

// The notes draft on a research sandbox, as the controller writes it.
const (
	annoNotesDraft     = "board.gemini.google.com/notes"
	annoNotesDraftedAt = "board.gemini.google.com/notes-drafted-at"
	annoNotesSaved     = "board.gemini.google.com/notes-saved-at"
)

// notesRevise is the research recipe's Save notes revise.
const notesRevise = "notes"

// researchNotesDraft is a conversation's notes draft.
type researchNotesDraft struct {
	Markdown  string `json:"markdown,omitempty"`
	DraftedAt string `json:"draftedAt,omitempty"`
	// SavedAt is when this draft was last pushed to research/notes.
	SavedAt string `json:"savedAt,omitempty"`
}

// researchNotesState is the draft with what the clicks on it say: a Save
// notes still writing, a save still pushing, and why the last of each
// failed.
type researchNotesState struct {
	researchNotesDraft
	// Note is the file name the notes are saved under, once pinned.
	Note       string `json:"note,omitempty"`
	Writing    bool   `json:"writing,omitempty"`
	WriteError string `json:"writeError,omitempty"`
	Saving     bool   `json:"saving,omitempty"`
	SaveError  string `json:"saveError,omitempty"`
}

// researchNotes is view's notes, with the newest revise and apply Request
// filed for its sandbox. A Request list that fails reads as no clicks.
func (s *Server) researchNotes(ctx context.Context, view researchSandboxView) researchNotesState {
	state := researchNotesState{researchNotesDraft: view.Notes, Note: view.Note}
	reqs, err := s.listRequests(ctx, view.Namespace, v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelVerb + " in (" + boardv1alpha1.VerbApply + "," + boardv1alpha1.VerbRevise + ")",
	})
	if err != nil {
		klog.V(2).Infof("research: cannot list the notes requests of %s: %v", view.Sandbox, err)
		return state
	}
	seen := map[string]bool{}
	// Newest first: the newest Request of each verb is the word on it.
	for _, req := range reqs {
		if req.Spec.Sandbox != view.Sandbox || seen[req.Spec.Verb] {
			continue
		}
		seen[req.Spec.Verb] = true
		failed := ""
		if req.Status.Phase == boardv1alpha1.RequestFailed {
			failed = req.Status.Message
		}
		if req.Spec.Verb == boardv1alpha1.VerbRevise {
			state.Writing, state.WriteError = req.Active(), failed
		} else {
			state.Saving, state.SaveError = req.Active(), failed
		}
	}
	return state
}

// researchBoard is the board a research sandbox is reconciled by: the
// member's board for its repository, as the controller matches them.
func (s *Server) researchBoard(ctx context.Context, view researchSandboxView) (*unstructured.Unstructured, error) {
	owner, repo, err := parseRepoURL(view.HTMLURL)
	if err != nil {
		return nil, fmt.Errorf("the sandbox names no repository: %w", err)
	}
	for _, board := range s.visibleBoards(ctx, view.Namespace, "") {
		repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
		if o, r, err := parseRepoURL(repoURL); err == nil && strings.EqualFold(o, owner) && strings.EqualFold(r, repo) {
			return &board, nil
		}
	}
	return nil, fmt.Errorf("no board in %s for %s/%s", view.Namespace, owner, repo)
}

// saveRecipeNotes is 💾 on a recipe's conversation: pin the note's file
// name, then file the revise that writes the draft. 202; the draft lands
// on the sandbox when the revise ends.
func (s *Server) saveRecipeNotes(c *gin.Context, view researchSandboxView) {
	ctx := c.Request.Context()
	board, err := s.researchBoard(ctx, view)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	note := s.researchNoteName(ctx, view, s.Auth.GetUserFromContext(c))
	if err := s.updateResearchAnnotations(ctx, view.Namespace, view.Sandbox, map[string]string{research.NoteAnnotation: note}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to pin the note's name", "details": err.Error()})
		return
	}
	filed, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbRevise,
		Member:  view.Namespace,
		Sandbox: view.Sandbox,
		Revise:  notesRevise,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file Save notes", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"sessionId": view.SessionID, "note": note, "request": filed.Name})
}

// recipeNotesView is the recipe conversation a notes route acts on, or
// false with the answer written. A paused sandbox is fine: these touch
// only its annotations.
func (s *Server) recipeNotesView(c *gin.Context) (researchSandboxView, bool) {
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return researchSandboxView{}, false
	}
	view, found, err := s.findResearchSandbox(c.Request.Context(), s.Auth.GetNamespaceFromContext(c), sessionID)
	switch {
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return researchSandboxView{}, false
	case !found:
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return researchSandboxView{}, false
	case view.Task == "":
		c.JSON(http.StatusConflict, gin.H{"error": researchLegacyMessage, "legacy": true})
		return researchSandboxView{}, false
	}
	return view, true
}

// saveResearchNotes is Save to research/notes: files the push of the draft
// as it is now. 202, as for any write.
func (s *Server) saveResearchNotes(c *gin.Context) {
	view, ok := s.recipeNotesView(c)
	if !ok {
		return
	}
	if view.Notes.Markdown == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "there are no notes to save — write them with Save notes first"})
		return
	}
	ctx := c.Request.Context()
	board, err := s.researchBoard(ctx, view)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	filed, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbApply,
		Member:  view.Namespace,
		Sandbox: view.Sandbox,
		Apply:   &boardv1alpha1.ApplyRequest{Kind: "Notes", Action: "push-notes"},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to file the save", "details": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": filed.Name, "note": view.Note})
}

// editResearchNotes replaces the draft with the member's edit: what Save
// to research/notes pushes from then on.
func (s *Server) editResearchNotes(c *gin.Context) {
	view, ok := s.recipeNotesView(c)
	if !ok {
		return
	}
	var req struct {
		Markdown string `json:"markdown"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Markdown) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the notes are empty — discard them instead"})
		return
	}
	if view.Notes.Markdown == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "there are no notes to edit"})
		return
	}
	if err := s.updateResearchAnnotations(c.Request.Context(), view.Namespace, view.Sandbox, map[string]string{
		annoNotesDraft: strings.TrimSpace(req.Markdown),
		annoNotesSaved: "",
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store the notes", "details": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// discardResearchNotes drops the draft. What was saved to research/notes
// stays there.
func (s *Server) discardResearchNotes(c *gin.Context) {
	view, ok := s.recipeNotesView(c)
	if !ok {
		return
	}
	if err := s.updateResearchAnnotations(c.Request.Context(), view.Namespace, view.Sandbox, map[string]string{
		annoNotesDraft:                   "",
		annoNotesDraftedAt:               "",
		annoNotesSaved:                   "",
		factorycli.AnnotationNotesOutput: "",
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to discard the notes", "details": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
