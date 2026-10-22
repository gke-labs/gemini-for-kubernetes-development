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
	// Mode is whether scheduled chores run: enabled, disabled, or dryrun
	// (log which chores are due, queue none).
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	Mode string `json:"mode,omitempty"`
}

// RepoSpec defines the configuration for Overseer repo (issue and PR handling).
type RepoSpec struct {
	// PRMode is whether PRs are worked on: enabled, disabled, or dryrun
	// (evaluate and log what would be queued or written, change nothing).
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	PRMode string `json:"prMode,omitempty"`

	// IssueMode is whether issues are worked on, fan-out included: enabled,
	// disabled, or dryrun (evaluate and log, change nothing).
	// +kubebuilder:validation:Enum=enabled;disabled;dryrun
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Optional
	IssueMode string `json:"issueMode,omitempty"`
}

// OverseerRules is the operator's say over the watch rules, which the
// rules engine decides.
type OverseerRules struct {
	// Disabled are rules the watcher does not act on at all, and whose
	// queued tasks it drops before they start: the operator's emergency
	// switch. The rules engine decides every other rule.
	// +kubebuilder:validation:Optional
	Disabled []string `json:"disabled,omitempty"`
}

// OverseerRepoRecipe is how the repository's own recipes run, and at
// which of its commits they are read.
type OverseerRepoRecipe struct {
	// Mode is off (factory's recipes only), augment (the repository's
	// recipes run too, under names of their own; a file named as one of
	// factory's is ignored) or replace (the repository's recipe of a name
	// runs before factory's, for any name, factory's included).
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=off;augment;replace
	// +kubebuilder:default=off
	Mode string `json:"mode,omitempty"`
	// GitCommit is the commit .agents/recipes is read at: a commit SHA, or
	// latest (the default) for the default branch's head.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^(latest|[0-9a-f]{7,40})$`
	GitCommit string `json:"gitCommit,omitempty"`
}

