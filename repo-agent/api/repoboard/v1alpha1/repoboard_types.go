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

// ViewSpec is the board's persistent view filter — VIEW ONLY. It narrows
// what the feed surfaces (items with an active sandbox always surface) and
// never affects automation: spec.auto is scoped by spec.auto alone.
// Ad-hoc narrowing on top of this is fluid client-side UI state.
type ViewSpec struct {
	// Labels: when set, the board only surfaces items carrying one of
	// these labels.
	// +kubebuilder:validation:Optional
	Labels []string `json:"labels,omitempty"`
}

// AutoSpec is everything that launches without a click — the deliberate,
// rarely-touched half of the board. What the member LOOKS at (view scopes,
// filters) is fluid per-user UI state and never lives on this CR: changing
// a view must never change what runs. Every verb is an enum whose value
// names its scope, every verb is bounded by the recency window, and all
// writes stay draft-shaped (triage drafts on the board, draft PRs, pending
// reviews visible only to the owner).
type AutoSpec struct {
	// Triage prepares triage drafts for recent issues: "unclaimed" (no
	// assignees) or "all" (assignment does not imply triaged).
	// +kubebuilder:default=off
	// +kubebuilder:validation:Enum=off;unclaimed;all
	Triage string `json:"triage,omitempty"`

	// Fix starts draft-PR fixes for recent issues: "assigned" (assigned
	// to the owner — the only scope where running as the owner is safe).
	// +kubebuilder:default=off
	// +kubebuilder:validation:Enum=off;assigned
	Fix string `json:"fix,omitempty"`

	// Review runs reviews as the owner (parked as pending reviews) for
	// recent PRs: "requested" (asking for the owner's review) or "all".
	// +kubebuilder:default=off
	// +kubebuilder:validation:Enum=off;requested;all
	Review string `json:"review,omitempty"`

	// Labels: when set, automation only touches items carrying one of
	// these labels — a filter within the scopes above, not a trigger.
	// +kubebuilder:validation:Optional
	Labels []string `json:"labels,omitempty"`

	// ExcludeLabels is the absolute veto: automation never touches items
	// carrying one of these.
	// +kubebuilder:validation:Optional
	ExcludeLabels []string `json:"excludeLabels,omitempty"`

	// RecencyDays bounds every verb to items updated within this window,
	// so enabling automation on an old repo processes the live edge, not
	// the archive. The rest stays a per-item manual click.
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=1
	RecencyDays int `json:"recencyDays,omitempty"`
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

	// Engine selects the agent engine for this board's factory tasks.
	// Per-invocation: flipping it affects the next launch, in-flight
	// tasks finish on the engine they started with.
	// +kubebuilder:validation:Enum=gemini;claude
	// +kubebuilder:default=gemini
	Engine string `json:"engine,omitempty"`

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
	View ViewSpec `json:"view,omitempty"`

	// +kubebuilder:validation:Optional
	Auto AutoSpec `json:"auto,omitempty"`

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
