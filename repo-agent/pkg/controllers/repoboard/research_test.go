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
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// The session used throughout; the sandbox name it produces is derived,
// so the tests can name the sandbox before anything creates it.
const testSession = "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00"

func researchSandboxObj(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"sandbox.gemini.google.com/type":    "research",
			},
			"annotations": map[string]interface{}{
				"repo": "repo",
				// The bare repo URL, as `factory recipe research` writes
				// it. Leaving it out of this fixture hid a filter that
				// dropped every real research sandbox on the floor.
				"htmlURL": "https://github.com/test/repo",
				"sandbox.gemini.google.com/research-session-id": testSession,
				// A finished sandbox, which is what nearly every test in
				// this package means by one: the receipt factory writes
				// after the checkout lands. researchSandboxUnfinished
				// takes it back off for the tests about the gap.
				researchReadyAnnotation: "2026-01-01T00:00:00Z",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

// researchSandboxUnfinished is a sandbox factory created and never came
// back to finish: the object is there, the receipt is not.
func researchSandboxUnfinished(sb *unstructured.Unstructured) *unstructured.Unstructured {
	ann := sb.GetAnnotations()
	delete(ann, researchReadyAnnotation)
	sb.SetAnnotations(ann)
	return sb
}

// researchRequest files a research click, aged so the TTL tests can
// reach back past it. The click time is the Request's own creation
// stamp, which is why nothing here carries a timestamp field.
func researchRequest(sessionID string, claimedAt time.Time, kickoff research.Kickoff) *boardv1alpha1.Request {
	req := testRequest(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbResearch,
		Research: &boardv1alpha1.ResearchRequest{
			SessionID: sessionID,
			Kind:      kickoff.Kind,
			Topic:     kickoff.Topic,
			Since:     kickoff.Since,
			Title:     kickoff.Title,
		},
	})
	req.CreationTimestamp = metav1.NewTime(claimedAt)
	return req
}

// aQuestion is what a click asks: the board sends the box's text as a
// topic.
var aQuestion = research.Kickoff{Kind: research.KindTopic, Topic: "where does the retry loop live?"}

func researchLaunches(fake *fakeLauncher) []fakeLaunch {
	var out []fakeLaunch
	for _, l := range fake.launches() {
		if l.ResearchOpts != nil {
			out = append(out, l)
		}
	}
	return out
}

// The typed spec removed most of what could go wrong — the member and
// the click time are fields now, not segments of a string — but the
// session id still has to be checked. It becomes an argument to the
// factory CLI and the name of a sandbox, and a Request is writable by
// anything with access to the namespace.
func TestResearchClaimFrom(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	t.Run("accepts a well-formed request", func(t *testing.T) {
		got, ok := researchClaimFrom(researchRequest(testSession, at, research.Kickoff{}))
		if !ok {
			t.Fatal("rejected a well-formed request")
		}
		if got.sessionID != testSession {
			t.Errorf("sessionID = %q, want %q", got.sessionID, testSession)
		}
		if got.member != "alice" {
			t.Errorf("member = %q, want alice", got.member)
		}
		if !got.claimedAt.Equal(at) {
			t.Errorf("claimedAt = %v, want %v", got.claimedAt, at)
		}
	})

	// The opening turn rides along as typed fields rather than an
	// encoded blob, which is the point of the spec.
	t.Run("carries the kickoff", func(t *testing.T) {
		kickoff := research.Kickoff{Kind: research.KindTopic, Topic: "where does the retry loop live?"}
		got, ok := researchClaimFrom(researchRequest(testSession, at, kickoff))
		if !ok || got.kickoff != kickoff {
			t.Errorf("kickoff = %+v, want %+v (ok=%v)", got.kickoff, kickoff, ok)
		}
	})

	bad := []struct{ name, sessionID string }{
		{"no session id", ""},
		{"shell metacharacters", "a;rm -rf /"},
		{"path traversal", "../../etc"},
		{"leading dash", "-x"},
		{"spaces", "a b"},
		{"slashes", "org/team"},
		{"too long", strings.Repeat("x", 200)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := researchClaimFrom(researchRequest(tc.sessionID, at, research.Kickoff{})); ok {
				t.Errorf("accepted session id %q", tc.sessionID)
			}
		})
	}

	// A research verb with no research block at all: the CRD makes this
	// hard to file, and nothing serves it if someone manages to.
	t.Run("no research block", func(t *testing.T) {
		req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbResearch})
		if _, ok := researchClaimFrom(req); ok {
			t.Error("accepted a research request with no session")
		}
	})
}

