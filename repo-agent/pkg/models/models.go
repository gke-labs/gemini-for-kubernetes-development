package models

import (
	"github.com/google/go-github/v39/github"
)

// ReviewAgentOutput defines the structure for the agent's YAML output.
type ReviewAgentOutput struct {
	Note   string                           `yaml:"note"`
	Review *github.PullRequestReviewRequest `yaml:"review"`
	Labels []string                         `yaml:"labels,omitempty"`
}

// Task represents a sandbox task
type Task struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	TaskState         string `json:"taskState"` // from status.taskState
	Result            string `json:"result"`    // from status.result
	CreationTimestamp string `json:"creationTimestamp"`
	AgentDraft        string `json:"agentDraft,omitempty"`
	UserDraft         string `json:"userDraft,omitempty"`
	AgentState        string `json:"agentState,omitempty"`
	AgentStateMessage string `json:"agentStateMessage,omitempty"`
}

// PR represents a pull request
type PR struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Draft             string   `json:"draft,omitempty"`
	Sandbox           string   `json:"sandbox,omitempty"`
	SandboxReplica    string   `json:"sandboxReplica,omitempty"`
	Review            string   `json:"review,omitempty"`
	HTMLURL           string   `json:"htmlURL,omitempty"`
	DiffURL           string   `json:"diffURL,omitempty"`
	AgentDraft        string   `json:"agentDraft,omitempty"`
	AgentState        string   `json:"agentState,omitempty"`
	AgentStateMessage string   `json:"agentStateMessage,omitempty"`
	ReviewState       string   `json:"reviewState,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	Tasks             []Task   `json:"tasks,omitempty"`
}

// Issue represents a GitHub issue
type Issue struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Draft             string   `json:"draft,omitempty"`
	Sandbox           string   `json:"sandbox,omitempty"`
	SandboxReplica    string   `json:"sandboxReplica,omitempty"`
	Comment           string   `json:"comment,omitempty"`
	HTMLURL           string   `json:"htmlURL,omitempty"`
	BranchURL         string   `json:"branchURL,omitempty"`
	PushBranch        bool     `json:"pushBranch"`
	AgentDraft        string   `json:"agentDraft,omitempty"`
	AgentState        string   `json:"agentState,omitempty"`
	AgentStateMessage string   `json:"agentStateMessage,omitempty"`
	SandboxStatus     string   `json:"sandboxStatus,omitempty"`
	Labels            []string `json:"labels,omitempty"`
}

// Repo represents a repository with its configuration
type Repo struct {
	Name                string        `json:"name"`
	Namespace           string        `json:"namespace"`
	URL                 string        `json:"url"`
	Review              *ReviewConfig `json:"review,omitempty"`
	Issue               *IssueConfig  `json:"issue,omitempty"`
	Dev                 *DevConfig    `json:"dev,omitempty"`
	PendingPRs          []int64       `json:"pendingPRs,omitempty"`
	ExcludePullRequests []int64       `json:"excludePullRequests,omitempty"`
	PendingDevBranches  []string      `json:"pendingDevBranches,omitempty"`
	ExcludeBranches     []string      `json:"excludeBranches,omitempty"`
	PendingIssues       []int64       `json:"pendingIssues,omitempty"`
	ExcludeIssues       []int64       `json:"excludeIssues,omitempty"`
}

// ReviewConfig holds configuration for PR reviews
type ReviewConfig struct {
	MaxActiveSandboxes int64    `json:"maxActiveSandboxes"`
	Assignees          []string `json:"assignees,omitempty"`
}

// IssueConfig holds configuration for issues
type IssueConfig struct {
	MaxActiveSandboxes int64          `json:"maxActiveSandboxes"`
	Handlers           []IssueHandler `json:"handlers,omitempty"`
	Issues             []int64        `json:"issues,omitempty"`
}

// IssueHandler holds configuration for an issue handler
type IssueHandler struct {
	Name       string `json:"name"`
	PushBranch bool   `json:"pushBranch"`
}

// DevConfig holds configuration for dev sandboxes
type DevConfig struct {
	MaxActiveSandboxes int64 `json:"maxActiveSandboxes"`
}

// DevSandbox represents a dev sandbox
type DevSandbox struct {
	Name              string   `json:"name"`
	Sandbox           string   `json:"sandbox,omitempty"`
	SandboxReplica    string   `json:"sandboxReplica,omitempty"`
	BranchURL         string   `json:"branchURL,omitempty"`
	Branch            string   `json:"branch,omitempty"`
	AgentState        string   `json:"agentState,omitempty"`
	AgentStateMessage string   `json:"agentStateMessage,omitempty"`
	Labels            []string `json:"labels,omitempty"`
}
