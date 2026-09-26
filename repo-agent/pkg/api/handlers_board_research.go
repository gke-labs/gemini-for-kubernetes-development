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
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// startResearchSession opens a deep-research conversation about the
// board's repository: a sandbox running the agent under acpd, which the
// member then talks to.
//
// Creation goes through the mailbox because making a sandbox means
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
// claim and the controller sends it.
func (s *Server) startResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}

	// An empty body is the plain "New conversation" click, which is how
	// this endpoint was called before kickoffs existed.
	var kickoff research.Kickoff
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&kickoff); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
			return
		}
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

	annotations := board.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	requests := map[string]string{}
	if raw := annotations[annoBoardRequests]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &requests)
	}
	// Member namespace, click time, and the opening turn when there is
	// one. The controller drops this once the sandbox exists, and drops
	// it unserved after its TTL.
	requests["research-"+sessionID] = research.Claim{
		Member:  namespace,
		At:      time.Now().UTC(),
		Kickoff: kickoff,
	}.Encode()
	buf, _ := json.Marshal(requests)
	annotations[annoBoardRequests] = string(buf)
	board.SetAnnotations(annotations)
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
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
