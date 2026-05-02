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

// ModelUsage captures usage statistics for a single model.
type ModelUsage struct {
	// TotalRequests is the number of API calls made.
	// +optional
	TotalRequests int64 `json:"totalRequests,omitempty"`
	// TotalErrors is the number of API errors.
	// +optional
	TotalErrors int64 `json:"totalErrors,omitempty"`
	// TotalLatencyMs is the total API latency in milliseconds.
	// +optional
	TotalLatencyMs int64 `json:"totalLatencyMs,omitempty"`
	// InputTokens is the number of input tokens consumed.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens is the number of output tokens generated.
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// TotalTokens is the total tokens consumed.
	// +optional
	TotalTokens int64 `json:"totalTokens,omitempty"`
	// CachedTokens is the number of cached tokens.
	// +optional
	CachedTokens int64 `json:"cachedTokens,omitempty"`
	// ThoughtTokens is the number of reasoning/thinking tokens consumed.
	// +optional
	ThoughtTokens int64 `json:"thoughtTokens,omitempty"`
}

// Stats captures aggregated LLM usage statistics for a task.
type Stats struct {
	// Models contains per-model usage statistics.
	// +optional
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// SandboxTaskStatus defines the observed state of SandboxTask
type SandboxTaskStatus struct {
	// TaskState represents the current state of the task (Pending, Running, Completed, Failed)
	// +optional
	// +kubebuilder:default="Pending"
	TaskState string `json:"taskState,omitempty"`

	// StartTime is the time when the task started running
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is the time when the task finished execution
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Result of the task execution
	// +optional
	Result string `json:"result,omitempty"`

	// Stats captures the LLM usage for this task.
	// +optional
	Stats *Stats `json:"stats,omitempty"`
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
