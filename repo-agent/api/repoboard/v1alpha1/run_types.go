package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run is one execution of a recipe against a target — the single object
// that answers "what is happening" (docs/design/platform-v2.md).
//
// It replaces the v1 arrangement where that answer had to be
// reconstructed from three unreliable places: a claim key in a board
// annotation, an in-memory result in the controller, and the task files
// on a sandbox's disk. Most of the duplicate-run and stale-state bugs
// in v1 were disagreements between those three.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=run
// +kubebuilder:printcolumn:name="Recipe",type=string,JSONPath=`.spec.recipe`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.verdict`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Run struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunSpec   `json:"spec,omitempty"`
	Status RunStatus `json:"status,omitempty"`
}

type RunSpec struct {
	// Repo is the RepoBoard this run belongs to, in the same namespace.
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`

	// Recipe names the workflow to execute (understand, deploy, fix…).
	// +kubebuilder:validation:Required
	Recipe string `json:"recipe"`

	// Target is what the recipe acts on: "repo", "issue:42",
	// "pr:1324", "environment:sub-1". A recipe declares which kinds it
	// accepts; the API rejects the rest before a Run is created.
	// +kubebuilder:validation:Required
	Target string `json:"target"`

	// Inputs are the recipe's declared inputs, already resolved — the
	// plan gate exists so a human reads real values, not templates.
	// +kubebuilder:validation:Optional
	Inputs map[string]string `json:"inputs,omitempty"`

	// Requester is the member whose credentials the run uses. Runs are
	// never executed with anyone else's identity.
	// +kubebuilder:validation:Optional
	Requester string `json:"requester,omitempty"`
}

type RunStatus struct {
	// Phase: Pending → Running → Succeeded | Failed. A phase is the
	// executor's own record, not something reconstructed from a pod.
	// +kubebuilder:validation:Optional
	Phase string `json:"phase,omitempty"`

	// Verdict is the recipe's own word for how it ended (VERIFIED,
	// PLANNED, BLOCKED…). Phase says whether the machinery worked;
	// verdict says what the work concluded, and they differ: a run can
	// succeed mechanically and return BLOCKED.
	// +kubebuilder:validation:Optional
	Verdict string `json:"verdict,omitempty"`

	// +kubebuilder:validation:Optional
	Message string `json:"message,omitempty"`

	// Sandbox is where the run executes, for logs and the agent card.
	// +kubebuilder:validation:Optional
	Sandbox string `json:"sandbox,omitempty"`

	// Key is the launcher key, so a restarted controller can ask the
	// runner about work it did not start.
	// +kubebuilder:validation:Optional
	Key string `json:"key,omitempty"`

	// +kubebuilder:validation:Optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// +kubebuilder:validation:Optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// +kubebuilder:object:root=true
type RunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Run `json:"items"`
}

const (
	RunPhasePending   = "Pending"
	RunPhaseRunning   = "Running"
	RunPhaseSucceeded = "Succeeded"
	RunPhaseFailed    = "Failed"
)

func init() {
	SchemeBuilder.Register(&Run{}, &RunList{})
}
