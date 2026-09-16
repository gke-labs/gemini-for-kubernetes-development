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

const (
	// AnnotationTriagedAt marks a stored triage draft.
	AnnotationTriagedAt = "board.gemini.google.com/triaged-at"
	// AnnotationDraftType distinguishes triage drafts from review drafts on
	// the shared agentDraft key (legacy name the UI already understands).
	AnnotationDraftType = "agentDraftType"
)

// discoverTriage lists open issues needing triage: not PRs, not vetoed, and
// not already routed to the fix flow via the trigger label.
func (r *Reconciler) discoverTriage(ctx context.Context, ghClient *github.Client, work *workState) ([]*github.Issue, error) {
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
			if vetoed(item.Labels, work.board.Spec.Intake.Filters.ExcludeLabels) {
				continue
			}
			if work.board.Spec.Triggers.Label != "" && hasGithubLabel(item.Labels, work.board.Spec.Triggers.Label) {
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

func hasGithubLabel(labels []*github.Label, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l.GetName(), name) {
			return true
		}
	}
	return false
}

// ensureTriage drives one issue's triage state machine: harvest a finished
// run's stdout report, or launch one within limits.
func (r *Reconciler) ensureTriage(ctx context.Context, work *workState, issue *github.Issue) {
	logger := log.FromContext(ctx)
	name := factorycli.TriageSandboxName(work.repo, issue.GetNumber())
	sb := work.findSandbox(work.board.Namespace, name)
	key := work.board.Namespace + "/" + name

	if sb != nil && sb.GetAnnotations()[AnnotationTriagedAt] != "" {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok {
		if res.Err == nil && sb != nil {
			if report := factorycli.ExtractTriageYAML(res.Output); report != "" {
				annotations := sb.GetAnnotations()
				if annotations == nil {
					annotations = map[string]string{}
				}
				annotations[AnnotationAgentDraft] = report
				annotations[AnnotationDraftType] = "triage"
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

	if sb == nil && r.activeCount(work) >= work.board.Spec.Limits.MaxActive {
		return
	}
	if r.Factory.StartTriage(key, factorycli.TriageOptions{
		Namespace:   work.board.Namespace,
		IssueURL:    issue.GetHTMLURL(),
		GithubToken: work.discToken,
	}) {
		logger.Info("launched factory triage", "issue", issue.GetNumber(), "board", work.board.Name)
	}
}