func TestResearchClaimExpiry(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"fresh", time.Minute, false},
		{"just inside the TTL", researchClaimTTL - time.Second, false},
		{"past the TTL", researchClaimTTL + time.Second, true},
		// A clock skew that puts the click in the future must not read
		// as expired — that would drop every claim a fast clock wrote.
		{"claimed in the future", -time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := researchClaim{sessionID: testSession, member: "alice", claimedAt: now.Add(-tc.age)}
			if got := researchClaimExpired(claim, now); got != tc.want {
				t.Errorf("researchClaimExpired = %v, want %v", got, tc.want)
			}
		})
	}
}

// A standing click launches `factory recipe research` with the session
// it names and the question it asks, and stays standing until a sandbox
// exists.
func TestResearchClaimLaunches(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := researchLaunches(fake)
	g.Expect(launches).To(gomega.HaveLen(1))
	opts := launches[0].ResearchOpts
	g.Expect(opts.SessionID).To(gomega.Equal(testSession))
	g.Expect(opts.Namespace).To(gomega.Equal("alice"))
	g.Expect(opts.RepoURL).To(gomega.Equal("https://github.com/test/repo"))
	g.Expect(opts.Topic).To(gomega.Equal("where does the retry loop live?"))
	g.Expect(opts.GithubToken).NotTo(gomega.BeEmpty(), "the clone needs the member's token")
	// The key has to name the sandbox the invocation will create, or a
	// second claim for the same session would launch a second engine.
	g.Expect(launches[0].Key).To(gomega.Equal("alice/" + factorycli.ResearchSandboxName("repo", testSession)))

	g.Expect(opts.Engine).To(gomega.Equal(acpd.EngineGemini), "a board with no engine set runs research on gemini")

	g.Expect(requestStatus(t, r, req).Phase).To(gomega.BeEmpty(),
		"an unserved click must stand until the sandbox exists")
}

// The sandbox is launched for the board's engine, since antigravity's
// ACP server is installed at launch. acpd has no claude engine, so a
// claude board's research stays on gemini.
func TestResearchClaimLaunchesForTheBoardEngine(t *testing.T) {
	for engine, want := range map[string]string{
		"antigravity": acpd.EngineAntigravity,
		"claude":      acpd.EngineGemini,
	} {
		t.Run(engine, func(t *testing.T) {
			g := gomega.NewWithT(t)
			req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
			board := testBoard(nil)
			board.Spec.Sandbox.Engine = engine
			fake := newFakeLauncher()
			r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), req)

			_, err := r.Reconcile(context.Background(), boardRequest())
			g.Expect(err).NotTo(gomega.HaveOccurred())

			launches := researchLaunches(fake)
			g.Expect(launches).To(gomega.HaveLen(1))
			g.Expect(launches[0].ResearchOpts.Engine).To(gomega.Equal(want))
		})
	}
}

// A board that overrides its sandbox image gets it for research too.
func TestResearchClaimLaunchesOnTheBoardImage(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	board := testBoard(nil)
	board.Spec.Sandbox.Image = "registry.example/factory-golang:abc"
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := researchLaunches(fake)
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ResearchOpts.Image).To(gomega.Equal("registry.example/factory-golang:abc"))
}

// The finished sandbox is the receipt. This is the anti-loop property:
// without it every reconcile would start another engine for a
// conversation that already has one.
func TestResearchClaimServedBySandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	fake := newFakeLauncher()
	sb := researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", testSession))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "a served click must not relaunch")
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded), "a served click must settle")
	g.Expect(status.Sandbox).To(gomega.Equal(factorycli.ResearchSandboxName("repo", testSession)))
}

