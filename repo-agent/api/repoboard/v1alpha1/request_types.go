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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A Request is one click, and what came of it.
//
// It replaces the mailbox: a JSON map in the
// `board.gemini.google.com/requests` annotation that the API appended to
// and the controller emptied. That worked while a click was only ever
// "seconds between the button and the sandbox", and stopped working as
// soon as clicks started carrying parameters (a run's mode and brief, a
// conversation's opening question) and outcomes someone might want to
// read. Four writers doing read-modify-write on one annotation with no
// RetryOnConflict anywhere is a lost click per collision; a value like
// `alice|2026-09-27T12:00:00Z|<base64>` is a schema nobody can validate;
// and an entry that is deleted the moment it is served can never say
// that the thing it asked for failed.
//
// One object per click fixes all three. Creates do not collide, the
// parameters are fields, and the object outlives the launch long enough
// to carry its verdict.
//
// What a Request is NOT: the working state of the thing it started. Once
// a sandbox exists it owns that — the plan draft, the review state, the
// iterate instruction all live on the sandbox, because they outlive the
// click by as long as the work does. A Request is the click and its
// outcome, and then it is garbage.

// Request phases. A Request moves Pending → Launching → Running → and
// then to exactly one of Succeeded or Failed, where it stays until it is
// reaped.
const (
	// RequestPending: filed, not yet acted on. The controller has not
	// reconciled the board since, or a launch limit is holding it back.
	RequestPending = "Pending"
	// RequestLaunching: written immediately BEFORE the launch, so that a
	// controller which dies mid-launch restarts knowing it may already
	// have spent something. For a named sandbox that is merely tidy; for
	// a deploy it is the difference between one cloud footprint and two.
	RequestLaunching = "Launching"
	// RequestRunning: the launch returned and the task is in flight.
	RequestRunning = "Running"
	// RequestSucceeded: what was asked for exists. For most verbs that
	// means the sandbox; for a run it means a result came back clean.
	RequestSucceeded = "Succeeded"
	// RequestFailed: it will not happen. Terminal on purpose — a retry
	// is a new click, and therefore a new Request. Nothing here retries
	// itself, because the failures that matter (a token the org blocked,
	// a project that was never configured) do not fix themselves, and
	// the ones that do are one button away.
	RequestFailed = "Failed"
)

// The verbs. Each is a button in the UI, and each names the factory
// invocation the controller makes on the member's behalf.
const (
	VerbFix         = "fix"
	VerbReview      = "review"
	VerbTriage      = "triage"
	VerbPlan        = "plan"
	VerbIterate     = "iterate"
	VerbAddress     = "address"
	VerbInvestigate = "investigate"
	VerbRun         = "run"
	VerbResearch    = "research"
)

// LabelBoard selects every Request filed against one board. Requests
// live in the board's namespace beside it, so this is the whole list
// query — there is no cross-namespace read.
const LabelBoard = "board.gemini.google.com/board"

// LabelVerb selects one kind of click. The research list is the reader
// that needs it: it wants every conversation a member has asked for
// across all their boards, and without this it would have to fetch every
// click on every board to find them.
const LabelVerb = "board.gemini.google.com/verb"

// RunRequest is the Runs tab's parameters: which run, which phase of it,
// and the brief for this particular invocation.
type RunRequest struct {
	// Mode is what to do: plan | deploy | run | teardown.
	// +kubebuilder:validation:Enum=plan;deploy;run;teardown
	Mode string `json:"mode"`

	// Name is the run's identity, and the name of its directory on the
	// research/runs branch. The sandbox is derived from it, so the same
	// name always reaches the same workspace.
	Name string `json:"name"`

	// Scenario is the shape the run was started from ("deploy-gcp"),
	// when that is something other than the run's own name. It survives
	// only as words in the brief now that runbooks are not shared
	// documents.
	// +kubebuilder:validation:Optional
	Scenario string `json:"scenario,omitempty"`

	// Intent is this invocation's brief, or the amendment on a re-plan.
	//
	// It rides here rather than on the board because a board-level field
	// is shared by every run and outlives all of them, which is how a
	// question typed for one deployment ended up steering the next.
	// +kubebuilder:validation:Optional
	Intent string `json:"intent,omitempty"`

	// Runbook is what a new run is started from: a runbook under the
	// repository's .agents/runbooks/, or another of the member's runs.
	// It is copied into the run and planned for it. Plan only.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Runbook string `json:"runbook,omitempty"`

	// Target is the pull request this run deploys instead of the
	// default branch. The plan pins its head commit; deploy and
	// teardown execute the pin. Plan only.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	Target int `json:"target,omitempty"`
}

