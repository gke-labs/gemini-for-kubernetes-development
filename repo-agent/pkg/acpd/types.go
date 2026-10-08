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

// Engine names an agent binary acpd knows how to spawn. gemini and
// antigravity (Google's agy_acp_server) are registered server-side; an
// unknown engine fails at create. Both read the same GEMINI_API_KEY.
const (
	EngineGemini      = "gemini"
	EngineAntigravity = "antigravity"
)

// ResearchEngineAnnotation is where `factory recipe research` records the
// engine it set the sandbox up for, alongside its ready receipt.
// Mirrored from factory, like the other research sandbox names.
const ResearchEngineAnnotation = "sandbox.gemini.google.com/research-engine"

// ResearchEngineFor is the engine a research sandbox is launched with
// for a board on engine. acpd has no claude engine, so a claude board's
// research runs on gemini, as all research did before the choice
// existed.
func ResearchEngineFor(engine string) string {
	if engine == EngineAntigravity {
		return EngineAntigravity
	}
	return EngineGemini
}

// ResearchEngine is the engine to create a sandbox's conversation with.
// Read from the sandbox, not the board: it is what the launch installed,
// and the board may have been switched since. A sandbox older than the
// annotation was set up for gemini.
func ResearchEngine(annotations map[string]string) string {
	if engine := annotations[ResearchEngineAnnotation]; engine != "" {
		return engine
	}
	return EngineGemini
}

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

// ResearchAutoApprove has acpd answer permission requests for research
// sessions rather than wait for a user.
//
// ResearchMode ought to cover this and does not: gemini's shell tool
// screens a command for tokens it lifted from earlier tool output — a
// prompt-injection guard — before it consults the approval mode, so a
// yolo session still stops on `git show <sha-it-just-read>` or `cat
// <path-it-just-listed>`. That is the ordinary shape of research. No
// setting disables the check and no answer to it is remembered, so the
// only place left to decide is here, where we already decided that a
// throwaway checkout is not worth a prompt.
//
// True for browser-started sessions too, and for the same reason the
// mode is: which of the two paths created the session is an accident of
// timing, and it must not be what determines whether the agent stalls.
const ResearchAutoApprove = true

// AutoApproveForMode says whether acpd should answer permission requests
// itself for a session running in mode.
//
// One control, one meaning. What the member asks at the approvals picker
// is "will this ask me before it acts?", and the answer has two layers —
// the engine's mode, and acpd answering underneath it. Deriving the
// second from the first is what keeps the control honest: a picker that
// moved only the engine's half left acpd auto-answering under a mode the
// member had just tightened, which is worse than having no picker.
//
// gemini's vocabulary, like ResearchMode, and matched the same way: an
// engine that renames its auto-approving mode gets prompts, which is the
// safe direction to be wrong in.
func AutoApproveForMode(mode string) bool { return mode == ModeYolo }

// Session is the server's view of one conversation.
type Session struct {
	ID        string    `json:"id"`
	Engine    string    `json:"engine"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"createdAt"`
	// Busy reports whether a turn is in flight. A prompt sent while busy
	// is rejected rather than queued.
	Busy bool `json:"busy"`
	// Waiting narrows Busy: the turn is in flight but stopped on a
	// permission request nobody has answered. Both bits are set, because
	// waiting is a kind of busy, not the end of it.
	//
	// Absent from an older acpd, which reads as false — the honest answer
	// there, since a daemon that does not publish it has told us nothing
	// about whether anyone is being asked for something.
	Waiting bool `json:"waiting,omitempty"`
	// Retrying narrows Busy too: the turn is in flight but its model
	// calls are failing (rate limit, quota, an overloaded model) and the
	// engine is retrying them. It is the last retry the engine logged,
	// absent once the engine is heard from again — and from an acpd that
	// does not watch for them.
	Retrying *EngineRetry `json:"retrying,omitempty"`
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
	// ModeError is why Mode is not the mode the session was created
	// with. The session runs anyway — it just asks before it acts — so
	// this is the only account anybody gets of why it keeps stopping.
	ModeError string `json:"modeError,omitempty"`
	// AutoApprove reports that acpd answers this session's permission
	// requests itself, so the ones in the transcript arrive already
	// resolved and there is nothing for a reader to click.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// Loaded says the session continues a conversation an earlier engine
	// had, so the agent remembers the transcript; false, it starts from
	// nothing whatever the transcript holds.
	Loaded bool `json:"loaded,omitempty"`
	// Task is the factory task the session belongs to, and Held says that
	// task is still running: the session can be watched, not driven.
	Task string `json:"task,omitempty"`
	Held bool   `json:"held,omitempty"`
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
	// AutoApprove tells acpd that no human is reading this conversation,
	// so it should answer permission requests itself instead of letting
	// each one wait out its timeout. See ResearchAutoApprove.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// Task makes the session a factory task's (ID must be the task's id).
	// Created again once the task has ended, it continues the task's
	// conversation, provided Engine and CWD are the ones it ran with.
	Task string `json:"task,omitempty"`
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

// EngineRetry is a model call the engine retried: the transcript's
// engine_retry payload, and a session's Retrying.
type EngineRetry struct {
	// Status is the HTTP status that failed the call ("429", "503"),
	// "5xx" when the engine did not say which, "" when it said nothing.
	Status  string    `json:"status,omitempty"`
	Attempt int       `json:"attempt"`
	Time    time.Time `json:"time"`
}
