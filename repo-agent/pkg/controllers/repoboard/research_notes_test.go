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

package repoboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

const (
	notesSandboxName = "rsch-repo-abc12345"
	notesReviseKeyAt = "alice/revise-" + notesSandboxName
	notesApplyKeyAt  = "alice/apply-" + notesSandboxName + "-research-push-notes"
)

// recipeResearchSandbox is a research sandbox `factory recipe research`
// started: its start, task research-1, recorded under research-run.
func recipeResearchSandbox(extra map[string]string) *unstructured.Unstructured {
	sb := researchSandboxObj("alice", notesSandboxName)
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "research/" + testSession, Task: "research-1", StartedAt: time.Now().Add(-time.Hour), Kind: "Notes",
		Revises: []factorycli.RecordedRevise{{ID: "notes", Label: "Save notes"}},
	})
	a := sb.GetAnnotations()
	a[factorycli.ResearchRunAnnotation] = string(run)
	for k, v := range extra {
		a[k] = v
	}
	sb.SetAnnotations(a)
	return sb
}

func notesReviseClick() *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbRevise,
		Sandbox: notesSandboxName,
		Revise:  "notes",
	})
}

func notesApplyClick() *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbApply,
		Sandbox: notesSandboxName,
		Apply:   &boardv1alpha1.ApplyRequest{Run: "research", Action: "push-notes"},
	})
}

// notesOutput is what StartRevise's harvest leaves for a Save notes: the
// revise's Notes task output after the banner.
const notesOutput = "================== TASK OUTPUT =================\n" +
	"apiVersion: factory.gemini.google.com/v1alpha1\nkind: Notes\n" +
	"target:\n  url: https://github.com/test/repo\n" +
	"source:\n  task: research-2\n  session: research-1\n" +
	"spec:\n  markdown: |-\n    # Retry loop\n    It lives in pkg/retry.\n" +
	"================================================\n"

// Save notes revises in the conversation's session, keyed by its sandbox,
// and the notes it writes become the draft, not yet saved.
func TestSaveNotesRevisesTheConversation(t *testing.T) {
	g := gomega.NewWithT(t)
	req := notesReviseClick()
	fake := newFakeLauncher()
	sb := recipeResearchSandbox(map[string]string{factorycli.AnnotationNotesApplied: `{"push-notes":"2026-10-01T00:00:00Z"}`})
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal(notesReviseKeyAt))
	opts := launches[0].ReviseOpts
	g.Expect(opts).NotTo(gomega.BeNil())
	g.Expect(opts.SandboxName).To(gomega.Equal(notesSandboxName))
	g.Expect(opts.Revise).To(gomega.Equal("notes"))
	g.Expect(opts.Session).To(gomega.Equal("research-1"))
	g.Expect(opts.RunName).To(gomega.HavePrefix("revise/test-board/" + notesSandboxName + "/notes/"))

	fake.running[notesReviseKeyAt] = true
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	delete(fake.running, notesReviseKeyAt)
	fake.results[notesReviseKeyAt] = factorycli.Result{FinishedAt: time.Now().Add(time.Second), Output: notesOutput}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	a := sandboxAnnotations(t, r, notesSandboxName)
	g.Expect(factorycli.NotesDraft(a)).To(gomega.Equal("# Retry loop\nIt lives in pkg/retry."))
	g.Expect(a[factorycli.AnnotationNotesOutput]).To(gomega.ContainSubstring("task: research-2"))
	g.Expect(a).To(gomega.HaveKey(AnnotationNotesDraftedAt))
	g.Expect(a).NotTo(gomega.HaveKey(factorycli.AnnotationNotesApplied))
}

// A Save notes that fails fails the click with what factory said, and is
// not run again by itself.
func TestSaveNotesFailureIsNotRetried(t *testing.T) {
	g := gomega.NewWithT(t)
	req := notesReviseClick()
	fake := newFakeLauncher()
	fake.results[notesReviseKeyAt] = factorycli.Result{
		FinishedAt: time.Now().Add(time.Second),
		Output:     "session research-1 is busy: a turn is in flight\n",
		Err:        errors.New("exit status 1"),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), recipeResearchSandbox(nil), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Message).To(gomega.ContainSubstring("a turn is in flight"))
}

// A sandbox the recipe did not start has no task session to revise
// in: nothing is launched for it.
func TestSaveNotesNeedsARecipeConversation(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchSandboxObj("alice", notesSandboxName), notesReviseClick())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
}

// After a restart, the Save notes recorded since the click is followed by
// its run name; the conversation's start is not.
func TestSaveNotesResumesItsRecordedRun(t *testing.T) {
	g := gomega.NewWithT(t)
	name := "revise/test-board/" + notesSandboxName + "/notes/1"
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: name, Task: "research-2", Session: "research-1", StartedAt: time.Now().Add(time.Second), Kind: "Notes",
		Revises: []factorycli.RecordedRevise{{ID: "notes", Label: "Save notes"}},
	})
	sb := recipeResearchSandbox(map[string]string{factorycli.ResearchRunAnnotation: string(run)})
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, notesReviseClick())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ReviseOpts.RunName).To(gomega.Equal(name))
	// The session is still the start's: the revise recorded it.
	g.Expect(launches[0].ReviseOpts.Session).To(gomega.Equal("research-1"))
}

// Save to research/notes is factory apply --action push-notes on the
// draft as it is now, under the conversation's note name, targeting the
// repository; done, it stamps the sandbox.
func TestSaveNotesToResearchNotes(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := recipeResearchSandbox(map[string]string{
		AnnotationNotesDraftedAt: "2026-10-01T00:00:00Z",
		research.NoteAnnotation:  "retry-loop",
		factorycli.AnnotationNotesOutput: "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Notes\n" +
			"target:\n  url: https://github.com/test/repo\nsource:\n  task: research-2\n  session: research-1\n" +
			"spec:\n  markdown: |-\n    # Retry loop\n    Edited.\n",
	})
	req := notesApplyClick()
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal(notesApplyKeyAt))
	opts := launches[0].ApplyOpts
	g.Expect(opts.Action).To(gomega.Equal("push-notes"))
	g.Expect(opts.Doc).To(gomega.And(
		gomega.ContainSubstring("kind: Notes"),
		gomega.ContainSubstring("task: research-2"),
		gomega.ContainSubstring("name: retry-loop"),
		gomega.ContainSubstring("Edited."),
		gomega.ContainSubstring("url: https://github.com/test/repo\n")))

	fake.results[notesApplyKeyAt] = factorycli.Result{FinishedAt: time.Now().Add(time.Second)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(factorycli.IsApplied(sandboxAnnotations(t, r, notesSandboxName), factorycli.AnnotationNotesApplied, "push-notes")).To(gomega.BeTrue())
}