// ResearchRequest is one deep-research conversation: its identity, and
// the opening turn it starts with.
//
// The fields after SessionID mirror research.Kickoff, which the API and
// the controller both build from them. They are spelled out rather than
// imported because an API package that reaches back into pkg/ inverts
// the dependency — and because a kickoff is four strings, so the copy
// costs less than the coupling.
type ResearchRequest struct {
	// SessionID is the conversation's identity and the only input to its
	// sandbox's name: the same id always names the same sandbox. Minted
	// by the API, never by the caller — a caller-chosen id would let one
	// member address another's session by guessing it.
	SessionID string `json:"sessionId"`

	// Kind is the canned opening this session starts from: onboard |
	// activity | topic. Empty means an empty conversation the member
	// will type into themselves.
	// +kubebuilder:validation:Optional
	Kind string `json:"kind,omitempty"`

	// Topic is the question, for kind=topic. It is also the session's
	// title, truncated.
	// +kubebuilder:validation:Optional
	Topic string `json:"topic,omitempty"`

	// Since is the window for kind=activity, e.g. "2 weeks".
	// +kubebuilder:validation:Optional
	Since string `json:"since,omitempty"`

	// Title is what the session is called. Empty means derive it.
	// +kubebuilder:validation:Optional
	Title string `json:"title,omitempty"`
}

// RequestSpec is the click: who made it, on what, and with what
// parameters. Immutable in practice — the controller only ever writes
// status.
type RequestSpec struct {
	// Board is the RepoBoard this click was made on. Same namespace; the
	// Request is owned by it and dies with it.
	Board string `json:"board"`

	// Verb is what was clicked.
	// +kubebuilder:validation:Enum=fix;review;triage;plan;iterate;address;investigate;run;research
	Verb string `json:"verb"`

	// Member is the namespace whose identity, token and sandbox quota
	// carry out the work. Never a credential — the token is fetched from
	// this namespace's secret at launch, so that nothing secret is ever
	// stored on a CR someone can read.
	Member string `json:"member"`

	// Number is the issue or pull request the verb acts on, for the
	// verbs that act on one.
	// +kubebuilder:validation:Optional
	Number int `json:"number,omitempty"`

	// Instruction is the member's own words for this invocation (the
	// Iterate box). Carried here because the sandbox it belongs on may
	// not exist yet.
	// +kubebuilder:validation:Optional
	Instruction string `json:"instruction,omitempty"`

	// Run is set for verb=run.
	// +kubebuilder:validation:Optional
	Run *RunRequest `json:"run,omitempty"`

	// Research is set for verb=research.
	// +kubebuilder:validation:Optional
	Research *ResearchRequest `json:"research,omitempty"`
}

// RequestStatus is what came of the click.
type RequestStatus struct {
	// Phase is where the click got to.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=Pending;Launching;Running;Succeeded;Failed
	Phase string `json:"phase,omitempty"`

	// Reason is a short CamelCase cause, for the terminal phases.
	// +kubebuilder:validation:Optional
	Reason string `json:"reason,omitempty"`

	// Message is the human half of Reason: what to read, and what to do.
	// +kubebuilder:validation:Optional
	Message string `json:"message,omitempty"`

	// Sandbox is the sandbox this click produced, once there is one. It
	// is the handoff: everything durable about the work lives there, and
	// this Request is only how to find it.
	// +kubebuilder:validation:Optional
	Sandbox string `json:"sandbox,omitempty"`

	// LaunchedAt is when the controller committed to the launch —
	// stamped before it, not after. A Request found in Launching after a
	// restart is compared against the sandbox's last task time to work
	// out whether the launch it was about to make actually happened.
	// +kubebuilder:validation:Optional
	LaunchedAt *metav1.Time `json:"launchedAt,omitempty"`

	// CompletedAt is when the Request reached a terminal phase, and the
	// clock its retention is measured on.
	// +kubebuilder:validation:Optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=req
//+kubebuilder:printcolumn:name="Verb",type=string,JSONPath=`.spec.verb`
//+kubebuilder:printcolumn:name="Subject",type=string,JSONPath=`.metadata.annotations.board\.gemini\.google\.com/subject`
//+kubebuilder:printcolumn:name="Member",type=string,JSONPath=`.spec.member`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Request is the Schema for the requests API.
type Request struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RequestSpec   `json:"spec,omitempty"`
	Status RequestStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RequestList contains a list of Request.
type RequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Request `json:"items"`
}

// AnnotationSubject records what a Request's verb acts on, as one
// string, purely so `kubectl get requests` reads like the UI. The
// authoritative copy is in the spec fields Subject() reads; this is a
// printer column and nothing consults it.
const AnnotationSubject = "board.gemini.google.com/subject"

// Subject is what the verb acts on, within its board: an issue or PR
// number, a run's mode and name, a session id. Together with the verb it
// is the identity a second click collapses onto — clicking Fix twice on
// issue 12 is one request for a fix of issue 12, not two.
func (s RequestSpec) Subject() string {
	switch s.Verb {
	case VerbRun:
		if s.Run == nil {
			return ""
		}
		return s.Run.Mode + "/" + s.Run.Name
	case VerbResearch:
		if s.Research == nil {
			return ""
		}
		return s.Research.SessionID
	default:
		return strconv.Itoa(s.Number)
	}
}

// Key is the full dedup identity: verb and subject.
func (s RequestSpec) Key() string { return s.Verb + "/" + s.Subject() }

// Active reports whether this Request is still owed something. The two
// terminal phases are the only inactive ones — an empty phase is a
// Request the controller has not looked at yet, which is as owed as it
// gets.
func (r *Request) Active() bool {
	return r.Status.Phase != RequestSucceeded && r.Status.Phase != RequestFailed
}

func init() {
	SchemeBuilder.Register(&Request{}, &RequestList{})
}
