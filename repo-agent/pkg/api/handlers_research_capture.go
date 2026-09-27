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

// Turning a conversation into a note on the fork.
//
// Two steps with a wait between them, and they are split across two
// processes for the same reason everything else here is. Asking the
// conversation to write the note is a prompt, and this process can send
// prompts — so it does, immediately, and the member watches it happen in
// the terminal they are already looking at. Pushing what it wrote needs
// the factory CLI and the member's GitHub token, and it cannot start
// until the turn finishes, which is minutes away. That half is left as
// an annotation on the sandbox for the controller to pick up: the API is
// replicated and stateless, so a wait held in memory here would not
// survive the replica that served the request going away.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// captureResearchNotes asks the conversation to write part of itself
// down, and records that the result is owed a push.
//
// The prompt is sent from here rather than handed to the controller with
// the rest of the work. A kickoff can wait a reconcile because nobody is
// watching an empty session yet; this one is a reply to something the
// member just did, in a conversation they have open, and up to a minute
// of nothing happening reads as a lost click.
func (s *Server) captureResearchNotes(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	var req struct {
		What string `json:"what"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "what and note are required"})
		return
	}
	// An unnamed note goes to the session's one document, which is what
	// a member who never thinks about this gets and wants.
	if req.Note == "" {
		req.Note = research.DefaultNote
	}
	note, err := research.NormaliseNote(req.Note)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	capture := research.Capture{What: req.What, Note: note}
	prompt, err := capture.Prompt(conn.view.SessionID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	if _, err := s.ensureResearchSession(ctx, conn); err != nil {
		researchError(c, err)
		return
	}
	// Stamped before the prompt is sent, not after.
	//
	// The two orders fail differently and only one of them is
	// recoverable. Annotate-then-prompt can leave a pending save for a
	// turn that never happened: the controller waits for a session that
	// is not busy, finds one, pushes whatever the directory already held
	// — at worst a no-op, and the script exits 1 on an empty one.
	// Prompt-then-annotate can leave a turn that writes a note nobody
	// ever pushes, which looks to the member exactly like the feature
	// not working.
	if err := s.setResearchPending(ctx, conn.view.Namespace, conn.view.Sandbox, research.Pending{
		Note: note,
		At:   time.Now().UTC(),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record the pending save", "details": err.Error()})
		return
	}

	offset, err := conn.client.Prompt(ctx, conn.view.SessionID, prompt)
	if err != nil {
		// Undo the stamp. A 409 here is the common case — the member
		// asked while a turn was in flight — and leaving a pending save
		// behind would have the controller push for a capture that was
		// refused.
		if cerr := s.clearResearchPending(ctx, conn.view.Namespace, conn.view.Sandbox); cerr != nil {
			klog.V(2).Infof("research: could not unstamp the pending save on %s: %v", conn.view.Sandbox, cerr)
		}
		researchError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": conn.view.SessionID,
		"note":      note,
		"path":      capture.Path(conn.view.SessionID),
		"offset":    offset,
	})
}

// getResearchNotes lists the notes already saved for one session.
//
// Read from the branch rather than from the sandbox's checkout, because
// the branch is what the member can actually go and read, and a note
// that has not been pushed yet is not one they can point anyone at. The
// cost is that a note written by a turn whose save has not landed is
// missing from this list for a minute or so; naming it again in the form
// is harmless, since a capture into an existing file updates it.
//
// Not behind resolveResearch: the notes of a paused or still-booting
// session are exactly as readable as any other session's, and that path
// refuses both.
func (s *Server) getResearchNotes(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	member := s.Auth.GetUserFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return
	}
	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return
	}

	out := gin.H{
		"sessionId":   sessionID,
		"forkOwner":   member,
		"branch":      notesBranch,
		"dir":         research.NotesDir(sessionID),
		"defaultNote": research.DefaultNote,
		"notes":       []gin.H{},
	}
	token, terr := s.memberToken(ctx, namespace)
	if terr != nil {
		// No token is not "no notes": it is "we cannot say". The form
		// still works — the member types a name — and reporting an empty
		// list would tell them their saved notes had vanished.
		out["unreadable"] = terr.Error()
		c.JSON(http.StatusOK, out)
		return
	}
	notes, err := researchNotesOnBranch(ctx, githubClientForToken(ctx, token), member, view.Repo, sessionID)
	if err != nil {
		out["unreadable"] = err.Error()
		c.JSON(http.StatusOK, out)
		return
	}
	out["notes"] = notes
	c.JSON(http.StatusOK, out)
}

// researchNotesOnBranch reads one session's directory on the notes
// branch. A directory that is not there yet is not an error — it is
// every session before its first save.
func researchNotesOnBranch(ctx context.Context, gh *github.Client, owner, repo, sessionID string) ([]gin.H, error) {
	_, entries, resp, err := gh.Repositories.GetContents(ctx, owner, repo, research.NotesDir(sessionID),
		&github.RepositoryContentGetOptions{Ref: notesBranch})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return []gin.H{}, nil
		}
		return nil, err
	}
	notes := []gin.H{}
	for _, e := range entries {
		if e.GetType() != "file" {
			continue
		}
		notes = append(notes, gin.H{
			"name":    e.GetName(),
			"path":    e.GetPath(),
			"size":    e.GetSize(),
			"htmlUrl": e.GetHTMLURL(),
		})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i]["name"].(string) < notes[j]["name"].(string) })
	return notes, nil
}

// setResearchPending records that a save is owed, and clears any earlier
// failure so a fresh attempt is not reported as the old one's error.
func (s *Server) setResearchPending(ctx context.Context, namespace, sandboxName string, pending research.Pending) error {
	encoded := pending.Encode()
	if encoded == "" {
		return fmt.Errorf("could not encode the pending save")
	}
	return s.updateResearchAnnotations(ctx, namespace, sandboxName, map[string]string{
		research.CaptureAnnotation:      encoded,
		research.CaptureErrorAnnotation: "",
	})
}

// clearResearchPending removes an owed save.
func (s *Server) clearResearchPending(ctx context.Context, namespace, sandboxName string) error {
	return s.updateResearchAnnotations(ctx, namespace, sandboxName, map[string]string{
		research.CaptureAnnotation: "",
	})
}

// updateResearchAnnotations applies a set of annotation writes to the
// sandbox, an empty value meaning delete. It is a no-op — and costs no
// write — when nothing would change.
func (s *Server) updateResearchAnnotations(ctx context.Context, namespace, sandboxName string, set map[string]string) error {
	sandboxes := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace)
	sb, err := sandboxes.Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	changed := false
	for key, value := range set {
		if annotations[key] == value {
			continue
		}
		if value == "" {
			delete(annotations, key)
		} else {
			annotations[key] = value
		}
		changed = true
	}
	if !changed {
		return nil
	}
	sb.SetAnnotations(annotations)
	_, err = sandboxes.Update(ctx, sb, v1.UpdateOptions{})
	return err
}
