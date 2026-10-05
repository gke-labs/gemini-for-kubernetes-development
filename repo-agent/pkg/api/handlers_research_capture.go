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

// Saving a conversation's notes: 💾 asks the recipe's notes revise to
// write them into a draft (saveRecipeNotes), and the note's file name is
// picked here.

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// captureResearchNotes is 💾: the conversation writes its notes with
// the recipe's revise, into a draft. No inputs — what goes in the note is
// the conversation, and a member who wants something narrower says so to
// the agent first.
func (s *Server) captureResearchNotes(c *gin.Context) {
	conn, ok := s.resolveResearch(c)
	if !ok {
		return
	}
	s.saveRecipeNotes(c, conn.view)
}

// researchNoteName is the file this session's notes go in.
//
// Derived from the session's title at the first save and read off
// the sandbox every time after that, so saving twice updates one file
// rather than writing a second copy of it. The name is the member's — a
// session called "where the retry loop terminates" saves to
// `where-the-retry-loop-terminates.md` — which is the whole point: the
// file is the note's address on a branch someone will read months from
// now, and a session id is not an address, it is a handle.
//
// Renaming the session clears the pin (see setResearchTitle), so the
// next save derives a fresh name from the new title. The note
// already on the branch under the old name is left alone: a duplicate
// is cheaper than a note saved under a name the member has moved on
// from, and the branch is an archive.
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