// The object without the receipt is not a session. factory creates the
// Sandbox in the first second and then pulls, waits and clones for
// minutes, so a launch killed in between leaves exactly this: a pod
// with an empty workspace and an engine that cannot chdir into a
// checkout nobody made. Settling on it is what stranded a conversation
// on "opening…" for good.
func TestResearchClaimNotServedByAnUnfinishedSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	fake := newFakeLauncher()
	sb := researchSandboxUnfinished(researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", testSession)))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(researchLaunches(fake)).To(gomega.HaveLen(1),
		"an unfinished sandbox must be relaunched, not handed over")
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.BeEmpty(),
		"the click must stand until the setup finishes")
}

// The relaunch is not forever. A sandbox that never gets its receipt
// runs out the claim's TTL like any other unserved click, and the
// member is told to start another rather than left watching a spinner.
func TestResearchClaimWithAnUnfinishedSandboxStillExpires(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-researchClaimTTL-time.Minute), aQuestion)
	fake := newFakeLauncher()
	sb := researchSandboxUnfinished(researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", testSession)))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("Expired"))
}

// A sandbox for a DIFFERENT session must not serve this claim: the
// sandbox is per conversation, so two sessions on one repo are two
// sandboxes.
func TestResearchClaimNotServedByAnotherSession(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	other := researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", "some-other-session"))
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), other, req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.HaveLen(1))
}

// An expired click stops launching, and says so. Nothing else bounds a
// click that can never be served, and a member waiting on a session
// that cannot be created is better off being told than left waiting —
// which is the half the annotation could not do, because trimming the
// entry left no record that anything had been asked for.
func TestResearchClaimExpires(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-researchClaimTTL-time.Minute), aQuestion)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "an expired click must not launch")
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("Expired"))
	g.Expect(status.Message).NotTo(gomega.BeEmpty(), "a failure must say why")
}

// Creation takes minutes; the click has to wait rather than pile up
// invocations, and it must not settle while one is in flight.
func TestResearchClaimWaitsWhileRunning(t *testing.T) {
	g := gomega.NewWithT(t)
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	fake := newFakeLauncher()
	fake.running["alice/"+factorycli.ResearchSandboxName("repo", testSession)] = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.BeEmpty(),
		"the click must stand while the runner is busy")
}

// A failed invocation leaves no sandbox, so the click stands — but the
// relaunch waits out the backoff rather than retrying every minute.
func TestResearchClaimBacksOffAfterFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute)
	key := "alice/" + factorycli.ResearchSandboxName("repo", testSession)

	fake := newFakeLauncher()
	fake.results[key] = factorycli.Result{Err: context.DeadlineExceeded, FinishedAt: time.Now()}
	req := researchRequest(testSession, claimAt, aQuestion)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "a fresh failure must not relaunch immediately")
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.BeEmpty(),
		"an unserved click must survive its failure")

	// Past the backoff, the same click tries again: the create is
	// idempotent, so a retry costs one lookup when it already worked.
	fake2 := newFakeLauncher()
	fake2.results[key] = factorycli.Result{Err: context.DeadlineExceeded, FinishedAt: time.Now().Add(-launchRetryBackoff - time.Minute)}
	r2 := newTestReconciler(fake2, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchRequest(testSession, claimAt, aQuestion))
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake2)).To(gomega.HaveLen(1), "past the backoff the click must retry")
}

// A session id nothing can serve fails the click rather than sitting
// Pending forever. The annotation dropped such an entry silently, which
// left the member watching a list that would never fill.
func TestResearchClaimMalformedFails(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := researchRequest("x y", time.Now(), aQuestion)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty())
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("Malformed"))
}

// The verbs used to be prefixes of one key string, and "research-" and
// "review-" share three letters. A research click read as a review
// would launch an agent against a pull request that does not exist.
// The verb is its own field now; this keeps the property nailed down.
func TestResearchClaimIsNotAReviewClaim(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := researchRequest(testSession, time.Now().Add(-time.Minute), aQuestion)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.ReviewOpts).To(gomega.BeNil(), "a research click must not launch a review")
		g.Expect(l.FixOpts).To(gomega.BeNil(), "a research click must not launch a fix")
	}
}

