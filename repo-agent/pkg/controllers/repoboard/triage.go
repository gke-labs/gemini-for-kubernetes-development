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
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Triage intake (design D4): `factory recipe triage` prepares label /
// priority / duplicate suggestions per inbound issue. It writes nothing to
// GitHub; its result, a Triage task output, is read back by the task's
// run name (factorycli.StartTriage). The maintainer applies suggestions
// with their own clicks.
//
// It runs in the issue's sandbox, the one plan and fix use
// (factorycli.IssueSandbox): a clicked triage in the clicker's namespace,
// as a clicked plan or fix runs, under their token; auto-triage, which has
// no clicker, in the board owner's, under the discovery identity. It
// records its state in its own annotation and its draft in
// AnnotationTriageDraft so that neither reads as the plan's, fix's or a
// review's.

const (
	// AnnotationTriagedAt marks a stored triage draft.
	AnnotationTriagedAt = "board.gemini.google.com/triaged-at"
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

// ensureTriage drives one issue's triage state machine in namespace:
// harvest a finished run's stdout report, or launch one within limits.
func (r *Reconciler) ensureTriage(ctx context.Context, work *workState, issue *github.Issue, namespace, runName string, clicked bool) {
	logger := log.FromContext(ctx)
	sb := work.triageSandbox(namespace, issue.GetNumber())
	if sb == nil {
		sb = work.issueSandbox(namespace, issue.GetNumber())
	}
	name := factorycli.FixSandboxName(work.repo, issue.GetNumber())
	if sb != nil {
		name = sb.GetName()
	}
	// The single-flight key only has to be stable.
	key := fmt.Sprintf("%s/triage-%s-%d", namespace, work.repo, issue.GetNumber())

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
		if res.Err == nil && sb != nil {
			if report := factorycli.ExtractTriageYAML(res.Output); report != "" {
				annotations[factorycli.AnnotationTriageDraft] = report
				setOrDelete(annotations, factorycli.AnnotationTriageOutput, factorycli.TriageTaskOutput(res.Output))
				delete(annotations, factorycli.AnnotationTriageLabeled)
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

	if sb == nil && r.activeCount(work) >= maxActive(work.board) {
		return
	}
	token := work.discToken
	if namespace != work.board.Namespace {
		var err error
		if token, err = r.executorToken(ctx, namespace); err != nil {
			logger.Info("no github token for the triage's namespace", "namespace", namespace, "err", err)
			return
		}
	}
	if !clicked {
		runName = r.resumableRun(key, annotations, factorycli.AnnotationTriageRun, runName, AnnotationTriageRejected)
	}
	r.stampUnpaused(ctx, sb)
	r.stampEngine(ctx, sb, boardEngine(work.board))
	if r.Factory.StartTriage(key, factorycli.TriageOptions{
		Namespace:   namespace,
		SandboxName: name,
		IssueURL:    issue.GetHTMLURL(),
		GithubToken: token,
		Engine:      boardEngine(work.board),
		RunName:     runName,
	}) {
		logger.Info("launched factory recipe triage", "issue", issue.GetNumber(), "board", work.board.Name)
	}
}

// resumeTriages re-drives triages whose suggestions have not been
// harvested: a clicked triage's Request settles when the sandbox
// appears, minutes before the run completes, and a restarted controller
// has no invocation waiting on the run at all. ensureTriage picks the
// sandbox's recorded run up by name, so its result is stored rather than
// the triage run again. Only sandboxes a triage has touched count: an
// issue's sandbox that only planned or fixed is not one to triage.
func (r *Reconciler) resumeTriages(ctx context.Context, work *workState) {
	seen := map[string]bool{}
	for _, sb := range work.sandboxes {
		annotations := sb.GetAnnotations()
		if annotations[AnnotationTriagedAt] != "" {
			continue
		}
		n, ok := factorycli.IssueOf(sb, work.repo)
		if !ok || !factorycli.HasTriage(sb) {
			continue
		}
		k := fmt.Sprintf("%s/%d", sb.GetNamespace(), n)
		if seen[k] {
			continue
		}
		seen[k] = true
		num := n
		url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, n)
		if u := annotations["htmlURL"]; strings.Contains(u, "/issues/") {
			url = u
		}
		r.ensureTriage(ctx, work, &github.Issue{Number: &num, HTMLURL: &url}, sb.GetNamespace(), triageRunName(work.board.Name, n, triageClick{}), false)
	}
}

// triageClick is a Triage click: the issue, the namespace of the member
// who clicked, where it runs, and the UID of the click's Request.
type triageClick struct {
	issue   int
	member  string
	request string
}

// triageRunName is what a triage's task is recorded under in the sandbox
// (factory --run-name), to read its result by. factory runs a name
// once — running it again returns that run — so it names one attempt:
// one per click, by its Request; one per launch for auto-triage, so that
// a failed run is retried rather than returned. A run this controller
// did not start (a restart's) is resumed under its recorded name instead
// (resumableRun). Identifiers only: anyone in the sandbox can read it.
func triageRunName(board string, issue int, click triageClick) string {
	if click.request != "" {
		return "request/" + click.request
	}
	return fmt.Sprintf("auto/%s/%d/%d", board, issue, time.Now().Unix())
}

// resultStaleSince reports whether a remembered invocation result predates
// the given marker (RFC3339) and must be ignored: a rejected draft's old
// result would otherwise resurrect it, or its backoff would block the
// member's fresh click.
func resultStaleSince(marker string, finishedAt time.Time) bool {
	at, err := time.Parse(time.RFC3339, marker)
	return err == nil && finishedAt.Before(at)
}
