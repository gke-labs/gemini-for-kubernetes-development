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
// the factory binary and serves per-issue/PR/workflow rollups.
//
// The Stats JSON shape is the wire contract with the producers; it matches
// the token-usage.json files written by the task scripts. A mirrored copy of
// these structs lives in factory/pkg/usagereport (factory must not import
// this module).
package tokenusage

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
// (issue number, PR number, or workflow session).
type Rollup struct {
	Key          string        `json:"key"`
	Repo         string        `json:"repo,omitempty"`
	WorkflowName string        `json:"workflowName,omitempty"`
	TaskCount    int           `json:"taskCount"`
	Issues       []int         `json:"issues,omitempty"`
	PRs          []int         `json:"prs,omitempty"`
	Stats        Stats         `json:"stats"`
	Records      []UsageRecord `json:"records,omitempty"` // only in detail responses
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
