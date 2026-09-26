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
// comes through the mailbox like every other click. Talking to the
// conversation needs nothing but HTTP to the sandbox's acpd port, which
// the API can already reach — so none of the conversation passes
// through the board. The board's only involvement is this one claim.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
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
)

// researchClaimTTL bounds how long an unserved claim stands.
//
// Every other claim kind is either one-shot or retried forever against
// an idempotent target. Research is neither: the sandbox is per
// conversation, so a claim that never produces one would otherwise sit
// in the annotation permanently, and a member waiting on a session that
// cannot be created is better told to start a new one. With the 30m
// relaunch backoff this allows two attempts.
const researchClaimTTL = time.Hour

// researchSessionIDRE is what may follow "research-" in a claim key.
//
// The id is minted by the API, but it arrives here as text in an
// annotation that anything with write access to the board can set, and
// it leaves as an argument to the factory CLI. Checking the shape keeps
// the blast radius of a hand-edited annotation at "claim ignored".
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

// parseResearchClaim reads one `research-<session id>` mailbox entry.
//
// The key is checked here and the value by the shared decoder: the
// session id is this package's concern because it becomes an argument
// to the factory CLI, while the value's shape is also the API's, which
// writes it and lists the sessions still waiting on it.
func parseResearchClaim(key, value string) (researchClaim, bool) {
	sessionID := strings.TrimPrefix(key, "research-")
	if !researchSessionIDRE.MatchString(sessionID) {
		return researchClaim{}, false
	}
	claim, ok := research.DecodeClaim(value)
	if !ok {
		return researchClaim{}, false
	}
	return researchClaim{
		sessionID: sessionID,
		member:    claim.Member,
		claimedAt: claim.At,
		kickoff:   claim.Kickoff,
	}, true
}

// researchKey is the runner's single-flight key. The sandbox name
// already carries both the repo and the session, so it identifies the
// invocation exactly.
func researchKey(member, repo, sessionID string) string {
	return fmt.Sprintf("%s/%s", member, factorycli.ResearchSandboxName(repo, sessionID))
}

// researchClaimServed: the sandbox for this session exists.
//
// Existence is the receipt, rather than a runner result as explore and
// runbook use, because the sandbox IS the session — one per
// conversation, named from the session id. That makes served-ness
// durable: a controller restart cannot forget it and create a second
// engine for a conversation that already has one. It also means a
// member who deletes their session is not handed it back by a claim
// that outlived the trim pass.
//
// The cost is that a launch which fails AFTER creating the sandbox — a
// clone that could not authenticate, an image that never pulls — is
// read as served and not retried, leaving a sandbox with no checkout.
// Recovery is to delete the session and start another.
func (r *Reconciler) researchClaimServed(claim researchClaim, work *workState) bool {
	return work.findSandbox(claim.member, factorycli.ResearchSandboxName(work.repo, claim.sessionID)) != nil
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
		if r.Factory.IsRunning(key) {
			continue
		}
		if r.researchClaimServed(claim, work) {
			// The sandbox is up: hand the claim's kickoff over to it
			// before the trim pass drops the claim, later in this very
			// reconcile.
			r.stampResearchKickoff(ctx, work, claim)
			continue
		}
		if researchClaimExpired(claim, time.Now()) {
			continue // the trim pass drops it
		}
		if res, ok := r.Factory.LastResult(key); ok && res.Err != nil && time.Since(res.FinishedAt) < launchRetryBackoff {
			continue
		}
		// The token is for the checkout only. The engine credential is
		// deliberately absent: it reaches the engine in acpd's
		// X-Engine-Api-Key header at session create, so that it never
		// lands on the sandbox's disk.
		token, err := r.executorToken(ctx, claim.member)
		if err != nil {
			continue
		}
		if r.Factory.StartResearch(key, factorycli.ResearchOptions{
			Namespace:   claim.member,
			RepoURL:     fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo),
			SessionID:   claim.sessionID,
			GithubToken: token,
		}) {
			logger.Info("launched factory research", "session", claim.sessionID, "board", work.board.Name)
		}
	}
}

