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
// a view must never change what runs. Each rule names a recipe and the
// items it runs on, every rule is bounded by the recency window and the
// shared label filters, and runs as the board's owner.
type AutoSpec struct {
	// Rules are the recipes automation runs, each on the open items its
	// conditions match: one recipe Request per item, as the board's owner.
	// +kubebuilder:validation:Optional
	Rules []AutoRule `json:"rules,omitempty"`

	// Triage is deprecated: the controller converts it into a rule
	// (LegacyRules) and sets it off.
	// +kubebuilder:default=off
	// +kubebuilder:validation:Enum=off;unclaimed;all
	Triage string `json:"triage,omitempty"`

	// Fix is deprecated: converted into a rule, as Triage.
	// +kubebuilder:default=off
	// +kubebuilder:validation:Enum=off;assigned
	Fix string `json:"fix,omitempty"`

	// Review is deprecated: converted into a rule, as Triage.
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

// AutoRule runs a recipe on the open items of one type its conditions
// match. The recipe must start on that type: a my-pr recipe cannot be
// automated on others' PRs.
type AutoRule struct {
	// Recipe is a recipe of the board's catalog.
	Recipe string `json:"recipe"`
	// On is the item type: issue or pr.
	// +kubebuilder:validation:Enum=issue;pr
	On string `json:"on"`
	// When are the conditions an item must meet, all of the ones set.
	// None set is every open item of the type.
	// +kubebuilder:validation:Optional
	When AutoWhen `json:"when,omitempty"`
	// MaxPerDay caps how many Requests the rule's recipe is filed in a
	// day by automation. Unset or 0, no cap.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	MaxPerDay int `json:"maxPerDay,omitempty"`
}

// AutoWhen is an AutoRule's conditions: a closed set.
type AutoWhen struct {
	// Unclaimed is an item with no assignees.
	// +kubebuilder:validation:Optional
	Unclaimed bool `json:"unclaimed,omitempty"`
	// AssignedToOwner is an item assigned to the board's owner.
	// +kubebuilder:validation:Optional
	AssignedToOwner bool `json:"assignedToOwner,omitempty"`
	// ReviewRequestedFromOwner is a PR requesting the owner's review.
	// +kubebuilder:validation:Optional
	ReviewRequestedFromOwner bool `json:"reviewRequestedFromOwner,omitempty"`
	// Labels is an item carrying any of these labels.
	// +kubebuilder:validation:Optional
	Labels []string `json:"labels,omitempty"`
}

// LegacyRules are the rules the deprecated Triage, Fix and Review settings
// mean, in that order.
func (a AutoSpec) LegacyRules() []AutoRule {
	var rules []AutoRule
	switch a.Triage {
	case "unclaimed":
		rules = append(rules, AutoRule{Recipe: "triage", On: "issue", When: AutoWhen{Unclaimed: true}})
	case "all":
		rules = append(rules, AutoRule{Recipe: "triage", On: "issue"})
	}
	if a.Fix == "assigned" {
		rules = append(rules, AutoRule{Recipe: "fix", On: "issue", When: AutoWhen{AssignedToOwner: true}})
	}
	switch a.Review {
	case "requested":
		rules = append(rules, AutoRule{Recipe: "review", On: "pr", When: AutoWhen{ReviewRequestedFromOwner: true}})
	case "all":
		rules = append(rules, AutoRule{Recipe: "review", On: "pr"})
	}
	return rules
}

// EffectiveRules are the rules automation runs: Rules, or else what the
// deprecated settings mean, until the controller has converted them.
func (a AutoSpec) EffectiveRules() []AutoRule {
	if len(a.Rules) > 0 {
		return a.Rules
	}
	return a.LegacyRules()
}

// ConvertLegacy moves the deprecated settings into Rules and sets them
// off, reporting whether it changed anything. Rules already set win: the
// deprecated settings are only set off.
func (a *AutoSpec) ConvertLegacy() bool {
	legacy := a.LegacyRules()
	if len(legacy) == 0 {
		return false
	}
	if len(a.Rules) == 0 {
		a.Rules = legacy
	}
	a.Triage, a.Fix, a.Review = "off", "off", "off"
	return true
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

// PolicySpec shapes attributed writes. A board's fixes always open as
// draft PRs (fix's draft input, default true), until a human promotes
// them.
type PolicySpec struct {
	// AutoIterate lets the PR follow-up loop push iteration commits to the
	// author's own PR branch without a per-push click.
	// +kubebuilder:default=true
	AutoIterate *bool `json:"autoIterate,omitempty"`

	// Disclose lets the agent say that an agent wrote what it posts: the
	// "generated by" line in PR descriptions, and the footer on its
	// comments and CI reports. Off, it posts none of them (factory
	// --disclose=false); factory's own default, for its other callers,
	// is on.
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
	// +kubebuilder:validation:Enum=gemini;claude;antigravity
	// +kubebuilder:default=gemini
	Engine string `json:"engine,omitempty"`

	// +kubebuilder:default="10Gi"
	DiskSize string `json:"diskSize,omitempty"`

	// StorageClass is the workspace PVC's StorageClass (factory
	// --workspace-storage-class); empty uses the cluster default.
	// +kubebuilder:validation:Optional
	StorageClass string `json:"storageClass,omitempty"`

	// EphemeralStorage is the sandbox pod's ephemeral storage, request
	// and limit (factory --ephemeral-storage): /tmp and anything written
	// outside /workspaces. Empty is factory's default (6Gi), which a KCC
	// build overflows.
	// +kubebuilder:validation:Optional
	EphemeralStorage string `json:"ephemeralStorage,omitempty"`

	// CPURequest, CPULimit, MemoryRequest and MemoryLimit size the
	// sandbox pod (factory --cpu-request …); empty is factory's default
	// (cpu 500m/4, memory 2Gi/6Gi).
	// +kubebuilder:validation:Optional
	CPURequest string `json:"cpuRequest,omitempty"`
	// +kubebuilder:validation:Optional
	CPULimit string `json:"cpuLimit,omitempty"`
	// +kubebuilder:validation:Optional
	MemoryRequest string `json:"memoryRequest,omitempty"`
	// +kubebuilder:validation:Optional
	MemoryLimit string `json:"memoryLimit,omitempty"`

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

	// Recipes are the recipes the controller's factory runs (factory
	// recipe list), in the board's order: what its rows offer to launch.
	// A new built-in shows up with the image that ships it.
	// +kubebuilder:validation:Optional
	Recipes []BoardRecipe `json:"recipes,omitempty"`

	// AutoRuleErrors are the rules automation skips, and why: a recipe
	// the catalog does not have, or an item type it does not start on.
	// +kubebuilder:validation:Optional
	AutoRuleErrors []string `json:"autoRuleErrors,omitempty"`

	// AutoFiled are the recipe Requests automation filed in the last day,
	// which rules' MaxPerDay count.
	// +kubebuilder:validation:Optional
	AutoFiled []AutoFiling `json:"autoFiled,omitempty"`
}

// AutoFiling is one recipe Request automation filed.
type AutoFiling struct {
	Recipe string      `json:"recipe"`
	At     metav1.Time `json:"at"`
}

// BoardRecipe is one recipe as factory recipe list -o json describes it.
type BoardRecipe struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// On is what it runs on: issue, pr, my-pr, repo. Empty, anything.
	// +kubebuilder:validation:Optional
	On []string `json:"on,omitempty"`
	// +kubebuilder:validation:Optional
	Inputs []BoardRecipeInput `json:"inputs,omitempty"`
	// Kind is its task output's kind, if it declares one.
	// +kubebuilder:validation:Optional
	Kind string `json:"kind,omitempty"`
	// TaskType is what its runs are recorded under on a sandbox.
	// +kubebuilder:validation:Optional
	TaskType string `json:"taskType,omitempty"`
	// Credentials is clone for a recipe whose agent never holds the
	// token; on a PR it runs in a sandbox of its own.
	// +kubebuilder:validation:Optional
	Credentials string `json:"credentials,omitempty"`
	// Session is its session tag: recipes with one share a conversation
	// on a target (and TaskType), and a row shows them as one group.
	// +kubebuilder:validation:Optional
	Session string `json:"session,omitempty"`
	// ApplicableWhen is which items it applies to: a row offers it only
	// on those. Unset, every item On names.
	// +kubebuilder:validation:Optional
	ApplicableWhen *BoardRecipeApplicableWhen `json:"applicableWhen,omitempty"`
	// OnFinish is the action of its task output the board's runner
	// applies when a run ends (fix's open-pr, review's post-review, care's
	// post-replies). Empty, the output waits for a click.
	// +kubebuilder:validation:Optional
	OnFinish string `json:"onFinish,omitempty"`
	// Timeout bounds a run, as a Go duration (3h). Empty, the runner's
	// default.
	// +kubebuilder:validation:Optional
	Timeout string `json:"timeout,omitempty"`
	// Source is where it comes from: builtin, or the path of the board's
	// repository's recipe file (.agents/recipes/<name>.yaml), which runs
	// as factory recipe run --recipe <name>.
	// +kubebuilder:validation:Optional
	Source string `json:"source,omitempty"`
}

// IsBuiltin reports whether the recipe ships with factory rather than
// with the board's repository.
func (r BoardRecipe) IsBuiltin() bool { return r.Source == "" || r.Source == "builtin" }

// BoardRecipeApplicableWhen is which items a recipe applies to.
type BoardRecipeApplicableWhen struct {
	// Labels are the labels the item must have one of, as the controller's
	// factory spells them for its trigger label.
	Labels []string `json:"labels"`
}

// BoardRecipeInput is one input a recipe declares.
type BoardRecipeInput struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:Optional
	Default string `json:"default,omitempty"`
	// +kubebuilder:validation:Optional
	Required bool `json:"required,omitempty"`
	// +kubebuilder:validation:Optional
	Type string `json:"type,omitempty"`
	// From is the task output kind whose result the input takes:
	// a run: <recipe> action on that kind's draft fills it.
	// +kubebuilder:validation:Optional
	From string `json:"from,omitempty"`
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
