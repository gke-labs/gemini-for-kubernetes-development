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

package controllers

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReviewSandboxSpec defines the desired state of ReviewSandbox
type ReviewSandboxSpec struct {
	Prompt          string  `json:"prompt"`
	Source          Source  `json:"source"`
	DevcontainerDir string  `json:"devcontainerDir"`
	Entrypoint      string  `json:"entrypoint"`
	Gateway         Gateway `json:"gateway"`
	Replicas        *int32  `json:"replicas,omitempty"`
}

// Source defines the source of the code
type Source struct {
	GitURL string `json:"giturl"`
}

// Gateway defines the gateway settings
type Gateway struct {
	Enabled bool `json:"enabled"`
}

// ReviewSandboxSpec is the Schema for the reviewsandboxes API
type ReviewSandbox struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ReviewSandboxSpec `json:"spec,omitempty"`
}

// ReviewSandboxList contains a list of ReviewSandbox
type ReviewSandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReviewSandbox `json:"items"`
}
