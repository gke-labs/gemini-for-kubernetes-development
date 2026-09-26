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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
				// The bare repo URL, as `factory research start` writes
				// it. Leaving it out of this fixture hid a filter that
				// dropped every real research sandbox on the floor.
				"htmlURL": "https://github.com/test/repo",
				"sandbox.gemini.google.com/research-session-id": testSession,
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

func researchClaimAnnotation(sessionID, value string) map[string]string {
	return map[string]string{AnnotationRequests: `{"research-` + sessionID + `": "` + value + `"}`}
}

func researchLaunches(fake *fakeLauncher) []fakeLaunch {
	var out []fakeLaunch
	for _, l := range fake.launches() {
		if l.ResearchOpts != nil {
			out = append(out, l)
		}
	}
	return out
}

func boardAnnotations(t *testing.T, r *Reconciler) map[string]string {
	t.Helper()
	got := &boardv1alpha1.RepoBoard{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "alice", Name: "test-board"}, got); err != nil {
		t.Fatalf("reading the board back: %v", err)
	}
	return got.GetAnnotations()
}

// The value is a claim, not free text: anything that does not parse as
// one names no member and carries no clock, so nothing could ever serve
// or expire it. Repairing such an entry would mean guessing whose
// GitHub token to run a clone under.
func TestParseResearchClaim(t *testing.T) {
	at := "2026-09-26T12:00:00Z"
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("fixture timestamp: %v", err)
	}

	t.Run("accepts a well-formed claim", func(t *testing.T) {
		got, ok := parseResearchClaim("research-"+testSession, "alice|"+at)
		if !ok {
			t.Fatal("rejected a well-formed claim")
		}
		if got.sessionID != testSession {
			t.Errorf("sessionID = %q, want %q", got.sessionID, testSession)
		}
		if got.member != "alice" {
			t.Errorf("member = %q, want alice", got.member)
		}
		if !got.claimedAt.Equal(when) {
			t.Errorf("claimedAt = %v, want %v", got.claimedAt, when)
		}
	})

	// A trailing field must not break the parse: runbook claims already
	// carry a third segment, and the shape should stay extensible.
	t.Run("ignores extra segments", func(t *testing.T) {
		got, ok := parseResearchClaim("research-"+testSession, "alice|"+at+"|something-later")
		if !ok || got.member != "alice" || !got.claimedAt.Equal(when) {
			t.Errorf("extra segment broke the parse: %+v ok=%v", got, ok)
		}
	})

	bad := []struct {
		name  string
		key   string
		value string
	}{
		{"no session id", "research-", "alice|" + at},
		{"no member", "research-" + testSession, "|" + at},
		{"empty value", "research-" + testSession, ""},
		// The timestamp is required rather than defaulted: it is what
		// the TTL measures, and a claim with no clock never expires.
		{"no timestamp", "research-" + testSession, "alice"},
		{"unparseable timestamp", "research-" + testSession, "alice|yesterday"},
		// The id becomes an argument to the factory CLI and a lookup
		// key; these shapes are refused before they get that far.
		{"shell metacharacters", "research-a;rm -rf /", "alice|" + at},
		{"path traversal", "research-../../etc", "alice|" + at},
		{"leading dash", "research--x", "alice|" + at},
		{"spaces", "research-a b", "alice|" + at},
		{"slashes", "research-org/team", "alice|" + at},
		{"too long", "research-" + strings.Repeat("x", 200), "alice|" + at},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseResearchClaim(tc.key, tc.value); ok {
				t.Errorf("accepted %q = %q", tc.key, tc.value)
			}
		})
	}
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

// A standing claim launches `factory research start` with the session
// it names, and the claim survives the trim until a sandbox exists.
func TestResearchClaimLaunches(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	launches := researchLaunches(fake)
	g.Expect(launches).To(gomega.HaveLen(1))
	opts := launches[0].ResearchOpts
	g.Expect(opts.SessionID).To(gomega.Equal(testSession))
	g.Expect(opts.Namespace).To(gomega.Equal("alice"))
	g.Expect(opts.RepoURL).To(gomega.Equal("https://github.com/test/repo"))
	g.Expect(opts.GithubToken).NotTo(gomega.BeEmpty(), "the clone needs the member's token")
	// The key has to name the sandbox the invocation will create, or a
	// second claim for the same session would launch a second engine.
	g.Expect(launches[0].Key).To(gomega.Equal("alice/" + factorycli.ResearchSandboxName("repo", testSession)))

	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).To(gomega.ContainSubstring("research-"),
		"an unserved claim must stand until the sandbox exists")
}

