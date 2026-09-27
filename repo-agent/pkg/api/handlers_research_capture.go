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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// captureResearchNotes asks the conversation to write itself down, and
// records that the result is owed a push.
//
// No inputs. The note is the session's one document and what goes in it
// is the conversation, both of which this end already knows — so the
// member's whole part in this is one click, and the form that used to
// ask them which file and which part is gone. A member who wants
// something narrower has a better way of saying so than a text box:
// they can say it to the agent, in the conversation, and then click
// save.
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
	// Every field is optional and the UI sends none of them, so a body
	// that will not parse is not a failure — it is the empty body the
	// button posts. `what` survives for a caller that does have
	// something specific to ask for.
	var req struct {
		What string `json:"what"`
	}
	_ = c.ShouldBindJSON(&req)

	ctx := c.Request.Context()
	capture := research.Capture{
		Note: s.researchNoteName(ctx, conn.view, s.Auth.GetUserFromContext(c)),
		What: req.What,
	}
	prompt, err := capture.Prompt()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := s.ensureResearchSession(ctx, conn); err != nil {
		researchError(c, err)
		return
	}
	// Stamped before the prompt is sent, not after.
	//
	// The two orders fail differently and only one of them is
	// recoverable. Annotate-then-prompt can leave a pending save for a
	// turn that never happened: the controller waits for a session that
	// is not busy, finds one, pushes whatever the file already held —
	// at worst a no-op, and the script exits 1 if there is no file.
	// Prompt-then-annotate can leave a turn that writes a note nobody
	// ever pushes, which looks to the member exactly like the feature
	// not working.
	if err := s.setResearchPending(ctx, conn.view.Namespace, conn.view.Sandbox, research.Pending{
		Note: capture.Note,
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
		"note":      capture.Note,
		"path":      capture.Path(),
		"offset":    offset,
	})
}

// researchNoteName is the file this session's notes go in.
//
// Decided once, at the first capture, and read off the sandbox every
// time after that. The name is the member's — a session called "where
// the retry loop terminates" saves to
// `where-the-retry-loop-terminates.md` — which is the whole point: the
// file is the note's address on a branch someone will read months from
// now, and a session id is not an address, it is a handle.
//
// Pinning is what makes that safe. A title can be changed, and a name
// derived fresh each time would follow it, stranding the note already
// pushed under the old one. So the first save fixes it.
func (s *Server) researchNoteName(ctx context.Context, view researchSandboxView, member string) string {
	if view.Note != "" {
		return view.Note
	}
	base := research.NoteName(view.Title, view.SessionID)
	return research.UniqueNote(base, s.takenNotes(ctx, view, member))
}

// takenNotes is every file name this session must not land on: the ones
// other live sessions have pinned, and the ones already on the branch.
//
// Both halves are needed and neither is enough. The branch holds the
// notes of sessions long deleted, which is exactly the archive a
// collision would overwrite; the sandboxes hold the names of sessions
// that have pinned one and not yet pushed anything under it. And both
// are best effort — a name is being chosen, not a lock taken. If
// neither can be read the session gets the plain slug of its title,
// which is what it would have got anyway.
func (s *Server) takenNotes(ctx context.Context, view researchSandboxView, member string) map[string]bool {
	taken := map[string]bool{}
	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(view.Namespace).List(ctx, v1.ListOptions{
		LabelSelector: researchTypeLabel + "=" + researchType,
	})
	if err != nil {
		klog.V(2).Infof("research: cannot list sessions in %s to pick a note name: %v", view.Namespace, err)
	} else {
		for i := range list.Items {
			other, ok := researchViewFromSandbox(&list.Items[i])
			// Scoped to the repository, because the branch is: two
			// forks are two archives and cannot collide.
			if ok && other.SessionID != view.SessionID && other.Repo == view.Repo && other.Note != "" {
				taken[other.Note] = true
			}
		}
	}
	token, err := s.memberToken(ctx, view.Namespace)
	if err != nil {
		klog.V(2).Infof("research: no token to read %s for taken note names: %v", notesBranch, err)
		return taken
	}
	notes, err := researchNotesOnBranch(ctx, githubClientForToken(ctx, token), member, view.Repo)
	if err != nil {
		klog.V(2).Infof("research: cannot read %s on %s/%s: %v", notesBranch, member, view.Repo, err)
		return taken
	}
	for _, note := range notes {
		taken[note] = true
	}
	return taken
}

// researchNotesOnBranch lists the notes already on the notes branch. A
// branch that is not there yet is not an error — it is every fork
// before its first save.
func researchNotesOnBranch(ctx context.Context, gh *github.Client, owner, repo string) ([]string, error) {
	_, entries, resp, err := gh.Repositories.GetContents(ctx, owner, repo, research.NotesRoot,
		&github.RepositoryContentGetOptions{Ref: notesBranch})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	notes := []string{}
	for _, e := range entries {
		if e.GetType() == "file" {
			notes = append(notes, e.GetName())
		}
	}
	return notes, nil
}

// setResearchPending records that a save is owed, pins the file it is
// owed into, and clears any earlier failure so a fresh attempt is not
// reported as the old one's error.
func (s *Server) setResearchPending(ctx context.Context, namespace, sandboxName string, pending research.Pending) error {
	encoded := pending.Encode()
	if encoded == "" {
		return fmt.Errorf("could not encode the pending save")
	}
	return s.updateResearchAnnotations(ctx, namespace, sandboxName, map[string]string{
		research.CaptureAnnotation:      encoded,
		research.NoteAnnotation:         pending.Note,
		research.CaptureErrorAnnotation: "",
	})
}

// clearResearchPending removes an owed save. The note name stays: it is
// where this session writes from now on, including the save a refused
// capture will ask for again in a minute.
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
