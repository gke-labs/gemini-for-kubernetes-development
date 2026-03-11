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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChoresSpec defines the configuration for Overseer chores.
type ChoresSpec struct {
	// Mode defines the mode for chores.
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	Mode string `json:"mode,omitempty"`
}

// RepoSpec defines the configuration for Overseer repo (issue and PR handling).
type RepoSpec struct {
	// Mode defines the mode for repo handling (issue and PR handling).
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	Mode string `json:"mode,omitempty"`
}

// OverseerSpec defines the desired state of Overseer
type OverseerSpec struct {
	// The full URL of the GitHub repository to watch.
	// e.g., https://github.com/owner/repo
	// +kubebuilder:validation:Required
	RepoURL string `json:"repoURL"`

	// Image to use for the overseer.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// Chores configuration
	// +kubebuilder:validation:Optional
	Chores *ChoresSpec `json:"chores,omitempty"`

	// Repo configuration
	// +kubebuilder:validation:Optional
	Repo *RepoSpec `json:"repo,omitempty"`

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
