package models

import (
	"github.com/google/go-github/v39/github"
)

// DraftReviewComment defines the structure for a review comment with severity
type DraftReviewComment struct {
	Path      *string `yaml:"path,omitempty" json:"path,omitempty"`
	Position  *int    `yaml:"position,omitempty" json:"position,omitempty"`
	Body      *string `yaml:"body,omitempty" json:"body,omitempty"`
	Line      *int    `yaml:"line,omitempty" json:"line,omitempty"`
	Side      *string `yaml:"side,omitempty" json:"side,omitempty"`
	StartLine *int    `yaml:"start_line,omitempty" json:"start_line,omitempty"`
	StartSide *string `yaml:"start_side,omitempty" json:"start_side,omitempty"`
	Severity  string  `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// PullRequestReviewRequest defines the structure for a review request
type PullRequestReviewRequest struct {
	Body     *string               `yaml:"body,omitempty" json:"body,omitempty"`
	Event    *string               `yaml:"event,omitempty" json:"event,omitempty"`
	Comments []*DraftReviewComment `yaml:"comments,omitempty" json:"comments,omitempty"`
}

// ReviewAgentOutput defines the structure for the agent's YAML output.
type ReviewAgentOutput struct {
	Note   string                    `yaml:"note"`
	Review *PullRequestReviewRequest `yaml:"review"`
	Labels []string                  `yaml:"labels,omitempty"`
}

// ToGitHubReviewRequest converts the internal PullRequestReviewRequest to the GitHub API struct
func (r *PullRequestReviewRequest) ToGitHubReviewRequest() *github.PullRequestReviewRequest {
	if r == nil {
		return nil
	}
	var comments []*github.DraftReviewComment
	for _, c := range r.Comments {
		comments = append(comments, &github.DraftReviewComment{
			Path:      c.Path,
			Position:  c.Position,
			Body:      c.Body,
			Line:      c.Line,
			Side:      c.Side,
			StartLine: c.StartLine,
			StartSide: c.StartSide,
		})
	}
	return &github.PullRequestReviewRequest{
		Body:     r.Body,
		Event:    r.Event,
		Comments: comments,
	}
}

// Condition represents a Kubernetes-style status condition
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// ModelUsage captures usage statistics for a single LLM model.
type ModelUsage struct {
	TotalRequests  int64 `json:"totalRequests,omitempty"`
	TotalErrors    int64 `json:"totalErrors,omitempty"`
	TotalLatencyMs int64 `json:"totalLatencyMs,omitempty"`
	InputTokens    int64 `json:"inputTokens,omitempty"`
	OutputTokens   int64 `json:"outputTokens,omitempty"`
	TotalTokens    int64 `json:"totalTokens,omitempty"`
	CachedTokens   int64 `json:"cachedTokens,omitempty"`
	ThoughtTokens  int64 `json:"thoughtTokens,omitempty"`
}

// Stats captures aggregated LLM statistics for a task.
type Stats struct {
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// Task represents a sandbox task
type Task struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	TaskState         string `json:"taskState"` // from status.taskState
	Result            string `json:"result"`    // from status.result
	CreationTimestamp string `json:"creationTimestamp"`
	AgentDraft        string `json:"agentDraft,omitempty"`
	AgentDraftType    string `json:"agentDraftType,omitempty"`
	UserDraft         string `json:"userDraft,omitempty"`
	AgentState        string `json:"agentState,omitempty"`
	AgentStateMessage string `json:"agentStateMessage,omitempty"`
	Stats             *Stats `json:"stats,omitempty"`
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
	SandboxStatus     string   `json:"sandboxStatus,omitempty"`
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
	Conditions          []Condition   `json:"conditions,omitempty"`
}

// ReviewConfig holds configuration for PR reviews
type ReviewConfig struct {
	MaxActiveSandboxes int64    `json:"maxActiveSandboxes"`
	Assignees          []string `json:"assignees,omitempty"`
	Models             []string `json:"models,omitempty"`
}

// IssueConfig holds configuration for issues
type IssueConfig struct {
	MaxActiveSandboxes int64          `json:"maxActiveSandboxes"`
	Handlers           []IssueHandler `json:"handlers,omitempty"`
	Issues             []int64        `json:"issues,omitempty"`
	Models             []string       `json:"models,omitempty"`
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
	Description       string   `json:"description,omitempty"`
	Sandbox           string   `json:"sandbox,omitempty"`
	SandboxReplica    string   `json:"sandboxReplica,omitempty"`
	BranchURL         string   `json:"branchURL,omitempty"`
	Branch            string   `json:"branch,omitempty"`
	AgentState        string   `json:"agentState,omitempty"`
	AgentStateMessage string   `json:"agentStateMessage,omitempty"`
	SandboxStatus     string   `json:"sandboxStatus,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	IdeaID            string   `json:"ideaID,omitempty"`
	Approach          string   `json:"approach,omitempty"`
	ParentApproach    string   `json:"parentApproach,omitempty"`
}