// OverseerSpec defines the desired state of Overseer
type OverseerSpec struct {
	// The full URL of the GitHub repository to watch.
	// e.g., https://github.com/owner/repo
	// +kubebuilder:validation:Required
	RepoURL string `json:"repoURL"`

	// Chores configuration
	// +kubebuilder:validation:Optional
	Chores *ChoresSpec `json:"chores,omitempty"`

	// Repo configuration
	// +kubebuilder:validation:Optional
	Repo *RepoSpec `json:"repo,omitempty"`

	// Image to use for the development sandbox. If set, this overrides the devcontainer image.
	// +kubebuilder:validation:Optional
	Image string `json:"image,omitempty"`

	// WorkspaceDiskSize specifies the disk size for the workspace PVC.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	WorkspaceDiskSize string `json:"workspaceDiskSize,omitempty"`

	// WorkspaceStorageClassName specifies the StorageClass for the workspace PVC.
	// +kubebuilder:validation:Optional
	WorkspaceStorageClassName string `json:"workspaceStorageClassName,omitempty"`

	// EphemeralStorage specifies the ephemeral storage size for the overseer pod.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	EphemeralStorage string `json:"ephemeralStorage,omitempty"`

	// SandboxCPURequest specifies the CPU request for child sandboxes.
	// +kubebuilder:validation:Optional
	SandboxCPURequest string `json:"sandboxCPURequest,omitempty"`

	// SandboxCPULimit specifies the CPU limit for child sandboxes.
	// +kubebuilder:validation:Optional
	SandboxCPULimit string `json:"sandboxCPULimit,omitempty"`

	// SandboxMemoryRequest specifies the memory request for child sandboxes.
	// +kubebuilder:validation:Optional
	SandboxMemoryRequest string `json:"sandboxMemoryRequest,omitempty"`

	// SandboxMemoryLimit specifies the memory limit for child sandboxes.
	// +kubebuilder:validation:Optional
	SandboxMemoryLimit string `json:"sandboxMemoryLimit,omitempty"`

	// SandboxEvictionAge defines the age threshold for idle sandbox eviction (e.g. "7d", "24h").
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="7d"
	SandboxEvictionAge string `json:"sandboxEvictionAge,omitempty"`

	// SandboxIdleTimeout defines the idle timeout after which a sandbox that has not run any task is suspended (e.g. "1h", "30m").
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="1h"
	SandboxIdleTimeout string `json:"sandboxIdleTimeout,omitempty"`

	// PRInactivityTimeout defines the time of inactivity with no human comments before pausing automated processing on a PR (defaults to 0, which disables staleness checks).
	// +kubebuilder:validation:Optional
	PRInactivityTimeout *metav1.Duration `json:"prInactivityTimeout,omitempty"`

	// TaskTimeout bounds the execution time of a single queued task (e.g. "3h", "24h").
	// When a task exceeds this budget it is marked failed and its sandbox is deleted,
	// discarding any work in progress, so this must be generous enough for the longest
	// legitimate task in the repo (e.g. end-to-end suites that record against real GCP).
	// Defaults to 24h when unset.
	// +kubebuilder:validation:Optional
	TaskTimeout *metav1.Duration `json:"taskTimeout,omitempty"`

	// Rules is the operator's say over the watch rules
	// (factory/docs/watch-rules.md), which decide what the watcher does on
	// each issue and PR.
	// +kubebuilder:validation:Optional
	Rules *OverseerRules `json:"rules,omitempty"`

	// RulesRecord makes the watcher record each decision with the facts it
	// was made on, on the overseer's volume
	// (/workspaces/.cache/factory-watch/decisions.jsonl), for `factory watch
	// rules replay` (a rule change run against what the watch saw) and
	// `explain --record`. What it decides is unchanged.
	// +kubebuilder:validation:Optional
	RulesRecord bool `json:"rulesRecord,omitempty"`

	// SharedScan makes the watcher's issue and PR scanners read one shared
	// set of GitHub listings, refreshed with what changed since the last
	// pass and kept in its queue directory across restarts, instead of each
	// listing for itself. What they decide is unchanged; it costs fewer
	// GitHub calls.
	// +kubebuilder:validation:Optional
	SharedScan bool `json:"sharedScan,omitempty"`

	// RepoRecipe is whether the repository's own recipes (.agents/recipes)
	// run beside, or in place of, factory's. Unset, factory's only.
	// +kubebuilder:validation:Optional
	RepoRecipe *OverseerRepoRecipe `json:"repoRecipe,omitempty"`

	// GraphQLPRDetails makes the watcher's PR scanner read what it decides
	// on about each pull request - the pull request, its merge queue entry,
	// commits, comments, reviews and their inline comments - in one GraphQL
	// query per batch of pull requests rather than six REST calls each.
	// What it decides is unchanged; CI is still read over REST.
	// +kubebuilder:validation:Optional
	GraphQLPRDetails bool `json:"graphqlPRDetails,omitempty"`

	// MinNumber specifies the minimum PR/issue number to process.
	// +kubebuilder:validation:Optional
	MinNumber *int32 `json:"minNumber,omitempty"`

	// RobotAccount to use for the overseer.
	// +kubebuilder:validation:Optional
	RobotAccount string `json:"robotAccount,omitempty"`

	// GeminiAPIKeySecretName is the name of the secret containing the Gemini API key.
	// +kubebuilder:validation:Optional
	GeminiAPIKeySecretName string `json:"geminiAPIKeySecretName,omitempty"`

	// PollInterval is how long each run of factory watch lasts before the
	// overseer commits the queue state to git and starts it again (run.sh's
	// --watch-timeout). It is not a polling interval: within a run, each
	// loop polls GitHub on its own fixed cadence (2m for new work, 10m for
	// full sweeps). In-memory state (which PRs changed, backoffs) resets on
	// every restart.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="30m"
	PollInterval string `json:"pollInterval,omitempty"`

	// EnableGeminiOrchestrator enables the non-deterministic Gemini orchestration cycle.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	EnableGeminiOrchestrator bool `json:"enableGeminiOrchestrator,omitempty"`

	// Secrets is a list of secrets to mount in all development and issue sandboxes.
	// +kubebuilder:validation:Optional
	Secrets []SecretMount `json:"secrets,omitempty"`

	// Env is a list of environment variables to inject in all sandboxes.
	// +kubebuilder:validation:Optional
	Env []EnvVar `json:"env,omitempty"`

	// Roles defines the user account pools per role.
	// +kubebuilder:validation:Optional
	Roles map[string]RoleSpec `json:"roles,omitempty"`

	// AllowlistedUsers is a list of GitHub logins whose comments and reviews
	// are trusted even though GitHub does not report them as OWNER, MEMBER or
	// COLLABORATOR of the repository - typically organisation members whose
	// membership is private. Feedback from untrusted accounts is neither
	// acted on by the watcher nor placed in an agent prompt.
	// +kubebuilder:validation:Optional
	AllowlistedUsers []string `json:"allowlistedUsers,omitempty"`

	// SessionURL links the overseer's PR descriptions and follow-up comments
	// to the agent conversation that wrote them: a URL with {sandbox} and
	// {task} in it, e.g. a dashboard's session page. Unset, nothing is
	// linked.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https://`
	SessionURL string `json:"sessionURL,omitempty"`

	// WarmWorkspace keeps a snapshot of a workspace disk with the repository
	// checked out and its caches filled; new sandboxes for the repository
	// start from it (factory/design/warm-workspace.md). Unset, no warming.
	// +kubebuilder:validation:Optional
	WarmWorkspace *WarmWorkspaceSpec `json:"warmWorkspace,omitempty"`
}

