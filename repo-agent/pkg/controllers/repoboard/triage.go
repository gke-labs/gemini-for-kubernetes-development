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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Triage intake (design D4): `factory triage --publish no` prepares label /
// priority / duplicate suggestions per inbound issue. It runs in the board
// namespace under the discovery identity, writes nothing to GitHub, and the
// structured report is harvested from the invocation's stdout banners —
// the same contract the review flow uses. The maintainer applies
// suggestions with their own clicks.
//
// It runs in the issue's sandbox in the board namespace, the one plan and
// fix use (factorycli.IssueSandbox), recording its state in its own
// annotation and its draft in AnnotationTriageDraft so that neither reads
// as the plan's, fix's or a review's. Triages from before that live in
// triage-<repo>-<N> sandboxes, which are still read (factorycli.TriageSandbox).

const (
	// AnnotationTriagedAt marks a stored triage draft.
	AnnotationTriagedAt = "board.gemini.google.com/triaged-at"
	// AnnotationDraftType distinguished triage drafts from review drafts on
	// the shared agentDraft key, where legacy triage sandboxes keep theirs.
	AnnotationDraftType = "agentDraftType"
	// AnnotationTriageRejected tombstones a rejected draft: auto-triage
	// must not redo work a human threw away, and a stale invocation
	// result must not resurrect the draft. A fresh Triage click re-arms.
	AnnotationTriageRejected = "board.gemini.google.com/triage-rejected-at"
	// AnnotationTriagePublished marks a draft the maintainer published.
	AnnotationTriagePublished = "board.gemini.google.com/triage-published-at"
)

// discoverTriage lists open issues needing auto-triage: not PRs, eligible
// under the universal auto filters (recency, labels, veto), and — for the
// "unclaimed" scope — carrying no assignees (anyone's assignment is a
// claim; "all" is for repos where assignment does not imply triaged).
func (r *Reconciler) discoverTriage(ctx context.Context, ghClient *github.Client, work *workState) ([]*github.Issue, error) {
	unclaimedOnly := work.board.Spec.Auto.Triage != "all"
	var candidates []*github.Issue
	opts := &github.IssueListByRepoOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		items, resp, err := ghClient.Issues.ListByRepo(ctx, work.owner, work.repo, opts)
		if err != nil {
			return candidates, err
		}
		for _, item := range items {
			if item.IsPullRequest() {
				continue
			}
			if unclaimedOnly && len(item.Assignees) > 0 {
				continue
			}
			if !autoEligible(work.board, item.Labels, item.GetUpdatedAt()) {
				continue
			}
			candidates = append(candidates, item)
		}
		if resp.NextPage == 0 {
			return candidates, nil
		}
		opts.Page = resp.NextPage
	}
}