func sandboxAnnotations(t *testing.T, r *Reconciler, name string) map[string]string {
	t.Helper()
	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "alice", Name: name}, sb); err != nil {
		t.Fatalf("reading the sandbox back: %v", err)
	}
	return sb.GetAnnotations()
}

// A canned kind is asked as the text it renders to, and named as before;
// the title moves to the sandbox in the reconcile that settles the click,
// or it is lost with it.
func TestResearchCannedKindIsAskedAndTitled(t *testing.T) {
	g := gomega.NewWithT(t)
	kickoff := research.Kickoff{Kind: research.KindOnboard}
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchRequest(testSession, time.Now().Add(-time.Minute), kickoff))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := researchLaunches(fake)
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].ResearchOpts.Topic).To(gomega.ContainSubstring("Overview of the repo"))
	g.Expect(launches[0].ResearchOpts.Topic).To(gomega.ContainSubstring("https://github.com/test/repo"))

	req := researchRequest(testSession, time.Now().Add(-time.Minute), kickoff)
	name := factorycli.ResearchSandboxName("repo", testSession)
	r = newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchSandboxObj("alice", name), req)
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	annotations := sandboxAnnotations(t, r, name)
	g.Expect(annotations[research.TitleAnnotation]).To(gomega.Equal("overview"))
}

// The bug that made the whole feature look broken in the cluster: the
// sandbox filter matched htmlURL against a hint ending in "/", which a
// PR URL satisfies and a bare repo URL does not. Every research sandbox
// was dropped from work.sandboxes, so no claim was ever served — the
// kickoff was never stamped, no opening was ever sent, and deleting the
// conversation handed the still-standing claim straight back to the
// controller, which built it again.
func TestResearchSandboxIsNotFilteredOutByItsBareRepoURL(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchSandboxObj("alice", name))
	work := &workState{owner: "test", repo: "repo"}

	g.Expect(r.loadSandboxes(context.Background(), work, map[string]bool{"alice": true})).To(gomega.Succeed())
	g.Expect(work.findSandbox("alice", name)).NotTo(gomega.BeNil(),
		"a research sandbox names the repo itself, with no path after it")
}

// The boundary the trailing slash was there for: a prefix of another
// repo's name must still not match.
func TestSandboxesOfAPrefixSharingRepoAreStillFilteredOut(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo-extra", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations["repo"] = ""
	annotations["htmlURL"] = "https://github.com/test/repo-extra"
	sb.SetAnnotations(annotations)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)
	work := &workState{owner: "test", repo: "repo"}

	g.Expect(r.loadSandboxes(context.Background(), work, map[string]bool{"alice": true})).To(gomega.Succeed())
	g.Expect(work.findSandbox("alice", name)).To(gomega.BeNil())
}

// The title is stamped in the pass that first sees the receipt, even
// while the launch that made it is still running: the trim pass reads the
// receipt as served and drops the claim with the title on it.
func TestResearchTitleIsStampedWhileTheLaunchIsStillRunning(t *testing.T) {
	g := gomega.NewWithT(t)
	kickoff := research.Kickoff{Kind: research.KindActivity, Since: "2 weeks"}
	name := factorycli.ResearchSandboxName("repo", testSession)
	fake := newFakeLauncher()
	fake.running["alice/"+name] = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchSandboxObj("alice", name),
		researchRequest(testSession, time.Now().Add(-time.Minute), kickoff))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "a sandbox that exists must not be launched again")
	g.Expect(sandboxAnnotations(t, r, name)[research.TitleAnnotation]).To(gomega.Equal("Changes in the last 2 weeks"))
}

// A click with no question has nothing for the recipe to ask: it is not
// launched, and runs out its TTL.
func TestResearchWithoutAQuestionIsNotLaunched(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		researchRequest(testSession, time.Now().Add(-time.Minute), research.Kickoff{}))
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty())
}
