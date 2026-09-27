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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// capturingSandbox is a research sandbox with a save owed on it.
func capturingSandbox(name string, pending research.Pending) *unstructured.Unstructured {
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureAnnotation] = pending.Encode()
	sb.SetAnnotations(annotations)
	return sb
}

func saveLaunches(fake *fakeLauncher) []fakeLaunch {
	var out []fakeLaunch
	for _, l := range fake.launches() {
		if l.SaveNotesOpts != nil {
			out = append(out, l)
		}
	}
	return out
}

// The ordinary path: the capture turn has ended, so the notes it wrote
// get pushed.
func TestResearchNotesArePushedOnceTheTurnEnds(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "scheduling.md", At: time.Now().Add(-2 * time.Minute).UTC()}
	acp := &fakeACPD{exists: true, offset: 4}
	acp.server(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := saveLaunches(fake)
	g.Expect(launches).To(gomega.HaveLen(1))
	opts := launches[0].SaveNotesOpts
	g.Expect(opts.SessionID).To(gomega.Equal(testSession))
	g.Expect(opts.Namespace).To(gomega.Equal("alice"))
	g.Expect(opts.RepoURL).To(gomega.Equal("https://github.com/test/repo"))
	g.Expect(opts.GithubToken).NotTo(gomega.BeEmpty(),
		"the push is the one place the member's token is used; without it there is nothing to authenticate with")
	// A distinct single-flight key from the sandbox create's. Sharing
	// one would read a save in flight as the create still running, and
	// vice versa — they act on the same sandbox.
	g.Expect(launches[0].Key).NotTo(gomega.Equal(researchKey("alice", "repo", testSession)))

	// Still owed: the push is asynchronous, and the receipt is the
	// harvested result, not the launch.
	g.Expect(sandboxAnnotations(t, r, name)).To(gomega.HaveKey(research.CaptureAnnotation))
}

// The wait this whole split exists for. Pushing mid-turn would save a
// half-written note, and the turn can run for minutes.
func TestResearchNotesWaitForTheTurnToFinish(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().Add(-time.Minute).UTC()}
	acp := &fakeACPD{exists: true, busy: true}
	acp.server(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty(), "a busy session is still writing the note")
	g.Expect(sandboxAnnotations(t, r, name)).To(gomega.HaveKey(research.CaptureAnnotation),
		"the save is still owed, and the next reconcile is the retry")
}

// A finished push is the receipt: the annotation goes, and so does any
// error left from an earlier attempt.
func TestResearchSaveIsHarvestedAndCleared(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().Add(-5 * time.Minute).UTC()}
	sb := capturingSandbox(name, pending)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureErrorAnnotation] = "an older failure"
	sb.SetAnnotations(annotations)

	acp := &fakeACPD{exists: true}
	acp.server(t)
	fake := newFakeLauncher()
	fake.results[saveNotesKey("alice", "repo", testSession)] = factorycli.Result{FinishedAt: time.Now()}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb, researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty(), "a save that has already finished must not be run again")
	got := sandboxAnnotations(t, r, name)
	g.Expect(got).NotTo(gomega.HaveKey(research.CaptureAnnotation))
	g.Expect(got).NotTo(gomega.HaveKey(research.CaptureErrorAnnotation),
		"a successful save clears the previous failure, or the conversation keeps reporting it")
}

// A push that failed has to say so. The member asked for the note in the
// conversation and was told it was coming; silence sends them to the
// fork to hunt for a file that was never pushed.
func TestResearchSaveFailureIsVisible(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().Add(-5 * time.Minute).UTC()}
	acp := &fakeACPD{exists: true}
	acp.server(t)
	fake := newFakeLauncher()
	fake.results[saveNotesKey("alice", "repo", testSession)] = factorycli.Result{
		Err:        errors.New("the conversation wrote no notes"),
		FinishedAt: time.Now(),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	got := sandboxAnnotations(t, r, name)
	g.Expect(got).NotTo(gomega.HaveKey(research.CaptureAnnotation), "a failed save is not retried forever")
	g.Expect(got[research.CaptureErrorAnnotation]).To(gomega.ContainSubstring("wrote no notes"))
}

// A session can be captured any number of times, and every capture uses
// the same runner key. Without the timestamp comparison the first save's
// result would be read as the second one's the instant it was asked for,
// and the second note would never be pushed.
func TestResearchSaveIgnoresAnEarlierCapturesResult(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "second.md", At: time.Now().UTC()}
	acp := &fakeACPD{exists: true}
	acp.server(t)
	fake := newFakeLauncher()
	fake.results[saveNotesKey("alice", "repo", testSession)] = factorycli.Result{
		FinishedAt: time.Now().Add(-10 * time.Minute),
	}
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.HaveLen(1),
		"the stale result belongs to the previous capture; this one still has to run")
	g.Expect(sandboxAnnotations(t, r, name)).To(gomega.HaveKey(research.CaptureAnnotation))
}

// Nothing to exec into and nothing to finish the turn. The member has to
// un-pause it; until then the save waits, and expires like any other
// undeliverable one.
func TestResearchSaveSkipsAPausedSession(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().UTC()}
	sb := capturingSandbox(name, pending)
	_ = unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas")
	acp := &fakeACPD{exists: true}
	acp.server(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), engineSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty())
	g.Expect(sandboxAnnotations(t, r, name)).To(gomega.HaveKey(research.CaptureAnnotation))
}

// Retrying forever means a dead session costs a pod list a minute for as
// long as it is kept, and the member is never told.
func TestResearchSaveGivesUpAndSaysWhy(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().Add(-2 * researchCaptureTTL).UTC()}
	fake := newFakeLauncher()
	// No pod: the save can never be made.
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty())
	got := sandboxAnnotations(t, r, name)
	g.Expect(got).NotTo(gomega.HaveKey(research.CaptureAnnotation))
	g.Expect(got[research.CaptureErrorAnnotation]).To(gomega.ContainSubstring("no running pod"))
}

// An annotation that cannot be read carries no clock, so it could never
// expire. Dropping it is the only way the wait ends.
func TestUnreadableCaptureIsDropped(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.CaptureAnnotation] = "not a pending save"
	sb.SetAnnotations(annotations)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), engineSecret(), sb,
		researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty())
	got := sandboxAnnotations(t, r, name)
	g.Expect(got).NotTo(gomega.HaveKey(research.CaptureAnnotation))
	g.Expect(got[research.CaptureErrorAnnotation]).NotTo(gomega.BeEmpty())
}

// The engine is gone — acpd restarted, or the pod did. Whatever the turn
// wrote is on the PVC and is the only copy there will be, so it gets
// pushed rather than waiting on a session that is not coming back.
func TestResearchSavePushesAfterTheEngineIsGone(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	pending := research.Pending{Note: "notes.md", At: time.Now().Add(-time.Minute).UTC()}
	acp := &fakeACPD{} // answers 404 for the session
	acp.server(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), capturingSandbox(name, pending), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.HaveLen(1))
	g.Expect(acp.sent()).To(gomega.BeEmpty(), "a save must not start a conversation")
}

// A sandbox with no save owed must cost nothing: no pod list, no session
// read, and above all no push. This is the state every research sandbox
// is in almost all of the time.
func TestSandboxWithNoCaptureIsUntouched(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	acp := &fakeACPD{exists: true}
	acp.server(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), researchSandboxObj("alice", name), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(saveLaunches(fake)).To(gomega.BeEmpty())
	g.Expect(sandboxAnnotations(t, r, name)).NotTo(gomega.HaveKey(research.CaptureErrorAnnotation))
}
