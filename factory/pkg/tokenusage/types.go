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

// Package tokenusage implements the token-usage collector service: a small
// HTTP server that durably records per-task gemini-cli usage stats pushed by
// factory task commands and serves per-issue/PR/workflow rollups. It runs as
// the hidden "factory token-daemon" command; the deployment manifests live in
// overseer/k8s.
//
// The Stats JSON shape is the wire contract with the producers; it matches
// the token-usage.json files written by the task scripts in factory/pkg/tasks.
package tokenusage

import "fmt"

// Stats captures accumulated usage statistics from LLM invocations,
// keyed by model name.
type Stats struct {
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// ModelUsage captures per-model usage statistics.
type ModelUsage struct {
	API    APIUsage   `json:"api"`
	Tokens TokenUsage `json:"tokens"`
}

// APIUsage captures API call statistics for a model.
type APIUsage struct {
	TotalRequests  int64 `json:"totalRequests"`
	TotalErrors    int64 `json:"totalErrors"`
	TotalLatencyMs int64 `json:"totalLatencyMs"`
}

// TokenUsage captures token consumption for a model.
type TokenUsage struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Total    int64 `json:"total"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
}

// UsageRecord is one harvested task's usage, with enough context to roll it
// up by issue, PR, or workflow. Key is the idempotency key
// "<sandbox>:<taskDir>"; re-posting the same key upserts the record.
type UsageRecord struct {
	Key      string `json:"key"`
	Repo     string `json:"repo,omitempty"` // "owner/name"
	TaskType string `json:"taskType,omitempty"`
	TaskDir  string `json:"taskDir,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`

	Issue  int   `json:"issue,omitempty"`
	PR     int   `json:"pr,omitempty"`
	Issues []int `json:"issues,omitempty"` // issues referenced by the PR

	// Workflow is the workflow session id (e.g. "issue-42") for tasks that
	// belong to a workflow; WorkflowName is the human-readable name.
	Workflow     string `json:"workflow,omitempty"`
	WorkflowName string `json:"workflowName,omitempty"`

	RecordedAt string `json:"recordedAt,omitempty"` // RFC3339
	Stats      Stats  `json:"stats"`
}

// Rollup is an aggregate of usage records grouped by some key
// (issue number, PR number, workflow session, or day).
type Rollup struct {
	Key          string `json:"key"`
	Repo         string `json:"repo,omitempty"`
	WorkflowName string `json:"workflowName,omitempty"`
	TaskCount    int    `json:"taskCount"`
	Issues       []int  `json:"issues,omitempty"`
	PRs          []int  `json:"prs,omitempty"`

	// Subject metadata (joined from the matching Subject, when reported):
	// GitHub state and timestamps of the underlying issue/PR, used to show
	// age and open/closed status.
	State     string `json:"state,omitempty"` // "open" | "closed"
	CreatedAt string `json:"createdAt,omitempty"`
	ClosedAt  string `json:"closedAt,omitempty"`

	Stats   Stats         `json:"stats"`
	Records []UsageRecord `json:"records,omitempty"`
}

// Subject tracks GitHub metadata of an entity that usage is attributed to
// (an issue or a PR): open/closed state and creation/close timestamps.
// Producers upsert subjects alongside usage records; rollups join on the
// subject key to expose age and status.
type Subject struct {
	Key       string `json:"key"` // "issue-<n>" or "pr-<n>"
	Repo      string `json:"repo,omitempty"`
	Kind      string `json:"kind"` // "issue" | "pr"
	Number    int    `json:"number"`
	State     string `json:"state,omitempty"`     // "open" | "closed"
	CreatedAt string `json:"createdAt,omitempty"` // RFC3339, GitHub creation time
	ClosedAt  string `json:"closedAt,omitempty"`  // RFC3339, set once closed
	UpdatedAt string `json:"updatedAt,omitempty"` // RFC3339, last upsert
}

// SubjectKey builds the canonical subject key for a kind and number.
func SubjectKey(kind string, number int) string {
	return fmt.Sprintf("%s-%d", kind, number)
}

// MergeStats accumulates src into dst per model.
func MergeStats(dst *Stats, src Stats) {
	if dst.Models == nil {
		dst.Models = map[string]ModelUsage{}
	}
	for model, mu := range src.Models {
		agg := dst.Models[model]
		agg.API.TotalRequests += mu.API.TotalRequests
		agg.API.TotalErrors += mu.API.TotalErrors
		agg.API.TotalLatencyMs += mu.API.TotalLatencyMs
		agg.Tokens.Input += mu.Tokens.Input
		agg.Tokens.Output += mu.Tokens.Output
		agg.Tokens.Total += mu.Tokens.Total
		agg.Tokens.Cached += mu.Tokens.Cached
		agg.Tokens.Thoughts += mu.Tokens.Thoughts
		dst.Models[model] = agg
	}
}