// WarmWorkspaceSpec is how often to warm a workspace disk, how many
// snapshots to keep, and the script that warms it: exactly one of script,
// scriptURL and scriptPath.
// +kubebuilder:validation:XValidation:rule="[has(self.script), has(self.scriptURL), has(self.scriptPath)].filter(x, x).size() == 1",message="exactly one of script, scriptURL and scriptPath must be set"
type WarmWorkspaceSpec struct {
	// Interval is how often to warm (e.g. "24h").
	// +kubebuilder:validation:Required
	Interval metav1.Duration `json:"interval"`

	// Keep is how many ready snapshots to keep, the newest included.
	// Defaults to 2.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	Keep *int32 `json:"keep,omitempty"`

	// Script is the warm script itself, run in the checkout of the default
	// branch.
	// +kubebuilder:validation:Optional
	Script string `json:"script,omitempty"`

	// ScriptURL is where to download the warm script from, at each warm.
	// Whoever controls the URL controls what every restored sandbox starts
	// from: prefer a URL pinned to a commit.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https://`
	// +kubebuilder:validation:MaxLength=2048
	ScriptURL string `json:"scriptURL,omitempty"`

	// ScriptPath is the warm script's path in the repository, run from its
	// default branch. Relative, without "..".
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/') && !self.split('/').exists(p, p == '..')",message="scriptPath must be relative, without .."
	ScriptPath string `json:"scriptPath,omitempty"`
}

type RoleSpec struct {
	// Users is the list of user accounts belonging to this role pool.
	// +kubebuilder:validation:Optional
	Users []string `json:"users,omitempty"`
}

type SecretMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// OverseerStatusType defines the status of the overseer.
// +kubebuilder:validation:Enum=Active;Error
type OverseerStatusType string

const (
	// OverseerStatusActive indicates the overseer is active and healthy.
	OverseerStatusActive OverseerStatusType = "Active"
	// OverseerStatusError indicates the overseer encountered an error.
	OverseerStatusError OverseerStatusType = "Error"
)

// OverseerStatus defines the observed state of Overseer
type OverseerStatus struct {
	// ObservedGeneration is the most recent generation observed for this resource.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// OverseerStatus defines the status of the overseer.
	// +kubebuilder:validation:Optional
	OverseerStatus OverseerStatusType `json:"overseerStatus,omitempty"`

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