// researchKickoffTTL bounds how long the opening turn stays owed.
//
// Measured from the sandbox's creation, because that is when the clock
// the member is watching starts. A pod that has not come up in an hour
// is not coming up, and the alternative — retrying forever — means a
// dead session costs a pod list a minute for as long as it is kept.
const researchKickoffTTL = time.Hour

// researchACPD is the dial seam, mirroring the API's. Production talks
// to the pod; tests point it at an httptest server.
var researchACPD = func(ip string) *acpd.Client { return acpd.NewForPodIP(ip) }

// stampResearchKickoff copies a claim's title and canned opening onto
// the sandbox that now exists for it.
//
// This is the handoff from the claim to the sandbox, and it has to
// happen in the pass that notices the sandbox: trimMailbox runs later in
// the same reconcile and drops a served claim, so anything still only on
// the claim at that point is lost. The sandbox outlives the claim by as
// long as the conversation does, which is exactly the lifetime the
// opening prompt needs — the pod will not be up for minutes yet.
func (r *Reconciler) stampResearchKickoff(ctx context.Context, work *workState, claim researchClaim) {
	if claim.kickoff == (research.Kickoff{}) {
		return // a session the member will type into themselves
	}
	sb := work.findSandbox(claim.member, factorycli.ResearchSandboxName(work.repo, claim.sessionID))
	if sb == nil {
		return
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// Only ever written once. A second stamp could resurrect an opening
	// that was already sent and deleted, and the send is not free — it
	// is a turn of engine time in someone's transcript.
	if annotations[research.KickoffAnnotation] != "" || annotations[research.TitleAnnotation] != "" {
		return
	}
	annotations[research.KickoffAnnotation] = claim.kickoff.Encode()
	if title := claim.kickoff.ResolvedTitle(); title != "" {
		annotations[research.TitleAnnotation] = title
	}
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to record the research kickoff", "session", claim.sessionID)
	}
}

// sendResearchKickoffs delivers the opening turn of every session that
// is still owed one.
//
// Driven from the sandboxes rather than the mailbox because the claim is
// long gone by the time this can succeed. It runs on the reconcile loop,
// so a sandbox that is still pulling its image is simply retried a
// minute later; nothing here waits.
func (r *Reconciler) sendResearchKickoffs(ctx context.Context, work *workState) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		annotations := sb.GetAnnotations()
		if annotations[research.KickoffAnnotation] == "" || annotations[research.KickoffErrorAnnotation] != "" {
			continue
		}
		if sb.GetLabels()[sandboxTypeLabel] != researchSandboxType {
			continue
		}
		// A paused sandbox has no engine to talk to, and un-pausing is
		// the member's call. The kickoff waits, and expires with the TTL
		// like any other undeliverable one.
		if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
			continue
		}
		if err := r.sendResearchKickoff(ctx, work, sb); err != nil {
			// Expected for most of a sandbox's first minutes: no pod, no
			// acpd, no engine yet. Logged at info, and only given up on
			// when the TTL runs out.
			logger.Info("research kickoff not delivered yet", "sandbox", sb.GetName(), "reason", err.Error())
			if ts := sb.GetCreationTimestamp(); !ts.IsZero() && time.Since(ts.Time) > researchKickoffTTL {
				r.abandonResearchKickoff(ctx, sb, err)
			}
		}
	}
}

