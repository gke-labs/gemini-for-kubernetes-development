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

package usagereport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

const requestTimeout = 5 * time.Second

// Meta carries the issue/PR/workflow context attached to harvested records.
type Meta struct {
	Repo         string // "owner/name"
	TaskType     string // inferred from the task dir name when empty
	Sandbox      string
	Issue        int
	PR           int
	Issues       []int
	Workflow     string // workflow session id, e.g. "issue-42"
	WorkflowName string
}

// Enabled reports whether usage reporting is configured.
func Enabled() bool {
	return collectorURL() != ""
}

func collectorURL() string {
	return strings.TrimRight(os.Getenv("COLLECTOR_URL"), "/")
}

// ReadTaskUsage reads the accumulated usage stats file from a task dir
// inside the sandbox. Returns (nil, nil) when the task wrote no usage file.
func ReadTaskUsage(ctx context.Context, client *envd.Client, taskDir string) (*Stats, error) {
	var stdout, stderr bytes.Buffer
	catCmd := fmt.Sprintf("cat %[1]s/token-usage.json 2>/dev/null || cat %[1]s/llm-usage.json 2>/dev/null || true", taskDir)
	if err := client.Exec(ctx, catCmd, "/workspaces", nil, nil, &stdout, &stderr); err != nil {
		return nil, fmt.Errorf("reading usage file from %s: %w (stderr: %s)", taskDir, err, stderr.String())
	}
	data := strings.TrimSpace(stdout.String())
	if data == "" {
		return nil, nil
	}
	var stats Stats
	if err := json.Unmarshal([]byte(data), &stats); err != nil {
		return nil, fmt.Errorf("parsing usage file from %s: %w", taskDir, err)
	}
	if len(stats.Models) == 0 {
		return nil, nil
	}
	return &stats, nil
}

// Publish upserts a usage record in the collector.
func Publish(ctx context.Context, rec UsageRecord) error {
	return postJSON(ctx, "/v1/usage", rec)
}

// PublishSubject upserts issue/PR metadata (state, timestamps) in the
// collector, which joins it onto rollups to expose age and status.
func PublishSubject(ctx context.Context, sub Subject) error {
	return postJSON(ctx, "/v1/subjects", sub)
}

