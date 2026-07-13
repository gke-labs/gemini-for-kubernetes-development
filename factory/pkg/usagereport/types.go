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

// Package usagereport pushes per-task gemini-cli token usage to the central
// token-usage collector service. Reporting is best-effort and entirely
// disabled unless COLLECTOR_URL is set; it must never fail a task.
//
// These structs mirror overseer/pkg/tokenusage (the canonical copy); the
// JSON wire format is the contract. The Stats shape also matches the
// token-usage.json files written by the task scripts in factory/pkg/tasks.
package usagereport

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
// up by issue, PR, or workflow on the collector side.
type UsageRecord struct {
	Key      string `json:"key"`
	Repo     string `json:"repo,omitempty"` // "owner/name"
	TaskType string `json:"taskType,omitempty"`
	TaskDir  string `json:"taskDir,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`

	Issue  int   `json:"issue,omitempty"`
	PR     int   `json:"pr,omitempty"`
	Issues []int `json:"issues,omitempty"`

	Workflow     string `json:"workflow,omitempty"` // session id, e.g. "issue-42"
	WorkflowName string `json:"workflowName,omitempty"`

	RecordedAt string `json:"recordedAt,omitempty"` // RFC3339
	Stats      Stats  `json:"stats"`
}

// Rollup is the collector's aggregate of usage records for one key.
type Rollup struct {
	Key          string        `json:"key"`
	Repo         string        `json:"repo,omitempty"`
	WorkflowName string        `json:"workflowName,omitempty"`
	TaskCount    int           `json:"taskCount"`
	Issues       []int         `json:"issues,omitempty"`
	PRs          []int         `json:"prs,omitempty"`
	Stats        Stats         `json:"stats"`
	Records      []UsageRecord `json:"records,omitempty"`
}