// sendResearchKickoff opens the conversation for one sandbox.
func (r *Reconciler) sendResearchKickoff(ctx context.Context, work *workState, sb *unstructured.Unstructured) error {
	annotations := sb.GetAnnotations()
	sessionID := annotations[researchSessionIDAnnotation]
	if sessionID == "" {
		return fmt.Errorf("the sandbox carries no session id")
	}
	kickoff := research.DecodeKickoff(annotations[research.KickoffAnnotation])
	repo := annotations["repo"]
	if repo == "" {
		repo = work.repo
	}
	htmlURL := annotations["htmlURL"]
	if htmlURL == "" {
		htmlURL = fmt.Sprintf("https://github.com/%s/%s", work.owner, repo)
	}
	prompt, err := kickoff.Prompt(repo, htmlURL)
	if err != nil {
		return err
	}
	if prompt == "" {
		// Nothing to say — a kickoff that decoded to the zero value.
		// Clearing it is the honest outcome: the session is a typed one.
		return r.clearResearchKickoff(ctx, sb)
	}

	podIP, err := r.researchPodIP(ctx, sb.GetNamespace(), sb.GetName())
	if err != nil {
		return err
	}
	if podIP == "" {
		return fmt.Errorf("no running pod yet")
	}
	client := researchACPD(podIP)

	// Bounded: this runs inline on the reconcile loop, and a sandbox
	// whose acpd is wedged must not hold the board's other work up.
	ctx, cancel := context.WithTimeout(ctx, researchKickoffTimeout)
	defer cancel()

	// The opening turn goes only into a conversation that has never had
	// one. Everything else here is idempotent by construction, but a
	// prompt is not: if the annotation survived a delivery whose receipt
	// failed to write, this is what stops the engine being asked twice.
	session, err := client.GetSession(ctx, sessionID)
	switch {
	case err == nil && session.Offset > 0:
		return r.clearResearchKickoff(ctx, sb)
	case err == nil:
		// A live, silent session: created by someone opening the tab
		// before the pod finished booting. Prompt it as it stands.
	case errors.Is(err, acpd.ErrNotFound):
		apiKey, kerr := r.engineAPIKey(ctx, sb.GetNamespace())
		if kerr != nil {
			return kerr
		}
		// The key is supplied here and nowhere else: acpd holds it only
		// until the engine child is spawned, and it never touches the
		// sandbox's disk.
		if _, cerr := client.CreateSession(ctx, acpd.CreateSessionRequest{
			ID:     sessionID,
			Engine: acpd.EngineGemini,
			CWD:    "/workspaces/" + repo,
		}, apiKey); cerr != nil {
			return cerr
		}
	default:
		return err
	}

	if _, err := client.Prompt(ctx, sessionID, prompt); err != nil {
		return err
	}
	log.FromContext(ctx).Info("sent the research kickoff", "sandbox", sb.GetName(), "kind", kickoff.Kind)
	return r.clearResearchKickoff(ctx, sb)
}

// researchKickoffTimeout bounds one delivery attempt. Generous, because
// creating a session spawns the engine and waits for its handshake.
const researchKickoffTimeout = 30 * time.Second

// clearResearchKickoff records that the opening turn is no longer owed.
func (r *Reconciler) clearResearchKickoff(ctx context.Context, sb *unstructured.Unstructured) error {
	annotations := sb.GetAnnotations()
	delete(annotations, research.KickoffAnnotation)
	sb.SetAnnotations(annotations)
	return r.Update(ctx, sb)
}

// abandonResearchKickoff gives up, visibly.
//
// The kickoff annotation stays: together with the error it says "this
// session was supposed to open with something, and here is why it did
// not", which is what the member needs to decide whether to ask it
// themselves or throw the session away.
func (r *Reconciler) abandonResearchKickoff(ctx context.Context, sb *unstructured.Unstructured, cause error) {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[research.KickoffErrorAnnotation] = cause.Error()
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to record a failed research kickoff", "sandbox", sb.GetName())
	}
}

// researchPodIP returns the sandbox pod's address, or "" when there is
// no running pod to dial.
//
// Read straight from the API server rather than the controller's cache:
// caching pods would mean an informer over every pod in the cluster to
// answer a question asked once per new research session.
func (r *Reconciler) researchPodIP(ctx context.Context, namespace, sandboxName string) (string, error) {
	pods := &corev1.PodList{}
	if err := r.podReader().List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels{"sandbox": sandboxName}); err != nil {
		return "", err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod.Status.PodIP, nil
		}
	}
	return "", nil
}

// podReader is the uncached reader, falling back to the cached client so
// that a Reconciler built by hand in a test needs no extra wiring.
func (r *Reconciler) podReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// engineAPIKey reads the member's engine credential, which this
// controller put in the factory-user secret in the first place.
func (r *Reconciler) engineAPIKey(ctx context.Context, namespace string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: factoryUserSecretName, Namespace: namespace}, secret); err != nil {
		return "", fmt.Errorf("reading the factory-user secret: %w", err)
	}
	key := string(secret.Data[factoryKeyGeminiAPIKey])
	if key == "" {
		return "", fmt.Errorf("no %s in this namespace's factory-user secret", factoryKeyGeminiAPIKey)
	}
	return key, nil
}
