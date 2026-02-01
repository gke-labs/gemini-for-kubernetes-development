/*
Copyright 2025.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// GeminiProvider represents the Gemini LLM provider.
	GeminiProvider = "gemini-cli"
	// ClaudeProvider represents the Claude LLM provider.
	ClaudeProvider = "claude"
	// Dummy provider for testing
	DummyProvider = "dummy"
)

// LLMConfig defines the configuration for the LLM provider.
type LLMConfig struct {
	// Provider is the name of the LLM provider to use. This field is used to
	// determine which LLM client to instantiate and how to interact with the
	// LLM API.
	// +kubebuilder:validation:Enum=gemini-cli;claude;dummy
	// +kubebuilder:default=gemini-cli
	Provider string `json:"provider,omitempty"`

	// APIKeySecretRef is a reference to a Kubernetes secret containing the API
	// key for the LLM provider. The secret must have a key named "apiKey".
	// This approach provides a secure way to manage API keys without exposing
	// them in the CRD.
	// +kubebuilder:validation:Required
	APIKeySecretRef string `json:"apiKeySecretRef,omitempty"`

	// Prompt is the prompt to use for the LLM. This can be a simple string or
	// a Go template that will be populated with information about the pull
	// request or issue.
	Prompt string `json:"prompt,omitempty"`

	// ConfigdirRef is a reference to a ConfigDir resource that contains
	// additional configuration for the LLM agent, such as tool schemas and
	// model configurations.
	ConfigdirRef string `json:"configdirRef,omitempty"`
}

type PRReviewSpec struct {
	// LLM configuration for the review sandboxes.
	LLM LLMConfig `json:"llm,omitempty"`

	// DevcontainerConfigRef string
	DevcontainerConfigRef string `json:"devcontainerConfigRef,omitempty"`

	// Image to use for the development sandbox. If set, this overrides the devcontainer image.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// The maximum number of sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`

	// The maximum number of sandboxes to have (active + inactive) at any given time.
	// +kubebuilder:validation:Optional
	MaxSandboxes int `json:"maxSandboxes,omitempty"`

	// The maximum number of files to review in a PR.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=30
	MaxReviewFiles int `json:"maxReviewFiles,omitempty"`

	// The time in minutes after which a review sandbox will be scaled down to 0 replicas.
	// +kubebuilder:validation:Optional
	ReviewShutdownAfterMinutes int `json:"reviewShutdownAfterMinutes,omitempty"`

	// Explicit PullRequests for this handler
	// +kubebuilder:validation:Optional
	PullRequests []int `json:"pullRequests,omitempty"`

	// ExcludePullRequests specifies a list of PR numbers that should not have sandboxes created for them.
	// These PRs will be ignored even if they match other criteria (e.g., labels).
	// +kubebuilder:validation:Optional
	ExcludePullRequests []int `json:"excludePullRequests,omitempty"`

	// Labels to filter issues for this handler
	// labels within the each array are anded, arrays are ored
	// +kubebuilder:validation:Optional
	Labels [][]string `json:"labels"`

	// Assigned to self when selecting PRs for review
	// If true, only PRs assigned to the user associated with the GitHub token will be considered in addition to the Asignees list.
	// +kubebuilder:validation:Optional
	AssignedToSelf bool `json:"assignedToSelf,omitempty"`

	// Assignees to filter PRs for this handler
	// +kubebuilder:validation:Optional
	Assignees []string `json:"assignees,omitempty"`

	// RobotAccount to use for this handler.
	// +kubebuilder:validation:Optional
	RobotAccount string `json:"robotAccount,omitempty"`
}

// DevSpec defines the configuration for development sandboxes.
type DevSpec struct {
	// LLM configuration for the review sandboxes.
	LLM LLMConfig `json:"llm,omitempty"`

	// The maximum number of dev sandboxes to create.
	// +kubebuilder:validation:Required
	MaxSandboxes int `json:"maxSandboxes"`

	// The maximum number of dev sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`

	// DevcontainerConfigRef string
	DevcontainerConfigRef string `json:"devcontainerConfigRef,omitempty"`

	// Image to use for the development sandbox. If set, this overrides the devcontainer image.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// Branches specifies a list of branch names that should have development sandboxes created for them.
	// If this list is empty, the controller will automatically discover branches based on other criteria.
	// +kubebuilder:validation:Optional
	Branches []string `json:"branches,omitempty"`

	// ExcludeBranches specifies a list of branch names that should not have development sandboxes created for them.
	// These branches will be ignored even if they match other criteria or are explicitly listed in 'Branches'.
	// +kubebuilder:validation:Optional
	ExcludeBranches []string `json:"excludeBranches,omitempty"`
}

type IssueHandlerSpec struct {
	// Name of the issue handler
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Labels to filter issues for this handler
	// +kubebuilder:validation:Optional
	Labels []string `json:"labels"`

	// Prompt is the prompt to use for the LLM.
	// +kubebuilder:validation:Optional
	Prompt string `json:"prompt,omitempty"`

	// TaskType is the type of task to create. Defaults to "issue".
	// +kubebuilder:validation:Optional
	TaskType string `json:"taskType,omitempty"`

	// CommentOutput - does this action produce an output that can be a comment on the issue ?
	// +kubebuilder:validation:Optional
	CommentOutput bool `json:"comment,omitempty"`
}

type IssueSpec struct {
	// Issues to filter issues for this handler
	// +kubebuilder:validation:Optional
	Issues []int `json:"issues"`

	// ExcludeIssues specifies a list of Issue numbers that should not have sandboxes created for them.
	// These Issues will be ignored even if they match other criteria (e.g., labels).
	// +kubebuilder:validation:Optional
	ExcludeIssues []int `json:"excludeIssues,omitempty"`

	// LLM configuration for the issue sandboxes.
	LLM LLMConfig `json:"llm,omitempty"`

	// DevcontainerConfigRef string
	DevcontainerConfigRef string `json:"devcontainerConfigRef,omitempty"`

	// Image to use for the development sandbox. If set, this overrides the devcontainer image.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// The maximum number of sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`

	// The maximum number of sandboxes to have (active + inactive) at any given time.
	// +kubebuilder:validation:Optional
	MaxSandboxes int `json:"maxSandboxes,omitempty"`

	// The time in minutes after which a issue sandbox will be scaled down to 0 replicas.
	// +kubebuilder:validation:Optional
	IssueShutdownAfterMinutes int `json:"issueShutdownAfterMinutes,omitempty"`

	// RobotAccount to use for this handler.
	// +kubebuilder:validation:Optional
	RobotAccount string `json:"robotAccount,omitempty"`

	// Handlers configuration for Bugs
	// +kubebuilder:validation:Optional
	Handlers []IssueHandlerSpec `json:"handlers,omitempty"`
}

// RepoWatchSpec defines the desired state of RepoWatch
type RepoWatchSpec struct {
	// The full URL of the GitHub repository to watch.
	// e.g., https://github.com/owner/repo
	// +kubebuilder:validation:Required
	RepoURL string `json:"repoURL"`

	// Review configuration for PRs
	// +kubebuilder:validation:Optional
	Review PRReviewSpec `json:"review,omitempty"`

	// Issue configuration for Bugs
	// +kubebuilder:validation:Optional
	Issue *IssueSpec `json:"issue,omitempty"`

	// Dev configuration for development sandboxes

	// +kubebuilder:validation:Optional
	Dev DevSpec `json:"dev,omitempty"`

	// Secret containing the GitHub Personal Access Token (PAT) for accessing the repo.
	// +kubebuilder:validation:Required
	GithubSecretName string `json:"githubSecretName"`

	// How often to check for new PRs (in seconds).
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:default=300
	PollIntervalSeconds int `json:"pollIntervalSeconds,omitempty"`
}

// RepoWatchStatus defines the observed state of RepoWatch
type RepoWatchStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	ActiveSandboxCount int `json:"activeSandboxCount"`

	// +optional
	ReviewSandboxes []WatchedPR `json:"reviewSandboxes,omitempty"`

	// +optional
	PendingPRs []int `json:"pendingPRs,omitempty"`

	// +optional
	IssueSandboxes map[string][]WatchedIssue `json:"issueSandboxes,omitempty"`

	// +optional
	PendingIssues map[string][]int `json:"pendingIssues,omitempty"`

	// +optional
	DevSandboxes []DevSandbox `json:"devSandboxes,omitempty"`

	// +optional
	PendingDevBranches []string `json:"pendingDevBranches,omitempty"`
}

// WatchedPR defines the state of a watched PR
type WatchedPR struct {
	// PR number
	Number int `json:"number"`
	// Name of the sandbox
	SandboxName string `json:"sandboxName"`
	// Status of the sandbox
	Status string `json:"status"`
	// ScaledDown indicates if the sandbox for this PR has been scaled down to 0 replicas.
	// +kubebuilder:validation:Optional
	ScaledDown bool `json:"scaledDown,omitempty"`
}

// WatchedIssue defines the state of a watched Issue
type WatchedIssue struct {
	// Issue number
	Number int `json:"number"`
	// Name of the sandbox
	SandboxName string `json:"sandboxName"`
	// Status of the sandbox
	Status string `json:"status"`
	// ScaledDown indicates if the sandbox for this Issue has been scaled down to 0 replicas.
	// +kubebuilder:validation:Optional
	ScaledDown bool `json:"scaledDown,omitempty"`
}

// DevSandbox defines the state of a watched development sandbox
type DevSandbox struct {
	// Name of the remote branch
	BranchName string `json:"branchName"`
	// Name of the sandbox
	SandboxName string `json:"sandboxName"`
	// Status of the sandbox
	Status string `json:"status"`
	// ScaledDown indicates if the sandbox for this Dev branch has been scaled down to 0 replicas.
	// +kubebuilder:validation:Optional
	ScaledDown bool `json:"scaledDown,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// RepoWatch is the Schema for the repowatches API
type RepoWatch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RepoWatchSpec   `json:"spec,omitempty"`
	Status RepoWatchStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RepoWatchList contains a list of RepoWatch
type RepoWatchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RepoWatch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RepoWatch{}, &RepoWatchList{})
}