// ReportIssueSubject publishes an issue's state and timestamps.
// Best-effort: no-op when disabled or issue is nil; failures are logged.
func ReportIssueSubject(ctx context.Context, repo string, issue *githubv39.Issue) {
	if !Enabled() || issue == nil || issue.GetNumber() == 0 {
		return
	}
	sub := Subject{
		Key:       tokenusage.SubjectKey("issue", issue.GetNumber()),
		Repo:      repo,
		Kind:      "issue",
		Number:    issue.GetNumber(),
		State:     issue.GetState(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t := issue.GetCreatedAt(); !t.IsZero() {
		sub.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	if t := issue.GetClosedAt(); !t.IsZero() {
		sub.ClosedAt = t.UTC().Format(time.RFC3339)
	}
	if err := PublishSubject(ctx, sub); err != nil {
		klog.Warningf("usagereport: publishing subject %s: %v", sub.Key, err)
	}
}

// ReportPRSubject publishes a PR's state and timestamps.
// Best-effort: no-op when disabled or pr is nil; failures are logged.
func ReportPRSubject(ctx context.Context, repo string, pr *githubv39.PullRequest) {
	if !Enabled() || pr == nil || pr.GetNumber() == 0 {
		return
	}
	sub := Subject{
		Key:       tokenusage.SubjectKey("pr", pr.GetNumber()),
		Repo:      repo,
		Kind:      "pr",
		Number:    pr.GetNumber(),
		State:     pr.GetState(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t := pr.GetCreatedAt(); !t.IsZero() {
		sub.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	if t := pr.GetClosedAt(); !t.IsZero() {
		sub.ClosedAt = t.UTC().Format(time.RFC3339)
	}
	if err := PublishSubject(ctx, sub); err != nil {
		klog.Warningf("usagereport: publishing subject %s: %v", sub.Key, err)
	}
}

func postJSON(ctx context.Context, path string, v any) error {
	base := collectorURL()
	if base == "" {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	return nil
}

// HarvestTask reads one task dir's usage and publishes it. Best-effort:
// failures are logged, never returned.
func HarvestTask(ctx context.Context, client *envd.Client, taskDir string, meta Meta) {
	if !Enabled() {
		return
	}
	stats, err := ReadTaskUsage(ctx, client, taskDir)
	if err != nil {
		klog.Warningf("usagereport: %v", err)
		return
	}
	if stats == nil {
		return
	}
	taskType := meta.TaskType
	if taskType == "" {
		taskType = taskTypeFromDir(taskDir)
	}
	rec := UsageRecord{
		Key:          meta.Sandbox + ":" + taskDir,
		Repo:         meta.Repo,
		TaskType:     taskType,
		TaskDir:      taskDir,
		Sandbox:      meta.Sandbox,
		Issue:        meta.Issue,
		PR:           meta.PR,
		Issues:       meta.Issues,
		Workflow:     meta.Workflow,
		WorkflowName: meta.WorkflowName,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Stats:        *stats,
	}
	if err := Publish(ctx, rec); err != nil {
		klog.Warningf("usagereport: publishing usage for %s: %v", rec.Key, err)
		return
	}
	klog.V(2).Infof("usagereport: published usage for %s", rec.Key)
}

// HarvestSandbox connects to a sandbox and harvests every task dir in it.
// Used as the sweep path right before a sandbox is deleted. Best-effort.
func HarvestSandbox(ctx context.Context, namespace, sandboxName string, meta Meta) {
	if !Enabled() {
		return
	}
	meta.Sandbox = sandboxName
	client, err := envd.Connect(ctx, namespace, sandboxName)
	if err != nil {
		klog.Warningf("usagereport: connecting to sandbox %s for usage sweep: %v", sandboxName, err)
		return
	}
	defer client.Close()

	var stdout, stderr bytes.Buffer
	if err := client.Exec(ctx, "ls -d /workspaces/tasks/*/ 2>/dev/null || true", "/workspaces", nil, nil, &stdout, &stderr); err != nil {
		klog.Warningf("usagereport: listing task dirs in sandbox %s: %v", sandboxName, err)
		return
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		taskDir := strings.TrimSuffix(strings.TrimSpace(line), "/")
		if taskDir == "" {
			continue
		}
		m := meta
		m.TaskType = "" // infer per dir
		HarvestTask(ctx, client, taskDir, m)
	}
}

// taskTypeFromDir infers the task type from a task dir name such as
// "fix-20260713-101500", "agent-mychore-20260713-101500".
func taskTypeFromDir(taskDir string) string {
	base := path.Base(taskDir)
	if idx := strings.Index(base, "-"); idx > 0 {
		return base[:idx]
	}
	return base
}

// PostWorkflowSummaryIfNeeded posts a one-time token-usage summary comment
// on a workflow issue. The collector's mark-summarized CAS guarantees the
// comment is posted at most once even across watch-loop restarts.
// Best-effort: failures are logged, never returned.
func PostWorkflowSummaryIfNeeded(ctx context.Context, ghClient *githubv39.Client, owner, repo string, issueNum int) {
	if !Enabled() {
		return
	}
	session := fmt.Sprintf("issue-%d", issueNum)
	rollup, err := fetchWorkflowRollup(ctx, owner+"/"+repo, session)
	if err != nil {
		klog.Warningf("usagereport: fetching workflow rollup for %s: %v", session, err)
		return
	}
	if rollup == nil {
		return // no usage recorded, nothing to summarize
	}

	alreadyPosted, err := markSummarized(ctx, session)
	if err != nil {
		klog.Warningf("usagereport: marking workflow %s summarized: %v", session, err)
		return
	}
	if alreadyPosted {
		return
	}

	body := FormatWorkflowSummary(rollup)
	if _, _, err := ghClient.Issues.CreateComment(ctx, owner, repo, issueNum, &githubv39.IssueComment{
		Body: githubv39.String(body),
	}); err != nil {
		klog.Errorf("usagereport: posting workflow usage summary on #%d: %v", issueNum, err)
		return
	}
	klog.Infof("usagereport: posted workflow usage summary on issue #%d", issueNum)
}

func fetchWorkflowRollup(ctx context.Context, repo, session string) (*Rollup, error) {
	base := collectorURL()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := fmt.Sprintf("%s/v1/usage/rollups/workflows/%s?repo=%s", base, url.PathEscape(session), url.QueryEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	var rollup Rollup
	if err := json.NewDecoder(resp.Body).Decode(&rollup); err != nil {
		return nil, err
	}
	return &rollup, nil
}

func markSummarized(ctx context.Context, session string) (bool, error) {
	base := collectorURL()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := fmt.Sprintf("%s/v1/workflows/%s/mark-summarized", base, url.PathEscape(session))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	var result struct {
		AlreadyPosted bool `json:"alreadyPosted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.AlreadyPosted, nil
}

// FormatWorkflowSummary renders the GitHub comment body for a workflow
// usage rollup.
func FormatWorkflowSummary(r *Rollup) string {
	var b strings.Builder
	b.WriteString("## Gemini token usage summary\n\n")
	if r.WorkflowName != "" {
		fmt.Fprintf(&b, "Workflow: **%s**\n", r.WorkflowName)
	}
	fmt.Fprintf(&b, "Tasks: %d", r.TaskCount)
	if len(r.PRs) > 0 {
		prs := make([]string, 0, len(r.PRs))
		for _, n := range r.PRs {
			prs = append(prs, fmt.Sprintf("#%d", n))
		}
		fmt.Fprintf(&b, " | Pull requests: %s", strings.Join(prs, ", "))
	}
	b.WriteString("\n\n")
	b.WriteString("| Model | Requests | Errors | Input | Output | Cached | Thoughts | Total |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")

	models := make([]string, 0, len(r.Stats.Models))
	for m := range r.Stats.Models {
		models = append(models, m)
	}
	sort.Strings(models)

	var total ModelUsage
	for _, m := range models {
		mu := r.Stats.Models[m]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d |\n",
			m, mu.API.TotalRequests, mu.API.TotalErrors,
			mu.Tokens.Input, mu.Tokens.Output, mu.Tokens.Cached, mu.Tokens.Thoughts, mu.Tokens.Total)
		total.API.TotalRequests += mu.API.TotalRequests
		total.API.TotalErrors += mu.API.TotalErrors
		total.Tokens.Input += mu.Tokens.Input
		total.Tokens.Output += mu.Tokens.Output
		total.Tokens.Cached += mu.Tokens.Cached
		total.Tokens.Thoughts += mu.Tokens.Thoughts
		total.Tokens.Total += mu.Tokens.Total
	}
	if len(models) > 1 {
		fmt.Fprintf(&b, "| **Total** | %d | %d | %d | %d | %d | %d | %d |\n",
			total.API.TotalRequests, total.API.TotalErrors,
			total.Tokens.Input, total.Tokens.Output, total.Tokens.Cached, total.Tokens.Thoughts, total.Tokens.Total)
	}
	return b.String()
}