// The sandbox existing is the receipt. This is the anti-loop property:
// without it every reconcile would start another engine for a
// conversation that already has one.
func TestResearchClaimServedBySandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	sb := researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", testSession))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "served claim must not relaunch")
	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).NotTo(gomega.ContainSubstring("research-"),
		"served claim must be trimmed")
}

// A sandbox for a DIFFERENT session must not serve this claim: the
// sandbox is per conversation, so two sessions on one repo are two
// sandboxes.
func TestResearchClaimNotServedByAnotherSession(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	other := researchSandboxObj("alice", factorycli.ResearchSandboxName("repo", "some-other-session"))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), other)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.HaveLen(1))
}

// An expired claim is dropped without launching. Nothing else bounds a
// claim that can never be served, and a member waiting on a session
// that cannot be created is better off starting a new one.
func TestResearchClaimExpires(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-researchClaimTTL - time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "an expired claim must not launch")
	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).NotTo(gomega.ContainSubstring("research-"),
		"an expired claim must be trimmed")
}

// Creation takes minutes; the claim has to wait rather than pile up
// invocations, and it must not be trimmed while one is in flight.
func TestResearchClaimWaitsWhileRunning(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	fake.running["alice/"+factorycli.ResearchSandboxName("repo", testSession)] = true
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty())
	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).To(gomega.ContainSubstring("research-"),
		"the claim must survive while the runner is busy")
}

// A failed invocation leaves no sandbox, so the claim stands — but the
// relaunch waits out the backoff rather than retrying every minute.
func TestResearchClaimBacksOffAfterFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	key := "alice/" + factorycli.ResearchSandboxName("repo", testSession)

	fake := newFakeLauncher()
	fake.results[key] = factorycli.Result{Err: context.DeadlineExceeded, FinishedAt: time.Now()}
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "a fresh failure must not relaunch immediately")
	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).To(gomega.ContainSubstring("research-"),
		"an unserved claim must survive its failure")

	// Past the backoff, the same claim tries again: the create is
	// idempotent, so a retry costs one lookup when it already worked.
	fake2 := newFakeLauncher()
	fake2.results[key] = factorycli.Result{Err: context.DeadlineExceeded, FinishedAt: time.Now().Add(-launchRetryBackoff - time.Minute)}
	board2 := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r2 := newTestReconciler(fake2, testGithubClient(`[]`), board2, githubSecret())
	_, err = r2.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake2)).To(gomega.HaveLen(1), "past the backoff the claim must retry")
}

// A claim the parser refuses must not survive in the annotation, or it
// would be re-examined on every reconcile forever.
func TestResearchClaimMalformedIsDropped(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	board := testBoard(map[string]string{AnnotationRequests: `{"research-x y": "alice"}`})
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty())
	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).NotTo(gomega.ContainSubstring("research-"))
}

// "research-" and "review-" share three letters, and the mailbox
// dispatches on prefixes. A research claim parsed as a review would
// launch an agent against a pull request that does not exist.
func TestResearchClaimIsNotAReviewClaim(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fake := newFakeLauncher()
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, l := range fake.launches() {
		g.Expect(l.ReviewOpts).To(gomega.BeNil(), "a research claim must not launch a review")
		g.Expect(l.FixOpts).To(gomega.BeNil(), "a research claim must not launch a fix")
	}
}

// --- the opening turn -------------------------------------------------

// fakeACPD is an acpd that records what it was asked.
type fakeACPD struct {
	mu       sync.Mutex
	exists   bool  // a session is already live
	offset   int64 // its transcript length, when it is
	created  []acpd.CreateSessionRequest
	apiKeys  []string
	prompts  []string
	promptNo int
}

