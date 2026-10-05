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

// Research claims: a member opens a deep-research conversation about the
// board's repository, and the sandbox that hosts it is created here.
//
// The split mirrors the chat terminal's. Creating a sandbox needs the
// factory CLI, which only this controller's image carries, so creation
// comes through a Request like every other click. Talking to the
// conversation needs nothing but HTTP to the sandbox's acpd port, which
// the API can already reach — so none of the conversation passes
// through the board. The board's only involvement is this one Request.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/podacpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// What factory stamps on a research sandbox, mirrored here as it is in
// the API: the two programs meet over the CLI, never as one.
const (
	sandboxTypeLabel    = "sandbox.gemini.google.com/type"
	researchSandboxType = "research"
	// researchSessionIDAnnotation holds the full session id — the
	// authoritative match, since the label carries only a digest of it.
	researchSessionIDAnnotation = "sandbox.gemini.google.com/research-session-id"
	// researchReadyAnnotation is written last by `factory research
	// start`, once the checkout is on the disk. It is the only thing that
	// says a sandbox finished being built: factory creates the object in
	// the first second of a launch that runs for minutes, so existence
	// says no more than that something began.
	researchReadyAnnotation = "sandbox.gemini.google.com/research-ready"
)

// researchClaimTTL bounds how long an unserved claim stands.
//
// Every other claim kind is either one-shot or retried forever against
// an idempotent target. Research is neither: the sandbox is per
// conversation, so a claim that never produces one would otherwise stand
// for the full day a Request gets, and a member waiting on a session
// that cannot be created is better told within the hour to start
// another. With the 30m relaunch backoff this allows two attempts.
const researchClaimTTL = time.Hour

// researchSessionIDRE is what a session id may look like.
//
// The id is minted by the API, but it arrives here as a field on a
// Request that anything with write access to the namespace can create,
// and it leaves as an argument to the factory CLI. Checking the shape
// keeps the blast radius of a hand-written Request at "claim ignored".
var researchSessionIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// researchClaim is a click on New research session.
type researchClaim struct {
	// sessionID is the conversation's identity, and the only input to
	// the sandbox's name: the same id always names the same sandbox.
	sessionID string
	member    string
	claimedAt time.Time
	// kickoff is the canned opening, when the click was one of the
	// exploration buttons rather than an empty conversation. It is
	// carried on the claim because the sandbox does not exist yet: the
	// member is minutes and probably a closed tab away by the time
	// anything can be said to the engine.
	kickoff research.Kickoff
}

// researchKey is the runner's single-flight key. The sandbox name
// already carries both the repo and the session, so it identifies the
// invocation exactly.
func researchKey(member, repo, sessionID string) string {
	return fmt.Sprintf("%s/%s", member, factorycli.ResearchSandboxName(repo, sessionID))
}

// researchSandboxDone returns the sandbox for a session once it is
// finished being built, and nil until then.
//
// The sandbox object, not a runner result as explore and runbook use,
// because the sandbox IS the session — one per conversation, named from
// the session id. That makes served-ness durable: a controller restart
// cannot forget it and create a second engine for a conversation that
// already has one. It also means a member who deletes their session is
// not handed it back by a claim that outlived the reap pass.
//
// But the object alone is not enough. `factory research start` creates
// it in the first second and then pulls, waits and clones for minutes,
// so for most of a launch the object exists and the session does not.
// Reading that as served is what left a conversation stuck on
// "opening…" behind a running pod with an empty workspace: the launch
// died mid-setup, the claim was already retired, and nothing was ever
// going to go back and finish it. So the receipt is the annotation
// factory writes last, and an unfinished sandbox leaves the claim
// standing for the relaunch to adopt or replace.
func researchSandboxDone(work *workState, member, sessionID string) *unstructured.Unstructured {
	sb := work.findSandbox(member, factorycli.ResearchSandboxName(work.repo, sessionID))
	if sb == nil || sb.GetAnnotations()[researchReadyAnnotation] == "" {
		return nil
	}
	return sb
}

// researchClaimServed: the sandbox for this session is built and ready.
func (r *Reconciler) researchClaimServed(claim researchClaim, work *workState) bool {
	return researchSandboxDone(work, claim.member, claim.sessionID) != nil
}

// researchClaimExpired reports whether the claim has outlived its TTL.
func researchClaimExpired(claim researchClaim, now time.Time) bool {
	return now.Sub(claim.claimedAt) > researchClaimTTL
}

