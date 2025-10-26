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

// ConfigFileSpec defines the desired state of ConfigFile
type ConfigFileSpec struct {
	Files []FileContent `json:"files"`
}

// FileContent defines the content of a file
type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64 encoded
	// +optional
	Continued string `json:"continued,omitempty"`
}

// ConfigFileStatus defines the observed state of ConfigFile
type ConfigFileStatus struct {
	// Conditions of the ConfigFile.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ConfigFile is the Schema for the configfiles API
type ConfigFile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigFileSpec   `json:"spec,omitempty"`
	Status ConfigFileStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ConfigFileList contains a list of ConfigFile
type ConfigFileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConfigFile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ConfigFile{}, &ConfigFileList{})
}
