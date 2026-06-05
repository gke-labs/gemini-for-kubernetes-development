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

package v1alpha1

import (
	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChoresSpec defines the configuration for Overseer chores.
type ChoresSpec struct {
	// Mode defines the mode for chores.
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	Mode string `json:"mode,omitempty"`

	// Include specifies a list of chore names to include.
	// If present, only chores in this list will be started.
	// +kubebuilder:validation:Optional
	Include []string `json:"include,omitempty"`

	// Exclude specifies a list of chore names to exclude.
	// +kubebuilder:validation:Optional
	Exclude []string `json:"exclude,omitempty"`
}

// RepoSpec defines the configuration for Overseer repo (issue and PR handling).
type RepoSpec struct {
	// ReviewMode defines the mode for handling PR reviews.
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	ReviewMode string `json:"reviewMode,omitempty"`

	// PRMode defines the mode for handling PRs.
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	PRMode string `json:"prMode,omitempty"`

	// IssueMode defines the mode for handling issues.
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	IssueMode string `json:"issueMode,omitempty"`
}

type ReviewSpec struct {
	// Prompt is the prompt to use for the LLM. This can be a simple string or
	// a Go template that will be populated with information about the pull
	// request or issue.
	Prompt string `json:"prompt,omitempty"`

	// The maximum number of files to review in a PR.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=150
	MaxReviewFiles int `json:"maxReviewFiles"`

	// IgnoreFiles specifies a list of glob patterns for files that should be ignored during review.
	// +kubebuilder:validation:Optional
	IgnoreFiles []string `json:"ignoreFiles,omitempty"`

	// SeverityThreshold sets the minimum severity level for review comments to be posted.
	// Comments below this threshold will be filtered out. Valid values: "LOW", "MEDIUM", "HIGH".
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=LOW;MEDIUM;HIGH;CRITICAL
	SeverityThreshold string `json:"severityThreshold,omitempty"`
}

// OverseerSpec defines the desired state of Overseer
type OverseerSpec struct {
	// The full URL of the GitHub repository to watch.
	// e.g., https://github.com/owner/repo
	// +kubebuilder:validation:Required
	RepoURL string `json:"repoURL"`

	// Chores configuration
	// +kubebuilder:validation:Optional
	Chores *ChoresSpec `json:"chores,omitempty"`

	// Repo configuration
	// +kubebuilder:validation:Optional
	Repo *RepoSpec `json:"repo,omitempty"`

	// ConfigdirRef is a reference to a ConfigDir resource that contains
	// additional configuration for the LLM agent, such as tool schemas and
	// model configurations.
	ConfigdirRef string `json:"configdirRef,omitempty"`

	// Image to use for the development sandbox. If set, this overrides the devcontainer image.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// Issue prompt for issue handling
	// +kubebuilder:validation:Optional
	IssuePrompt string `json:"issuePrompt,omitempty"`

	// WorkspaceDiskSize specifies the disk size for the workspace PVC.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	WorkspaceDiskSize string `json:"workspaceDiskSize,omitempty"`

	// EphemeralStorage specifies the ephemeral storage size for the overseer pod.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	EphemeralStorage string `json:"ephemeralStorage,omitempty"`

	// Extensions is a list of extensions to install in the sandbox
	// before running the agent. Each entry specifies a source
	// (GitHub URL or extension name) and optional ref.
	// +kubebuilder:validation:Optional
	Extensions []reviewv1alpha1.Extension `json:"extensions,omitempty"`

	// Review configuration for PRs
	// +kubebuilder:validation:Optional
	Review ReviewSpec `json:"review"`

	// MaxActiveReviews limits the number of concurrent review sandboxes.
	// +kubebuilder:validation:Optional
	MaxActiveReviews *int32 `json:"maxActiveReviews,omitempty"`

	// MaxActiveIssues limits the number of concurrent issue sandboxes.
	// +kubebuilder:validation:Optional
	MaxActiveIssues *int32 `json:"maxActiveIssues,omitempty"`

	// RobotAccount to use for the overseer.
	// +kubebuilder:validation:Optional
	RobotAccount string `json:"robotAccount,omitempty"`

	// GeminiAPIKeySecretName is the name of the secret containing the Gemini API key.
	// +kubebuilder:validation:Optional
	GeminiAPIKeySecretName string `json:"geminiAPIKeySecretName,omitempty"`

	// PollInterval is the interval at which the overseer polls for updates.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="30m"
	PollInterval string `json:"pollInterval,omitempty"`

	// EnableGeminiOrchestrator enables the non-deterministic Gemini orchestration cycle.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	EnableGeminiOrchestrator bool `json:"enableGeminiOrchestrator,omitempty"`

	// Secrets is a list of secrets to mount in all development and issue sandboxes.
	// +kubebuilder:validation:Optional
	Secrets []SecretMount `json:"secrets,omitempty"`

	// Env is a list of environment variables to inject in all sandboxes.
	// +kubebuilder:validation:Optional
	Env []EnvVar `json:"env,omitempty"`
}

type SecretMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// OverseerStatus defines the observed state of Overseer
type OverseerStatus struct {
	// OverseerStatus defines the status of the overseer.
	// +kubebuilder:validation:Optional
	OverseerStatus string `json:"overseerStatus,omitempty"`

	// Message provides more details about the status.
	// +kubebuilder:validation:Optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Overseer is the Schema for the overseers API
type Overseer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OverseerSpec   `json:"spec,omitempty"`
	Status OverseerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OverseerList contains a list of Overseer
type OverseerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Overseer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Overseer{}, &OverseerList{})
}
