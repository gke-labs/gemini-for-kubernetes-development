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

// ToolTelemetry captures accumulated tool execution statistics from task runs.
type ToolTelemetry struct {
	TotalCalls       int                 `json:"total_tool_calls"`
	TotalDurationSec float64             `json:"total_tool_duration_sec"`
	Tools            map[string]ToolStat `json:"tools,omitempty"`
	ShellCalls       []ShellCall         `json:"shell_calls,omitempty"`
}

// ToolStat captures per-tool execution statistics.
type ToolStat struct {
	Count      int     `json:"count"`
	TotalSec   float64 `json:"total_sec"`
	MaxSec     float64 `json:"max_sec"`
	SlowestCmd string  `json:"slowest_cmd"`
}

// ShellCall captures one run_shell_command invocation record.
type ShellCall struct {
	Cmd         string  `json:"cmd"`
	DurationSec float64 `json:"duration_sec"`
	Timestamp   string  `json:"timestamp"`

	// Attributed context (filled during harvest)
	Repo     string `json:"repo,omitempty"`
	TaskType string `json:"taskType,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	TaskDir  string `json:"taskDir,omitempty"`
}

// AggregatedCommand captures aggregated statistics for a unique shell command pattern.
type AggregatedCommand struct {
	Cmd            string  `json:"cmd"`
	Count          int     `json:"count"`
	TotalSec       float64 `json:"total_sec"`
	AvgSec         float64 `json:"avg_sec"`
	MaxSec         float64 `json:"max_sec"`
	LastExecutedAt string  `json:"last_executed_at"`
}

// PeriodSlowest contains the top 5 slowest shell command invocations for day, week, and month.
type PeriodSlowest struct {
	Day   []ShellCall `json:"day"`
	Week  []ShellCall `json:"week"`
	Month []ShellCall `json:"month"`
}

// SlowestCommandsResponse is returned by GET /v1/usage/tools.
type SlowestCommandsResponse struct {
	TopSlowestCommands []AggregatedCommand `json:"top_slowest_commands"`
	SlowestByPeriod    PeriodSlowest       `json:"slowest_by_period"`
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

	RecordedAt    string         `json:"recordedAt,omitempty"` // RFC3339
	Stats         Stats          `json:"stats"`
	ToolTelemetry *ToolTelemetry `json:"toolTelemetry,omitempty"`
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

// TaskRecord is the index entry of one archived task, and its folder's
// manifest.json. Key is "<sandbox>/<task>"; a newer harvest of the same
// task replaces it.
type TaskRecord struct {
	Key      string `json:"key"`
	Repo     string `json:"repo,omitempty"`
	Sandbox  string `json:"sandbox"`
	Task     string `json:"task"`
	TaskType string `json:"taskType,omitempty"`
	Issue    int    `json:"issue,omitempty"`
	PR       int    `json:"pr,omitempty"`

	// From the task's own files: task.json, status.json, push.json and
	// task-output.applied. Empty when the task has none.
	Recipe       string `json:"recipe,omitempty"`
	RunName      string `json:"runName,omitempty"`
	URL          string `json:"url,omitempty"`
	State        string `json:"state,omitempty"`
	ExitCode     string `json:"exitCode,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	EndedAt      string `json:"endedAt,omitempty"`
	PushedBranch string `json:"pushedBranch,omitempty"`
	PushedHead   string `json:"pushedHead,omitempty"`
	Applied      string `json:"applied,omitempty"`

	// Instructions describes which instructions the task ran on:
	// workflow, recipe, engine, model, commit, and skills detected in the trace.
	Instructions *Instructions `json:"instructions,omitempty"`

	// Files are the task's files as archived: after redaction, before
	// compression.
	Files []TaskFile `json:"files"`
	// Folder is the task's folder in the archive, under the prefix.
	Folder     string `json:"folder,omitempty"`
	ArchivedAt string `json:"archivedAt,omitempty"`
}

// TaskFile is one archived file of a task, by its name in the task
// directory ("push.json", "session/stream.ndjson").
type TaskFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// TaskFilter selects task records. Zero values mean "no filter".
type TaskFilter struct {
	Repo    string
	Issue   int
	PR      int
	Sandbox string
}

// Instructions maps a task run to the instructions it ran on: workflow, recipe,
// engine, model, commit, and skills detected in the trace.
type Instructions struct {
	Repo     string       `json:"repo,omitempty"`
	Commit   string       `json:"commit,omitempty"`
	Workflow *WorkflowRef `json:"workflow,omitempty"`
	Recipe   *RecipeRef   `json:"recipe,omitempty"`
	Engine   string       `json:"engine,omitempty"`
	Model    string       `json:"model,omitempty"`
	Context  []string     `json:"context,omitempty"`
	Skills   []SkillRef   `json:"skills,omitempty"`
}

// WorkflowRef identifies the workflow instructions.
type WorkflowRef struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// RecipeRef identifies the recipe run.
type RecipeRef struct {
	Name string `json:"name,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// SkillRef records one skill invoked or read during a task session.
type SkillRef struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	How      string `json:"how"` // "activated" | "read"
	FirstSeq int64  `json:"firstSeq"`
	LastSeq  int64  `json:"lastSeq"`
}

// AnsweredBy identifies the care task that addressed a feedback item and the
// outcome of that care pass.
type AnsweredBy struct {
	Task    string `json:"task"`
	Outcome string `json:"outcome"` // "fixed", "replied", "nothing"
}

// FeedbackItem is one human review, inline comment or conversation comment
// on a pull request, captured when the PR is closed.
type FeedbackItem struct {
	Author         string      `json:"author"`
	Kind           string      `json:"kind"` // "review", "inline", "comment"
	State          string      `json:"state,omitempty"`
	Path           string      `json:"path,omitempty"`
	Line           int         `json:"line,omitempty"`
	At             string      `json:"at"`
	InReplyTo      int64       `json:"inReplyTo,omitempty"`
	Body           string      `json:"body"`
	Head           string      `json:"head"`
	AfterBotReview bool        `json:"afterBotReview"`
	AnsweredBy     *AnsweredBy `json:"answeredBy,omitempty"`
}
