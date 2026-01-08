package models

import (
	"github.com/google/go-github/v39/github"
)

// AgentOutput defines the structure for the agent's YAML output.
type AgentOutput struct {
	Note   string                           `yaml:"note"`
	Review *github.PullRequestReviewRequest `yaml:"review"`
}

// PR represents a pull request
type PR struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Draft             string `json:"draft,omitempty"`
	Sandbox           string `json:"sandbox,omitempty"`
	SandboxReplica    string `json:"sandboxReplica,omitempty"`
	Review            string `json:"review,omitempty"`
	HTMLURL           string `json:"htmlURL,omitempty"`
	DiffURL           string `json:"diffURL,omitempty"`
	AgentDraft        string `json:"agentDraft,omitempty"`
	AgentState        string `json:"agentState,omitempty"`
	AgentStateMessage string `json:"agentStateMessage,omitempty"`
	ReviewState       string `json:"reviewState,omitempty"`
}

// Issue represents a GitHub issue
type Issue struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Draft             string `json:"draft,omitempty"`
	Sandbox           string `json:"sandbox,omitempty"`
	SandboxReplica    string `json:"sandboxReplica,omitempty"`
	Comment           string `json:"comment,omitempty"`
	HTMLURL           string `json:"htmlURL,omitempty"`
	BranchURL         string `json:"branchURL,omitempty"`
	PushBranch        bool   `json:"pushBranch"`
	AgentDraft        string `json:"agentDraft,omitempty"`
	AgentState        string `json:"agentState,omitempty"`
	AgentStateMessage string `json:"agentStateMessage,omitempty"`
}

// Repo represents a repository with its configuration
type Repo struct {
	Name                string         `json:"name"`
	Namespace           string         `json:"namespace"`
	URL                 string         `json:"url"`
	Review              *ReviewConfig  `json:"review,omitempty"`
	IssueHandlers       []IssueHandler `json:"issueHandlers,omitempty"`
	Dev                 *DevConfig     `json:"dev,omitempty"`
	PendingPRs          []PendingPR    `json:"pendingPRs,omitempty"`
	ExcludePullRequests []int64        `json:"excludePullRequests,omitempty"`
	PendingDevBranches  []string       `json:"pendingDevBranches,omitempty"`
	ExcludeBranches     []string       `json:"excludeBranches,omitempty"`
}

// PendingPR represents a pending pull request
type PendingPR struct {
	Number  int64  `json:"number"`
	Title   string `json:"title,omitempty"`
	HTMLURL string `json:"htmlURL,omitempty"`
}

// ReviewConfig holds configuration for PR reviews
type ReviewConfig struct {
	MaxActiveSandboxes int64    `json:"maxActiveSandboxes"`
	Assignees          []string `json:"assignees,omitempty"`
}

// IssueHandler holds configuration for an issue handler
type IssueHandler struct {
	Name               string `json:"name"`
	MaxActiveSandboxes int64  `json:"maxActiveSandboxes"`
	PushBranch         bool   `json:"pushBranch"`
}

// DevConfig holds configuration for dev sandboxes
type DevConfig struct {
	MaxActiveSandboxes int64 `json:"maxActiveSandboxes"`
}

// DevSandbox represents a dev sandbox
type DevSandbox struct {
	Name              string `json:"name"`
	Sandbox           string `json:"sandbox,omitempty"`
	SandboxReplica    string `json:"sandboxReplica,omitempty"`
	BranchURL         string `json:"branchURL,omitempty"`
	Branch            string `json:"branch,omitempty"`
	AgentState        string `json:"agentState,omitempty"`
	AgentStateMessage string `json:"agentStateMessage,omitempty"`
}
