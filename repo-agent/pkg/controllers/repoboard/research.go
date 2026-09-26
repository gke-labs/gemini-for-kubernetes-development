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
	"fmt"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
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
}

// parseResearchClaim reads one `research-<session id>` mailbox entry,
// whose value is `<member>|<RFC3339>`.
//
// A malformed entry is rejected rather than repaired. The timestamp in
// particular is required, not defaulted to zero as the explore claims
// do: it is what researchClaimTTL measures, and a claim with no clock
// on it could never expire.
func parseResearchClaim(key, value string) (researchClaim, bool) {
	sessionID := strings.TrimPrefix(key, "research-")
	if !researchSessionIDRE.MatchString(sessionID) {
		return researchClaim{}, false
	}
	member, rest, _ := strings.Cut(value, "|")
	if member == "" {
		return researchClaim{}, false
	}
	// Second cut so a later field can be appended the way the runbook
	// claim's intent was, without this parser having to change.
	at, _, _ := strings.Cut(rest, "|")
	claimedAt, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return researchClaim{}, false
	}
	return researchClaim{sessionID: sessionID, member: member, claimedAt: claimedAt}, true
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
		if r.researchClaimServed(claim, work) || researchClaimExpired(claim, time.Now()) {
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