func (f *fakeACPD) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			var in acpd.CreateSessionRequest
			_ = json.NewDecoder(req.Body).Decode(&in)
			f.created = append(f.created, in)
			f.apiKeys = append(f.apiKeys, req.Header.Get(acpd.APIKeyHeader))
			f.exists = true
			_ = json.NewEncoder(w).Encode(acpd.Session{ID: in.ID, Engine: in.Engine, CWD: in.CWD})
		case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/sessions/"):
			if !f.exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no such session"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(acpd.Session{ID: testSession, Offset: f.offset})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/prompt"):
			var in struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(req.Body).Decode(&in)
			f.prompts = append(f.prompts, in.Text)
			f.promptNo++
			_ = json.NewEncoder(w).Encode(map[string]int64{"offset": 128})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected ` + req.Method + " " + req.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	prev := researchACPD
	researchACPD = func(string) *acpd.Client { return acpd.New(srv.URL) }
	t.Cleanup(func() { researchACPD = prev })
	return srv
}

func (f *fakeACPD) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func (f *fakeACPD) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.apiKeys...)
}

// engineSecret is the member's own engine credential. The controller
// copies it into factory-user on every reconcile, which is where the
// kickoff reads it from — so the fixture is the source, not the copy.
func engineSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: geminiSecretName, Namespace: "alice"},
		Data:       map[string][]byte{"gemini": []byte("AIza-test")},
	}
}

// researchPod is the sandbox's pod, running and addressable.
func researchPod(sandboxName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName + "-0",
			Namespace: "alice",
			Labels:    map[string]string{"sandbox": sandboxName},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.1.2.3"},
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

// The claim carries the kickoff only until the sandbox exists; the
// handoff has to happen in the same reconcile that trims the claim, or
// the opening prompt is lost with it.
func TestResearchKickoffMovesFromClaimToSandbox(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	kickoff := research.Kickoff{Kind: research.KindOnboard}
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt+"|"+kickoff.Encode()))
	name := factorycli.ResearchSandboxName("repo", testSession)
	// No pod: the sandbox exists but is still booting, which is the
	// state this handoff is for.
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), board, githubSecret(), researchSandboxObj("alice", name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(boardAnnotations(t, r)[AnnotationRequests]).NotTo(gomega.ContainSubstring("research-"),
		"the claim is served and must be trimmed")
	annotations := sandboxAnnotations(t, r, name)
	g.Expect(research.DecodeKickoff(annotations[research.KickoffAnnotation])).To(gomega.Equal(kickoff),
		"the kickoff must survive the claim on the sandbox")
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

// The window this was actually lost in: `factory research start`
// creates the Sandbox and then clones for minutes, so the launch is
// still running while the sandbox already exists — and the trim pass
// reads that existence as served. Skipping the stamp because the runner
// is busy dropped the claim with the kickoff still on it, and the
// session came up untitled with nothing ever asked.
func TestResearchKickoffIsStampedWhileTheLaunchIsStillRunning(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	kickoff := research.Kickoff{Kind: research.KindActivity, Since: "2 weeks"}
	name := factorycli.ResearchSandboxName("repo", testSession)
	fake := newFakeLauncher()
	fake.running["alice/"+name] = true
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt+"|"+kickoff.Encode()))
	r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), researchSandboxObj("alice", name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(researchLaunches(fake)).To(gomega.BeEmpty(), "a sandbox that exists must not be launched again")
	annotations := sandboxAnnotations(t, r, name)
	g.Expect(research.DecodeKickoff(annotations[research.KickoffAnnotation])).To(gomega.Equal(kickoff))
	g.Expect(annotations[research.TitleAnnotation]).To(gomega.Equal("what happened · 2 weeks"))
}

// Once the pod is up the controller opens the conversation itself: the
// member may be minutes and a closed tab away.
func TestResearchKickoffIsSentAndCleared(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	kickoff := research.Kickoff{Kind: research.KindTopic, Topic: "where does the retry loop live?"}
	sb.SetAnnotations(map[string]string{
		"repo":                      "repo",
		researchSessionIDAnnotation: testSession,
		research.KickoffAnnotation:  kickoff.Encode(),
		research.TitleAnnotation:    kickoff.ResolvedTitle(),
	})
	acp := &fakeACPD{}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb, researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	sent := acp.sent()
	g.Expect(sent).To(gomega.HaveLen(1))
	g.Expect(sent[0]).To(gomega.ContainSubstring("where does the retry loop live?"))
	g.Expect(sent[0]).To(gomega.ContainSubstring("repo"), "the prompt names the checkout it is about")
	// The key reaches acpd in a header at create, and nowhere else.
	g.Expect(acp.keys()).To(gomega.Equal([]string{"AIza-test"}))
	g.Expect(acp.created[0].CWD).To(gomega.Equal("/workspaces/repo"))
	// Auto-approving, because nobody is watching this one. A canned
	// opening that stops to ask blocks until acpd's permission timeout
	// and is then cancelled, so the answer never arrives at all.
	g.Expect(acp.created[0].Mode).To(gomega.Equal(acpd.ResearchMode))

	annotations := sandboxAnnotations(t, r, name)
	g.Expect(annotations).NotTo(gomega.HaveKey(research.KickoffAnnotation),
		"a delivered kickoff must be cleared, or it would be sent again")
	g.Expect(annotations[research.TitleAnnotation]).To(gomega.Equal("where does the retry loop live?"))
}

// The receipt is the annotation's absence, so the second reconcile must
// be silent. This is the loop that would otherwise spend engine time on
// every pass.
func TestResearchKickoffIsSentOnce(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.KickoffAnnotation] = research.Kickoff{Kind: research.KindOnboard}.Encode()
	sb.SetAnnotations(annotations)
	acp := &fakeACPD{}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb, researchPod(name))

	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
	}
	g.Expect(acp.sent()).To(gomega.HaveLen(1))
}

// A conversation that has already been talked to keeps its history: the
// opening turn belongs at the start or not at all. This is what makes a
// lost receipt survivable.
func TestResearchKickoffSkipsAConversationInProgress(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.KickoffAnnotation] = research.Kickoff{Kind: research.KindOnboard}.Encode()
	sb.SetAnnotations(annotations)
	acp := &fakeACPD{exists: true, offset: 4096}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb, researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(acp.sent()).To(gomega.BeEmpty())
	g.Expect(sandboxAnnotations(t, r, name)).NotTo(gomega.HaveKey(research.KickoffAnnotation),
		"an opening that can no longer be sent must stop being owed")
}

// No pod yet is the normal state for a sandbox's first minutes. It is
// not an error, and nothing about it is recorded.
func TestResearchKickoffWaitsForThePod(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.KickoffAnnotation] = research.Kickoff{Kind: research.KindOnboard}.Encode()
	sb.SetAnnotations(annotations)
	acp := &fakeACPD{}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(acp.sent()).To(gomega.BeEmpty())
	got := sandboxAnnotations(t, r, name)
	g.Expect(got).To(gomega.HaveKey(research.KickoffAnnotation), "still owed")
	g.Expect(got).NotTo(gomega.HaveKey(research.KickoffErrorAnnotation), "and not yet given up on")
}

// A pod that never comes up eventually stops being retried, and says
// so: a session that was supposed to open with a question should not
// sit there silently looking answered.
func TestResearchKickoffGivesUpAndSaysWhy(t *testing.T) {
	g := gomega.NewWithT(t)
	name := factorycli.ResearchSandboxName("repo", testSession)
	sb := researchSandboxObj("alice", name)
	annotations := sb.GetAnnotations()
	annotations[research.KickoffAnnotation] = research.Kickoff{Kind: research.KindOnboard}.Encode()
	sb.SetAnnotations(annotations)
	sb.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-researchKickoffTTL - time.Minute)))
	acp := &fakeACPD{}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(),
		engineSecret(), sb)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	got := sandboxAnnotations(t, r, name)
	g.Expect(got[research.KickoffErrorAnnotation]).To(gomega.ContainSubstring("pod"))
	g.Expect(got).To(gomega.HaveKey(research.KickoffAnnotation),
		"what was owed stays readable next to why it was not delivered")

	// And it is not retried after that.
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(acp.sent()).To(gomega.BeEmpty())
}

// A plain "new conversation" claim files no kickoff, and the sandbox it
// produces must be left exactly as factory made it.
func TestResearchWithoutAKickoffStampsNothing(t *testing.T) {
	g := gomega.NewWithT(t)
	claimAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	board := testBoard(researchClaimAnnotation(testSession, "alice|"+claimAt))
	name := factorycli.ResearchSandboxName("repo", testSession)
	acp := &fakeACPD{}
	acp.server(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), board, githubSecret(),
		engineSecret(), researchSandboxObj("alice", name), researchPod(name))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(acp.sent()).To(gomega.BeEmpty(), "nobody asked for an opening turn")
	got := sandboxAnnotations(t, r, name)
	g.Expect(got).NotTo(gomega.HaveKey(research.KickoffAnnotation))
	g.Expect(got).NotTo(gomega.HaveKey(research.TitleAnnotation))
}
