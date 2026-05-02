// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.


package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceRule defines the resource to watch and the criteria to match.
type ResourceRule struct {
	// Group is the API group of the resource
	Group string `json:"group"`
	// Version is the API version of the resource
	Version string `json:"version"`
	// Kind is the Kind of the resource
	Kind string `json:"kind"`
	// Namespaces to filter (optional). If empty, watch all namespaces.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
	// MatchCEL is a CEL expression to match the resource.
	// The resource is available as 'object' and 'self'.
	// +optional
	MatchCEL string `json:"matchCEL,omitempty"`
}

// SyncerSpec defines the desired state of Syncer
type SyncerSpec struct {
	// InstallationName used in the GCS path prefix
	InstallationName string `json:"installationName"`
	// GCSBucketName is the name of the bucket
	GCSBucketName string `json:"gcsBucketName"`
	// Rules defines which resources to watch and sync
	Rules []ResourceRule `json:"rules"`
}

// SyncerStatus defines the observed state of Syncer
type SyncerStatus struct {
	// Conditions of the Syncer.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Syncer is the Schema for the syncers API
type Syncer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SyncerSpec   `json:"spec,omitempty"`
	Status SyncerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SyncerList contains a list of Syncer
type SyncerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Syncer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Syncer{}, &SyncerList{})
}
