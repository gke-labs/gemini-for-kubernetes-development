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
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v39/github"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// Triage intake (design D4): a draft-only factory agent prepares label /
// priority / duplicate suggestions per inbound issue. It runs in the board
// namespace under the discovery identity, writes nothing to GitHub
// (skipPR), and its report is harvested from the sandbox's gemini output
// via `factory sandbox exec` — the maintainer applies suggestions with
// their own clicks.

//go:embed triage_agent.md
var triageAgentDefinition []byte

const (
	// AnnotationTriagedAt marks a stored triage draft.
	AnnotationTriagedAt = "board.gemini.google.com/triaged-at"
	// AnnotationDraftType distinguishes triage drafts from review drafts on
	// the shared agentDraft key (legacy name the UI already understands).
	AnnotationDraftType = "agentDraftType"

	triageAgentSlug = "triage"
)

var triageAgentFile = struct {
	sync.Once
	path string
	err  error
}{}

// triageAgentPath materializes the embedded agent definition once per
// process for `factory agent create --local`.
func triageAgentPath() (string, error) {
	triageAgentFile.Do(func() {
		f, err := os.CreateTemp("", "triage-agent-*.md")
		if err != nil {
			triageAgentFile.err = err
			return
		}
		defer f.Close()
		if _, err := f.Write(triageAgentDefinition); err != nil {
			triageAgentFile.err = err
			return
		}
		triageAgentFile.path = f.Name()
	})
	return triageAgentFile.path, triageAgentFile.err
}

// triageSandboxName mirrors factory's EnsureAgentSandbox naming:
// agent-<repo>-issue-<n>-<agent-slug>.
func (w *workState) triageSandboxName(issue int) string {
	return fmt.Sprintf("agent-%s-issue-%d-%s", w.repo, issue, triageAgentSlug)
}

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
// run, or launch one within limits.
func (r *Reconciler) ensureTriage(ctx context.Context, work *workState, issue *github.Issue) {
	logger := log.FromContext(ctx)
	name := work.triageSandboxName(issue.GetNumber())
	sb := work.findSandbox(work.board.Namespace, name)
	key := fmt.Sprintf("%s/triage-%d", work.board.Namespace, issue.GetNumber())

	if sb != nil && sb.GetAnnotations()[AnnotationTriagedAt] != "" {
		return
	}
	if r.Factory.IsRunning(key) {
		return
	}

	if res, ok := r.Factory.LastResult(key); ok {
		if res.Err == nil && sb != nil {
			report, err := r.harvestTriageReport(work, name)
			if err != nil {
				logger.Error(err, "unable to harvest triage report", "issue", issue.GetNumber())
			} else if report != "" {
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
	agentFile, err := triageAgentPath()
	if err != nil {
		logger.Error(err, "unable to materialize triage agent definition")
		return
	}
	if r.Factory.StartAgent(key, factorycli.AgentOptions{
		Namespace:   work.board.Namespace,
		URL:         issue.GetHTMLURL(),
		AgentFile:   agentFile,
		GithubToken: work.discToken,
	}) {
		logger.Info("launched triage agent", "issue", issue.GetNumber(), "board", work.board.Name)
	}
}

// harvestTriageReport pulls the agent's final response out of the sandbox
// (gemini-output.json of the latest agent task dir) and trims it to the
// triage YAML block.
func (r *Reconciler) harvestTriageReport(work *workState, sandboxName string) (string, error) {
	out, err := r.Factory.Exec(work.board.Namespace, sandboxName,
		"jq -r .response $(ls -td /workspaces/tasks/agent-*/gemini-output.json | head -1)")
	if err != nil {
		return "", fmt.Errorf("sandbox exec: %w (output: %s)", err, tailOf(out, 512))
	}
	return extractTriageYAML(out), nil
}

// extractTriageYAML trims a model response to the triage YAML block.
func extractTriageYAML(response string) string {
	idx := strings.LastIndex(response, "triage:")
	if idx < 0 {
		return ""
	}
	report := response[idx:]
	if end := strings.Index(report, "```"); end >= 0 {
		report = report[:end]
	}
	return strings.TrimSpace(report)
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
