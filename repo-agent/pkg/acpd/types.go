// Package acpd is repo-agent's client for the acpd conversation server
// that runs inside a research sandbox (factory/pkg/acpd).
//
// The wire types below are declared here rather than imported from
// factory. repo-agent and factory meet over HTTP and nothing else — a Go
// import would make the sandbox image and the control plane a single
// unit of deployment, so that the two could no longer be rolled
// independently. The cost is this file: a handful of structs that must
// track the server's JSON. They are small and the tests pin them.
package acpd

import (
	"encoding/json"
	"time"
)

// APIKeyHeader carries the engine credential on session creation.
//
// Deliberately not Authorization: this key does not authenticate the
// caller to acpd, it is the credential acpd hands to the engine it
// spawns. It is a header rather than a body field because the request
// body of a session create would otherwise be a candidate for logging,
// and because acpd writes conversation bodies to an on-disk transcript.
const APIKeyHeader = "X-Engine-Api-Key"

// DefaultPort is acpd's listener, adjacent to envd's 49983.
const DefaultPort = 49984

// Engine names an agent binary acpd knows how to spawn. Only gemini is
// registered server-side today; an unknown engine fails at create.
const EngineGemini = "gemini"

// The approval modes gemini offers. ACP fixes the shape of a mode but not
// its vocabulary, so these are the engine's names and not the protocol's:
// acpd matches whatever is asked for against what the engine advertised
// and refuses the create if it is not there, which makes a renamed mode a
// loud failure rather than a session that quietly starts prompting again.
const (
	// ModeDefault prompts for approval on every tool call.
	ModeDefault = "default"
	// ModeAutoEdit auto-approves edit tools and prompts for the rest.
	ModeAutoEdit = "autoEdit"
	// ModeYolo auto-approves every tool call.
	ModeYolo = "yolo"
	// ModePlan is read-only, and only offered when plan is enabled.
	ModePlan = "plan"
)

// ResearchMode is the mode research sessions run in.
//
// Yolo, because a research session is a conversation with a throwaway
// checkout and nobody is necessarily watching it. The canned openings
// make that concrete: an activity digest runs `git log`, an execute tool,
// so a read-only mode would not spare it the prompt — and the controller
// fires those with no browser attached, where an unanswered prompt blocks
// the turn until acpd's permission timeout and then cancels it. The blast
// radius is one sandbox's disk, which is deleted with the conversation.
const ResearchMode = ModeYolo

// Session is the server's view of one conversation.
type Session struct {
	ID        string    `json:"id"`
	Engine    string    `json:"engine"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"createdAt"`
	// Busy reports whether a turn is in flight. A prompt sent while busy
	// is rejected rather than queued.
	Busy bool `json:"busy"`
	// Offset is the transcript length in bytes at the time of the reply,
	// which is where a follower should resume from to see only what
	// happens next.
	Offset int64 `json:"offset"`
	// Mode is the approval mode in force, and AvailableModes the set it
	// can be switched to. Both are empty for an engine that does not
	// implement modes, which is how a client knows not to offer the
	// switch at all rather than offering one that will fail.
	Mode           string        `json:"mode,omitempty"`
	AvailableModes []SessionMode `json:"availableModes,omitempty"`
}

// SessionMode is one approval mode the engine will accept.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateSessionRequest is the body of POST /sessions. The engine
// credential travels in APIKeyHeader, not here.
type CreateSessionRequest struct {
	// ID is chosen by the caller so that a create can be retried after a
	// timeout without risking two engines for one conversation.
	ID     string `json:"id"`
	Engine string `json:"engine"`
	// AuthMethodID names one of the methods the engine advertised during
	// the ACP handshake. Empty lets acpd pick when the choice is
	// unambiguous.
	AuthMethodID string `json:"authMethodId,omitempty"`
	// CWD is the repository checkout the conversation is about. Empty
	// means the sandbox's workspace root.
	CWD string `json:"cwd,omitempty"`
	// Mode is the approval mode to start in. Empty leaves the engine's
	// own, which for gemini means prompting on every tool call. A mode
	// the engine does not offer fails the create.
	Mode string `json:"mode,omitempty"`
}

// Event is one line of the NDJSON transcript.
//
// Kind is an open set. Five kinds are acpd's own — see the Kind
// constants — and every other value is an ACP sessionUpdate kind
// forwarded verbatim from the engine (agent_message_chunk, tool_call,
// plan, and whatever the engine adds next). Treat unknown kinds as
// renderable rather than as an error; the engine is allowed to grow new
// ones without repo-agent changing.
type Event struct {
	Seq  int64           `json:"seq"`
	Time time.Time       `json:"time"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// acpd's own event kinds.
const (
	KindUserPrompt         = "user_prompt"
	KindPermissionRequest  = "permission_request"
	KindPermissionResolved = "permission_resolved"
	KindTurnEnd            = "turn_end"
	KindError              = "error"
	// KindModeChanged records a mode acpd asked the engine for. Its
	// payload is ACP's current_mode_update ({"currentModeId": …}), which
	// the engine also emits when it changes mode by itself — the two
	// kinds carry the same thing and a reader should treat them alike.
	KindModeChanged = "mode_changed"
)

// PermissionRequest is the payload of a KindPermissionRequest event: the
// engine is blocked until someone answers it.
type PermissionRequest struct {
	RequestID string `json:"requestId"`
	// ToolCall is left raw. It is an ACP sessionUpdate whose shape varies
	// by tool and is the engine's to define; repo-agent forwards it to
	// the UI rather than interpreting it.
	ToolCall json.RawMessage    `json:"toolCall"`
	Options  []PermissionOption `json:"options"`
}

// PermissionOption is one answer the engine will accept.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionResolution answers a PermissionRequest. Exactly one of
// OptionID or Cancelled is meaningful.
type PermissionResolution struct {
	RequestID string `json:"requestId"`
	OptionID  string `json:"optionId,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// UserPromptData is the payload of a KindUserPrompt event.
type UserPromptData struct {
	Text string `json:"text"`
}

// TurnEndData is the payload of a KindTurnEnd event. StopReason is
// ACP's: end_turn, max_tokens, refusal, cancelled.
type TurnEndData struct {
	StopReason string `json:"stopReason"`
}

// ErrorData is the payload of a KindError event. Log, when set, is a
// path inside the sandbox holding the engine's stderr — the detail that
// makes a bare "engine exited" diagnosable.
type ErrorData struct {
	Message string `json:"message"`
	Log     string `json:"log,omitempty"`
}

// PermissionResolvedData is the payload of a KindPermissionResolved
// event. Outcome is ACP's ("selected" or "cancelled"); Reason is set
// only when acpd resolved the request itself, such as on timeout.
type PermissionResolvedData struct {
	RequestID string `json:"requestId"`
	Outcome   string `json:"outcome"`
	OptionID  string `json:"optionId,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
