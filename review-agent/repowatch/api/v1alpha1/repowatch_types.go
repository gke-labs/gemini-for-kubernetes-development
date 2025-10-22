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

// GeminiConfig defines the Gemini configuration
type GeminiConfig struct {
	// Prompt string
	Prompt string `json:"prompt,omitempty"`
	// .gemini folder configdir reference
	ConfigdirRef string `json:"configdirRef,omitempty"`
}

type RepoWatchReviewSpec struct {
	// Gemini configuration for the review sandboxes.
	Gemini GeminiConfig `json:"gemini,omitempty"`

	// DevcontainerConfigRef string
	DevcontainerConfigRef string `json:"devcontainerConfigRef,omitempty"`

	// The maximum number of sandboxes to have active (replicas > 0) at any given time.
	// +kubebuilder:validation:Required
	MaxActiveSandboxes int `json:"maxActiveSandboxes"`
}

// RepoWatchSpec defines the desired state of RepoWatch
type RepoWatchSpec struct {
	// The full URL of the GitHub repository to watch.
	// e.g., https://github.com/owner/repo
	// +kubebuilder:validation:Required
	RepoURL string `json:"repoURL"`

	// Review configuration for PR sandboxes.
	// +kubebuilder:validation:Required
	Review RepoWatchReviewSpec `json:"review"`

	// How often to check for new PRs (in seconds).
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:default=300
	PollIntervalSeconds int `json:"pollIntervalSeconds,omitempty"`

	// Secret containing the GitHub Personal Access Token (PAT) for accessing the repo.
	// +kubebuilder:validation:Required
	GithubSecretRef GithubSecretRef `json:"githubSecretRef"`
}

// GithubSecretRef defines the reference to the secret containing the GitHub PAT
type GithubSecretRef struct {
	// Name of the secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Key in the secret
	// +kubebuilder:validation:Required
	Key string `json:"key"`
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
