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
	notesApplyKeyAt  = "alice/apply-" + notesSandboxName + "-research-push-notes"
)

// recipeResearchSandbox is a research sandbox `factory recipe research`
// started: its start, task research-1, recorded under research-run.
func recipeResearchSandbox(extra map[string]string) *unstructured.Unstructured {
	sb := researchSandboxObj("alice", notesSandboxName)
	run, _ := json.Marshal(factorycli.RecordedRun{
		Name: "research/" + testSession, Task: "research-1", StartedAt: time.Now().Add(-time.Hour), Kind: "Notes",
	})
	a := sb.GetAnnotations()
	a[factorycli.ResearchRunAnnotation] = string(run)
	for k, v := range extra {
		a[k] = v
	}
	sb.SetAnnotations(a)
	return sb
}

func notesApplyClick() *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb:    boardv1alpha1.VerbApply,
		Sandbox: notesSandboxName,
		Apply:   &boardv1alpha1.ApplyRequest{Run: "research", Action: "push-notes"},
	})
}

// Save to research/notes is factory apply --action push-notes on the
// draft as it is now, under the conversation's note name, targeting the
// repository; done, it stamps the sandbox.
func TestSaveNotesToResearchNotes(t *testing.T) {
	g := gomega.NewWithT(t)
	sb := recipeResearchSandbox(map[string]string{
		research.NoteAnnotation: "retry-loop",
		factorycli.OutputAnnotation(factorycli.ResearchRunAnnotation): "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Notes\n" +
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
	g.Expect(factorycli.IsApplied(sandboxAnnotations(t, r, notesSandboxName), factorycli.AppliedAnnotation(factorycli.ResearchRunAnnotation), "push-notes")).To(gomega.BeTrue())
}