// ensureResearchClaims creates the sandbox for each standing research
// claim.
//
// No sandbox-wide preflight: `factory research start` runs no agent
// task, so there is nothing in the sandbox to serialize against — and
// the sandbox is brand new anyway, made by this very invocation.
func (r *Reconciler) ensureResearchClaims(ctx context.Context, work *workState, claims []researchClaim) {
	logger := log.FromContext(ctx)
	for _, claim := range claims {
		key := researchKey(claim.member, work.repo, claim.sessionID)
		// Served is checked before running, and the order is load-bearing.
		// The pass that settles Requests reads the same receipt and runs
		// later in this very reconcile, so the claim is gone by the next
		// one. The stamp has to happen in the pass that first sees the
		// receipt, or the session comes up untitled.
		if r.researchClaimServed(claim, work) {
			r.stampResearchTitle(ctx, work, claim)
			continue
		}
		if r.Factory.IsRunning(key) {
			continue
		}
		if researchClaimExpired(claim, time.Now()) {
			continue // the trim pass drops it
		}
		if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
			continue
		}
		// The question is what the recipe's start asks. A canned kind is
		// asked as the text it renders to, which is also what the board's
		// fill controls put in the box.
		topic, err := researchTopic(claim.kickoff, work)
		if err != nil {
			logger.Info("research claim has no question to ask", "session", claim.sessionID, "reason", err.Error())
			continue
		}
		// The token is for the clone only: the recipe is credentials:
		// clone, so factory hands it to that one step and the agent never
		// holds it.
		token, err := r.executorToken(ctx, claim.member)
		if err != nil {
			continue
		}
		if r.Factory.StartResearch(key, factorycli.ResearchOptions{
			Namespace:   claim.member,
			RepoURL:     fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo),
			SessionID:   claim.sessionID,
			Topic:       topic,
			GithubToken: token,
			// Read at launch, like a task's engine: switching the board
			// changes the next session, not the ones already running.
			Engine: acpd.ResearchEngineFor(boardEngine(work.board)),
		}) {
			logger.Info("launched factory recipe research", "session", claim.sessionID, "board", work.board.Name)
		}
	}
}

// researchACPD is the dial seam, mirroring the API's. Production talks
// to the pod, over a forward or on its IP; tests point it at an httptest
// server.
var researchACPD = func(ctx context.Context, d *podacpd.Dialer, pod *corev1.Pod) *acpd.Client {
	return d.Client(ctx, pod)
}

// researchTopic is the question a claim's conversation opens with: the
// member's, or the text a canned kind renders to for the board's repo.
func researchTopic(k research.Kickoff, work *workState) (string, error) {
	if k.Kind == research.KindTopic {
		if t := strings.TrimSpace(k.Topic); t != "" {
			return t, nil
		}
		return "", fmt.Errorf("a topic session needs a topic")
	}
	prompt, err := k.Prompt(work.repo, fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("the claim asks nothing")
	}
	return prompt, nil
}

// stampResearchTitle copies a claim's title onto the sandbox that now
// exists for it.
//
// This is the handoff from the claim to the sandbox, and it has to
// happen in the pass that notices the sandbox: the Request settles later
// in the same reconcile and the claim goes with it, so a title still only
// on the claim at that point is lost.
func (r *Reconciler) stampResearchTitle(ctx context.Context, work *workState, claim researchClaim) {
	title := claim.kickoff.ResolvedTitle()
	if title == "" {
		return
	}
	sb := work.findSandbox(claim.member, factorycli.ResearchSandboxName(work.repo, claim.sessionID))
	if sb == nil {
		return
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// Only ever written once: a member's rename wins.
	if annotations[research.TitleAnnotation] != "" {
		return
	}
	annotations[research.TitleAnnotation] = title
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to record the research title", "session", claim.sessionID)
	}
}

// researchPod returns the sandbox's running pod, or nil when there is
// none to dial.
//
// Read straight from the API server rather than the controller's cache:
// caching pods would mean an informer over every pod in the cluster to
// answer a question asked once per new research session.
func (r *Reconciler) researchPod(ctx context.Context, namespace, sandboxName string) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := r.podReader().List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels{"sandbox": sandboxName}); err != nil {
		return nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod, nil
		}
	}
	return nil, nil
}

// podReader is the uncached reader, falling back to the cached client so
// that a Reconciler built by hand in a test needs no extra wiring.
func (r *Reconciler) podReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
