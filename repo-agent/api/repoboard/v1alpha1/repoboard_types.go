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

// RepoBoard is a repo-scoped work queue: work is discovered from GitHub and
// factory sandboxes, actions are human clicks, and every GitHub-visible
// artifact is authored by a human identity. The spec is near-static
// configuration — GitHub is the coordination plane, sandboxes are execution
// state (see docs/design/repoboard.md).
//
// Boards are personal: each lives in its owner's namespace (namespace ==
// GitHub login) and is visible only to them. GitHub itself is the shared
// view — assignments, labels, PRs and submitted reviews coordinate the
// team; boards never do.

// TriggersSpec controls how intent enters the system. Claims (assignment,
// self-requested review) are invariants of every action and not configurable.
type TriggersSpec struct {
	// Label is the trigger-label prefix. Applying it on GitHub triggers
	// work, subject to the executor-consent rule (the labeler must be the
	// assignee, or the assignee must hold the auto-fix opt-in). Empty
	// disables GitHub-side label triggering.
	// +kubebuilder:default=agent
	Label string `json:"label,omitempty"`

	// Discreet permits UI-only kickoff via the transient request mailbox,
	// without applying the trigger label.
	// +kubebuilder:default=true
	Discreet *bool `json:"discreet,omitempty"`
}

// AutoFixSpec is the standing auto-fix consent. Boards are personal
// (owner == executor), so the board spec is the single source of consent —
// no member-side key.
type AutoFixSpec struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Require lists the predicates that must all hold before an auto-fix
	// launches for an opted-in member. "assigned" plus "label" is the
	// recommended gate.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:items:Enum=assigned;label
	Require []string `json:"require,omitempty"`
}

// IntakeFilters vetoes items from any intake or automatic processing.
type IntakeFilters struct {
	// +kubebuilder:validation:Optional
	ExcludeLabels []string `json:"excludeLabels,omitempty"`
}

// IntakeSpec configures draft-only automation. Nothing under intake may
// write to GitHub; drafts surface on the board until a human publishes.
type IntakeSpec struct {
	// TriageIssues prepares triage suggestions (labels/priority/duplicates)
	// for inbound issues.
	// +kubebuilder:default=false
	TriageIssues bool `json:"triageIssues,omitempty"`

	// DraftReviews prepares unpublished review drafts for inbound PRs.
	// +kubebuilder:default=false
	DraftReviews bool `json:"draftReviews,omitempty"`

	// AutoReview runs a review as the owner for PRs that request their
	// review, parking a pending review on GitHub (visible only to them).
	// +kubebuilder:default=false
	AutoReview bool `json:"autoReview,omitempty"`

	// +kubebuilder:validation:Optional
	AutoFix AutoFixSpec `json:"autoFix,omitempty"`

	// +kubebuilder:validation:Optional
	Filters IntakeFilters `json:"filters,omitempty"`
}

// ReadySpec defines when a PR row lights up "ready to merge" on the board.
// Display/attention policy only — GitHub branch protection remains the
// enforcement at the merge click.
type ReadySpec struct {
	// +kubebuilder:default=1
	Approvals int `json:"approvals,omitempty"`

	// +kubebuilder:default=true
	RequiredChecks *bool `json:"requiredChecks,omitempty"`
}

// PolicySpec shapes attributed writes.
type PolicySpec struct {
	// DraftPR opens fixes as draft PRs until a human promotes them.
	// Forced true for auto-fix launches regardless of this field.
	// +kubebuilder:default=true
	DraftPR *bool `json:"draftPR,omitempty"`

	// AutoIterate lets the PR follow-up loop push iteration commits to the
	// author's own PR branch without a per-push click.
	// +kubebuilder:default=true
	AutoIterate *bool `json:"autoIterate,omitempty"`

	// Disclose appends an assisted-by note to agent-drafted PR bodies.
	// +kubebuilder:default=false
	Disclose bool `json:"disclose,omitempty"`
}

// LimitsSpec caps concurrent factory work for the board. One knob:
// boards are personal (executor == owner), so a per-user limit would
// always cap the same population as the board limit.
type LimitsSpec struct {
	// +kubebuilder:default=5
	MaxActive int `json:"maxActive,omitempty"`
}

// SandboxSpec is passed through to factory invocations.
type SandboxSpec struct {
	// Image overrides the factory sandbox base image; must be
	// factory-compatible.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// +kubebuilder:default="10Gi"
	DiskSize string `json:"diskSize,omitempty"`

	// IdleMinutes pauses finished sandboxes after this idle period.
	// +kubebuilder:default=60
	IdleMinutes int `json:"idleMinutes,omitempty"`
}

// RepoBoardSpec defines the desired state of a RepoBoard.
type RepoBoardSpec struct {
	// RepoURL is the GitHub repository this board serves. Immutable:
	// delete and re-add the board to change repos.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="repoURL is immutable"
	RepoURL string `json:"repoURL"`

	// +kubebuilder:validation:Optional
	Triggers TriggersSpec `json:"triggers,omitempty"`

	// +kubebuilder:validation:Optional
	Intake IntakeSpec `json:"intake,omitempty"`

	// +kubebuilder:validation:Optional
	Ready ReadySpec `json:"ready,omitempty"`

	// +kubebuilder:validation:Optional
	Policy PolicySpec `json:"policy,omitempty"`

	// +kubebuilder:validation:Optional
	Limits LimitsSpec `json:"limits,omitempty"`

	// +kubebuilder:validation:Optional
	Sandbox SandboxSpec `json:"sandbox,omitempty"`
}

// BoardCounts are cheap badge numbers; the board itself is computed live by
// the API from GitHub and sandboxes, never persisted here.
type BoardCounts struct {
	// +kubebuilder:validation:Optional
	NeedsHuman int `json:"needsHuman,omitempty"`

	// +kubebuilder:validation:Optional
	Active int `json:"active,omitempty"`
}

// RepoBoardStatus defines the observed state of a RepoBoard.
type RepoBoardStatus struct {
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +kubebuilder:validation:Optional
	Counts BoardCounts `json:"counts,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repoURL`
//+kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.counts.active`
//+kubebuilder:printcolumn:name="NeedsHuman",type=integer,JSONPath=`.status.counts.needsHuman`

// RepoBoard is the Schema for the repoboards API.
type RepoBoard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RepoBoardSpec   `json:"spec,omitempty"`
	Status RepoBoardStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RepoBoardList contains a list of RepoBoard.
type RepoBoardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RepoBoard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RepoBoard{}, &RepoBoardList{})
}
