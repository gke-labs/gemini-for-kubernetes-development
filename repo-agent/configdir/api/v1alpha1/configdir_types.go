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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigDirSpec defines the desired state of ConfigDir
type ConfigDirSpec struct {
	// The controller will use this selector to find all ConfigMap
	// objects associated with this project config.
	// +optional
	FileContentSelector *metav1.LabelSelector `json:"fileContentSelector,omitempty"`
	Files               []FileItem            `json:"files"`
}

// FileSpec defines a single file in the ConfigDir
type FileItem struct {
	Path   string     `json:"path"`
	Source FileSource `json:"source"`
}

// FileSource defines the source of a file's content
type FileSource struct {
	// +optional
	Inline string `json:"inline,omitempty"`
	// +optional
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`
	// +optional
	SecretRef *corev1.SecretKeySelector `json:"secretRef,omitempty"`
	// +optional
	FileContentKey string `json:"fileContentKey,omitempty"`
	// +optional
	URL *URLSource `json:"url,omitempty"`
}

// URLSource defines a URL source
type URLSource struct {
	Location string `json:"location"`
	// +optional
	SHA256 string `json:"sha256,omitempty"`
	// Optional secret for auth headers (e.g., "Authorization: Bearer <token>")
	// +optional
	SecretRef *corev1.SecretKeySelector `json:"secretRef,omitempty"`
}

// ConfigDirStatus defines the observed state of ConfigDir
type ConfigDirStatus struct {
	// Conditions of the ConfigDir.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ConfigDir is the Schema for the configdirs API
type ConfigDir struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigDirSpec   `json:"spec,omitempty"`
	Status ConfigDirStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ConfigDirList contains a list of ConfigDir
type ConfigDirList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConfigDir `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ConfigDir{}, &ConfigDirList{})
}
