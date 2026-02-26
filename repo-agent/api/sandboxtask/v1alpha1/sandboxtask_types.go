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

// SandboxTaskSpec defines the desired state of SandboxTask
type SandboxTaskSpec struct {
	// SandboxName is the name of the sandbox to execute the task in
	SandboxName string `json:"sandboxName"`

	// Type of the task (e.g., "exec", "review", etc.)
	Type string `json:"type"`

	// Params are additional parameters for the task
	// +optional
	Params map[string]string `json:"params,omitempty"`

	// Script to execute (if applicable)
	// +optional
	Script string `json:"script,omitempty"`
}

// SandboxTaskStatus defines the observed state of SandboxTask
type SandboxTaskStatus struct {
	// TaskState represents the current state of the task (Pending, Running, Completed, Failed)
	// +optional
	// +kubebuilder:default="Pending"
	TaskState string `json:"taskState,omitempty"`

	// Result of the task execution
	// +optional
	Result string `json:"result,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.taskState",description="The state of the sandbox task"
//+kubebuilder:printcolumn:name="Synced",type="string",JSONPath=".status.conditions[?(@.type==\"InstanceSynced\")].status",description="Whether a sandbox task has all its subresources ready"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age of the resource"

// SandboxTask is the Schema for the sandboxtasks API
type SandboxTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxTaskSpec   `json:"spec,omitempty"`
	Status SandboxTaskStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SandboxTaskList contains a list of SandboxTask
type SandboxTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxTask `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SandboxTask{}, &SandboxTaskList{})
}
