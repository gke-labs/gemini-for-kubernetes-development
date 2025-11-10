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
)

// LLMConfig defines the configuration for the LLM provider.
type LLMConfig struct {
	// Provider is the name of the LLM provider to use. This field is used to
	// determine which LLM client to instantiate and how to interact with the
	// LLM API.
	// +kubebuilder:validation:Enum=gemini-cli
	// +kubebuilder:default=gemini-cli
	Provider string `json:"provider,omitempty"`

	// APIKeySecretRef is a reference to a Kubernetes secret containing the API
	// key for the LLM provider. The secret must have a key named "apiKey".
	// This approach provides a secure way to manage API keys without exposing
	// them in the CRD.
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

	// The maximum number of sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`

	// PullRequests to filter for this handler
	// +kubebuilder:validation:Optional
	PullRequests []int `json:"pullRequests,omitempty"`
}

type IssueHandlerSpec struct {
	// Name of the issue handler
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Labels to filter issues for this handler
	// +kubebuilder:validation:Optional
	Labels []string `json:"labels"`

	// Issues to filter issues for this handler
	// +kubebuilder:validation:Optional
	Issues []int `json:"issues"`

	// LLM configuration for the bug fix sandboxes.
	LLM LLMConfig `json:"llm,omitempty"`

	// DevcontainerConfigRef string
	DevcontainerConfigRef string `json:"devcontainerConfigRef,omitempty"`

	// The maximum number of sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`

	// PushEnabled - allow pushing to user origin
	// +kubebuilder:validation:Optional
	PushEnabled bool `json:"pushEnabled,omitempty"`
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

	// Handlers configuration for Bugs
	// +kubebuilder:validation:Optional
	IssueHandlers []IssueHandlerSpec `json:"issueHandlers,omitempty"`

	// Secret containing the GitHub Personal Access Token (PAT) for accessing the repo.
	// +kubebuilder:validation:Required
	GithubSecretName string `json:"githubSecretName,"`

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
	WatchedPRs []WatchedPR `json:"watchedPRs,omitempty"`

	// +optional
	PendingPRs []PendingPR `json:"pendingPRs,omitempty"`

	// +optional
	WatchedIssues map[string][]WatchedIssue `json:"watchedIssues,omitempty"`

	// +optional
	PendingIssues map[string][]PendingIssue `json:"pendingIssues,omitempty"`
}

// WatchedPR defines the state of a watched PR
type WatchedPR struct {
	// PR number
	Number int `json:"number"`
	// Name of the sandbox
	SandboxName string `json:"sandboxName"`
	// Status of the sandbox
	Status string `json:"status"`
}

// PendingPR defines the state of a pending PR
type PendingPR struct {
	// PR number
	Number int `json:"number"`
	// Status of the PR
	Status string `json:"status"`
}

// WatchedIssue defines the state of a watched Issue
type WatchedIssue struct {
	// Issue number
	Number int `json:"number"`
	// Name of the sandbox
	SandboxName string `json:"sandboxName"`
	// Status of the sandbox
	Status string `json:"status"`
}

// PendingIssue defines the state of a pending PR
type PendingIssue struct {
	// PR number
	Number int `json:"number"`
	// Status of the PR
	Status string `json:"status"`
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
