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
	"errors"
	"testing"
	"time"

	"github.com/onsi/gomega"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

const fixKeyAt = "alice/fix-repo-7"

// fixRun is a fix run recorded on issue 7's sandbox, started an hour ago.
func fixRun(name, session string) string {
	s := `{"name":"` + name + `","task":"recipe-fix-1"`
	if session != "" {
		s += `,"session":"` + session + `"`
	}
	return s + `,"startedAt":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `","kind":"Change",` +
		`"revises":[{"id":"iterate","label":"Iterate","inputs":["instruction"]},{"id":"address-comments"},{"id":"fix-ci"}]}`
}

// fixedSandbox is issue 7's sandbox after its fix: task type fix, the run
// recorded, and, with a PR, aliased to it as open-pr leaves it.
func fixedSandbox(state, run string, extra map[string]interface{}) map[string]interface{} {
	a := map[string]interface{}{
		factorycli.AnnotationTaskType:  "fix",
		factorycli.AnnotationTaskState: state,
		factorycli.AnnotationFixRun:    run,
	}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

// A fix run the controller did not see end (it restarted) is followed by
// its recorded run name, so the runner reads it and opens its PR.
func TestFixFollowsAnUnreadRun(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	sb := issueSandbox("fix-repo-7", "7", fixedSandbox(factorycli.TaskStateRunning, fixRun("fix/test-board/7/100", ""), nil))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal(fixKeyAt))
	g.Expect(launches[0].FixOpts.RunName).To(gomega.Equal("fix/test-board/7/100"))
	g.Expect(launches[0].FixOpts.SandboxName).To(gomega.Equal("fix-repo-7"))
	g.Expect(launches[0].FixOpts.URL).To(gomega.Equal("https://github.com/test/repo/issues/7"))
}

// A run the controller read is not followed again, and a revise of the
// fix (recorded under the same key) is never followed as a fix.
func TestFixDoesNotFollowAReadRunOrARevise(t *testing.T) {
	for name, a := range map[string]map[string]interface{}{
		"read": fixedSandbox(factorycli.TaskStateCompleted, fixRun("fix/test-board/7/100", ""), map[string]interface{}{
			AnnotationFixHarvestedAt: time.Now().UTC().Format(time.RFC3339),
		}),
		"revise": fixedSandbox(factorycli.TaskStateCompleted, fixRun("revise/test-board/fix-repo-7/iterate/200", "recipe-fix-1"), nil),
	} {
		g := gomega.NewWithT(t)
		fake := newFakeLauncher()
		r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), issueSandbox("fix-repo-7", "7", a))
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(fake.launches()).To(gomega.BeEmpty(), name)
	}
}

// The fix's result is written down on the sandbox once read: when, and
// why it failed. A restarted controller then leaves it be.
func TestFixRecordsItsResult(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.results[fixKeyAt] = factorycli.Result{
		FinishedAt: time.Now(),
		Output: "================== TASK OUTPUT =================\n" + changeDoc("Fix it") + "\n" + closer +
			"\nError: the fork has no branch issue-7-1\n",
		Err: errors.New("applying open-pr: exit status 1"),
	}
	sb := issueSandbox("fix-repo-7", "7", fixedSandbox(factorycli.TaskStateCompleted, fixRun("fix/test-board/7/100", ""), nil))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	a := sandboxAnnotations(t, r, "fix-repo-7")
	g.Expect(a).To(gomega.HaveKey(AnnotationFixHarvestedAt))
	g.Expect(a[AnnotationFixError]).To(gomega.Equal("the fork has no branch issue-7-1"))
	_, unread := fixRunUnread(a, "test-board", 7)
	g.Expect(unread).To(gomega.BeFalse())
	// Its Change is kept, its PR not opened: Open draft PR is the retry.
	g.Expect(a["board.gemini.google.com/fix-output"]).To(gomega.ContainSubstring("title: Fix it"))
	g.Expect(factorycli.IsApplied(a, "board.gemini.google.com/fix-applied", "open-pr")).To(gomega.BeFalse())
}

// closer closes a harvested task output.
const closer = "================================================"

// changeDoc is a Change task output titled title.
func changeDoc(title string) string {
	return "apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\nsource:\n  task: recipe-fix-1\n" +
		"spec:\n  title: " + title + "\n  body: Fixes #7.\n"
}

// A fix whose PR opened keeps its Change with open-pr applied.
func TestFixKeepsItsChange(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	fake.results[fixKeyAt] = factorycli.Result{
		FinishedAt: time.Now(),
		Output:     "================== TASK OUTPUT =================\n" + changeDoc("Fix it") + "\n" + closer + "\nopened #9\n",
	}
	sb := issueSandbox("fix-repo-7", "7", fixedSandbox(factorycli.TaskStateCompleted, fixRun("fix/test-board/7/100", ""), nil))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	a := sandboxAnnotations(t, r, "fix-repo-7")
	g.Expect(a).NotTo(gomega.HaveKey(AnnotationFixError))
	g.Expect(a["board.gemini.google.com/fix-output"]).To(gomega.Equal(changeDoc("Fix it")))
	g.Expect(factorycli.IsApplied(a, "board.gemini.google.com/fix-applied", "open-pr")).To(gomega.BeTrue())
}

// Fix again relaunches the fix in its sandbox, under a new run name.
func TestFixAgainRelaunches(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	done := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	sb := issueSandbox("fix-repo-7", "7", fixedSandbox(factorycli.TaskStateFailed, fixRun("fix/test-board/7/100", ""), map[string]interface{}{
		AnnotationFixHarvestedAt:            done,
		factorycli.AnnotationCompletionTime: done,
		AnnotationRefixRequested:            time.Now().UTC().Format(time.RFC3339),
	}))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].FixOpts.RunName).To(gomega.HavePrefix("fix/test-board/7/"))
	g.Expect(launches[0].FixOpts.RunName).NotTo(gomega.Equal("fix/test-board/7/100"))
}
