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
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// getResearchPrompts hands back the canned openings, rendered for this
// board's repository.
//
// For a caller that wants to put one in front of the member rather than
// run it: the landing pane's `overview` fills the ask box with this
// text, which you then read, edit and send like anything else you would
// have typed. A canned read used to be a button that started a session
// around a prompt nobody was shown — which is a strange thing to pay
// minutes of sandbox for sight unseen, and left "why did it answer in
// that shape?" unanswerable from the UI.
//
// The text stays where it was, in pkg/research's embedded templates, and
// is rendered by the same Kickoff.Prompt the controller calls. A copy in
// the UI would be the copy that drifts, and the drift would show up as
// an answer that came back shaped oddly with nothing on screen to
// explain it.
//
// Read-only and derived from the board, so it needs nothing but the
// access check every other board route makes.
func (s *Server) getResearchPrompts(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}
	// The same URL the controller builds when it sends a kickoff itself.
	htmlURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	prompts := map[string]string{}
	for _, kind := range []string{research.KindOnboard, research.KindActivity} {
		text, err := (research.Kickoff{Kind: kind}).Prompt(repo, htmlURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render the canned prompts", "details": err.Error()})
			return
		}
		prompts[kind] = text
	}
	c.JSON(http.StatusOK, gin.H{"prompts": prompts})
}

// startResearchSession opens a deep-research conversation about the
// board's repository: a sandbox running the agent under acpd, which the
// member then talks to.
//
// Creation goes through a Request because making a sandbox means
// running the factory CLI, and only the controller's image carries it —
// this API server is distroless with nothing in it but itself. The
// conversation that follows does not: acpd is plain HTTP on the pod,
// which this process can reach directly, so nothing after this click
// touches the board.
//
// Asynchronous by necessity — an image pull, a PVC and a clone is
// minutes — so the response is the session's identity and the name of
// the sandbox that will appear, not a running session.
//
// An optional body carries a kickoff: the canned openings (overview,
// what happened) and the topic box all land here, because a canned
// session is an ordinary session whose first prompt someone else typed.
// The prompt itself is not sent from this handler — the sandbox will not
// exist for minutes, and by then the click is long over. It rides on the
// Request and the controller sends it.
func (s *Server) startResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	// A conversation starts with a question: `factory recipe research`
	// asks it as the task, and there is no task without one.
	var kickoff research.Kickoff
	if err := c.ShouldBindJSON(&kickoff); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	if kickoff.Kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a research conversation needs a question"})
		return
	}
	if err := kickoff.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}

	// The id is minted here rather than by the caller: it names the
	// sandbox, so a caller-chosen id would let one member address
	// another's session by guessing it, and a repeated one would
	// silently join an existing conversation.
	sessionID := uuid.NewString()

	// Member namespace, click time (the Request's own creation stamp),
	// and the opening turn when there is one. The controller settles
	// this once the sandbox exists, and fails it after its TTL.
	if _, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:   boardv1alpha1.VerbResearch,
		Member: namespace,
		Research: &boardv1alpha1.ResearchRequest{
			SessionID: sessionID,
			Kind:      kickoff.Kind,
			Topic:     kickoff.Topic,
			Since:     kickoff.Since,
			Title:     kickoff.Title,
		},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record research request", "details": err.Error()})
		return
	}

	// The sandbox name is derived, not reported back by factory, so the
	// caller can poll for it before anything has been created.
	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"sandbox":   factorycli.ResearchSandboxName(repo, sessionID),
		"namespace": namespace,
		"repo":      repo,
		"title":     kickoff.ResolvedTitle(),
	})
}