// ensureTriage drives one issue's triage state machine: harvest a finished
// run's stdout report, or launch one within limits.
func (r *Reconciler) ensureTriage(ctx context.Context, work *workState, issue *github.Issue, clicked bool) {
	logger := log.FromContext(ctx)
	sb := work.triageSandbox(issue.GetNumber())
	if sb == nil {
		sb = work.issueSandbox(work.board.Namespace, issue.GetNumber())
	}
	name := factorycli.FixSandboxName(work.repo, issue.GetNumber())
	if sb != nil && !factorycli.IsLegacyTriageSandbox(sb) {
		name = sb.GetName()
	}
	// The single-flight key keeps the old sandbox name: it only has to be
	// stable, and a run in flight across a rollout keeps its slot.
	key := work.board.Namespace + "/" + factorycli.LegacyTriageSandboxName(work.repo, issue.GetNumber())

	annotations := map[string]string{}
	if sb != nil && sb.GetAnnotations() != nil {
		annotations = sb.GetAnnotations()
	}
	if annotations[AnnotationTriagedAt] != "" {
		return
	}
	if !clicked && annotations[AnnotationTriageRejected] != "" {
		// A human rejected the last draft: automation does not redo
		// thrown-away work. A fresh Triage click re-arms.
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok && !resultStaleSince(annotations[AnnotationTriageRejected], res.FinishedAt) {
		// A finished run's sandbox is the issue's; a legacy triage one
		// still found here is not where it ran.
		if sb != nil && factorycli.IsLegacyTriageSandbox(sb) {
			if isb := work.issueSandbox(work.board.Namespace, issue.GetNumber()); isb != nil {
				sb, annotations = isb, isb.GetAnnotations()
				if annotations == nil {
					annotations = map[string]string{}
				}
			}
		}
		if res.Err == nil && sb != nil {
			if report := factorycli.ExtractTriageYAML(res.Output); report != "" {
				if factorycli.IsLegacyTriageSandbox(sb) {
					// Where an older repo-agent would look for it.
					annotations[AnnotationAgentDraft] = report
					annotations[AnnotationDraftType] = "triage"
				} else {
					annotations[factorycli.AnnotationTriageDraft] = report
				}
				annotations[AnnotationTriagedAt] = time.Now().UTC().Format(time.RFC3339)
				annotations[AnnotationBoard] = work.board.Name
				sb.SetAnnotations(annotations)
				if err := r.Update(ctx, sb); err != nil {
					logger.Error(err, "unable to store triage draft", "issue", issue.GetNumber())
				}
				return
			}
		}
		if time.Since(res.FinishedAt) < launchRetryBackoff {
			return
		}
	}

	if sb != nil && factorycli.IsLegacyTriageSandbox(sb) {
		// A new run goes to the issue's sandbox, not the legacy one.
		sb = work.issueSandbox(work.board.Namespace, issue.GetNumber())
	}
	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		return
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	if r.Factory.StartTriage(key, factorycli.TriageOptions{
		Namespace:   work.board.Namespace,
		SandboxName: name,
		IssueURL:    issue.GetHTMLURL(),
		GithubToken: work.discToken,
		Engine:      boardEngine(work.board),
	}) {
		logger.Info("launched factory triage", "issue", issue.GetNumber(), "board", work.board.Name)
	}
}

// resumeTriages re-drives triages whose suggestions have not been
// harvested: a clicked triage's Request settles when the sandbox
// appears, minutes before the run completes, so without this pass the
// finished invocation's output would never be stored (and the row would
// show Triaging… forever). Only sandboxes a triage has touched count: an
// issue's sandbox that only planned or fixed is not one to triage.
func (r *Reconciler) resumeTriages(ctx context.Context, work *workState) {
	legacyPrefix := "triage-" + work.repo + "-"
	seen := map[int]bool{}
	for _, sb := range work.sandboxes {
		if sb.GetNamespace() != work.board.Namespace {
			continue
		}
		annotations := sb.GetAnnotations()
		if annotations[AnnotationTriagedAt] != "" {
			continue
		}
		var n int
		if rest, ok := strings.CutPrefix(sb.GetName(), legacyPrefix); ok {
			var err error
			if n, err = strconv.Atoi(rest); err != nil {
				continue
			}
		} else if issue, ok := factorycli.IssueOf(sb, work.repo); ok && factorycli.HasTriage(sb) {
			n = issue
		} else {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		num := n
		url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, n)
		if u := annotations["htmlURL"]; strings.Contains(u, "/issues/") {
			url = u
		}
		r.ensureTriage(ctx, work, &github.Issue{Number: &num, HTMLURL: &url}, false)
	}
}

// resultStaleSince reports whether a remembered invocation result predates
// the given marker (RFC3339) and must be ignored: a rejected draft's old
// result would otherwise resurrect it, or its backoff would block the
// member's fresh click.
func resultStaleSince(marker string, finishedAt time.Time) bool {
	at, err := time.Parse(time.RFC3339, marker)
	return err == nil && finishedAt.Before(at)
}
