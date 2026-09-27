package acpd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
	"k8s.io/klog/v2"
)

// Engine is how to start one agent CLI in ACP mode.
//
// A table rather than a switch because the only thing that differs between
// engines is these four fields, and a table makes adding one a data change
// that cannot forget a branch.
type Engine struct {
	Command string
	Args    []string
	// APIKeyEnv is the variable the CLI reads its credential from. ACP's
	// authenticate call names a method but carries no credential (see
	// acp.AuthenticateRequest), so the key has to reach the engine through
	// its environment, set at exec time.
	APIKeyEnv string
	// AuthMethodID is the default method to authenticate with, used when
	// the caller does not name one and the agent advertises several.
	AuthMethodID string
	// Env is added to the engine's environment, which acpd sets
	// explicitly rather than inheriting (see spawn). For settings the CLI
	// has no flag for and that every session started here wants — not for
	// credentials, which come from APIKeyEnv and the request.
	Env []string
}

// Engines is the set acpd knows how to start. Only gemini speaks ACP
// natively today; claude is absent rather than broken on purpose, so the
// failure is "unknown engine" at session create rather than a hang on a
// handshake that will never complete.
var Engines = map[string]Engine{
	"gemini": {
		Command:      "gemini",
		Args:         []string{"--acp"},
		APIKeyEnv:    "GEMINI_API_KEY",
		AuthMethodID: "gemini-api-key",
		// Folder trust is on by default and a fresh sandbox has no
		// ~/.gemini/trustedFolders.json, so the checkout is untrusted and
		// gemini refuses every approval mode but its own: "Cannot enable
		// privileged approval modes in an untrusted folder."
		//
		// The prompt that decision exists to force cannot be answered
		// here — there is no human at the sandbox, and the checkout is a
		// clone the session was created to work on. Saying so is
		// therefore the honest answer rather than a way around the
		// question. It does mean the checkout's own .gemini config is
		// live too: project hooks, stdio MCP servers, project GEMINI.md.
		Env: []string{"GEMINI_CLI_TRUST_WORKSPACE=true"},
	},
}

// The approval modes gemini advertises. ACP fixes the shape of a mode but
// not its name, so these are the engine's vocabulary rather than the
// protocol's: they are here to be recognised, not to be relied on. acpd
// matches whatever the caller asks for against what the engine actually
// advertised, so a renamed mode is reported as one the session did not
// get rather than disappearing into a session that quietly keeps
// prompting.
const (
	// GeminiModeDefault prompts for approval on every tool call.
	GeminiModeDefault = "default"
	// GeminiModeAutoEdit auto-approves edit tools and prompts for the rest.
	GeminiModeAutoEdit = "autoEdit"
	// GeminiModeYolo auto-approves every tool call.
	GeminiModeYolo = "yolo"
	// GeminiModePlan is read-only, and only offered when plan is enabled.
	GeminiModePlan = "plan"
)

// PermissionRequest is a tool call waiting on the user, as published to the
// transcript. RequestID is ours, not ACP's: the protocol correlates by
// JSON-RPC message id, which never leaves this process.
type PermissionRequest struct {
	RequestID string                 `json:"requestId"`
	ToolCall  acp.SessionUpdate      `json:"toolCall"`
	Options   []acp.PermissionOption `json:"options"`
}

// permissionResolution is what the HTTP layer sends back to the blocked
// handler.
type permissionResolution struct {
	optionID  string
	cancelled bool
}

// SessionConfig is everything needed to start one.
type SessionConfig struct {
	ID     string
	Engine string
	// APIKey is used to spawn the engine and then forgotten. It is never
	// stored on the Session: repo-agent re-supplies it on reconnect, so
	// retaining it would buy nothing and leak into every heap dump.
	APIKey string
	// AuthMethodID overrides the engine's default when set.
	AuthMethodID string
	// CWD is the workspace the agent operates in.
	CWD string
	// Mode is the approval mode to switch to once the session exists.
	// Empty leaves the engine on whatever it starts in. The ids are the
	// engine's, advertised in the session/new reply — see GeminiMode*.
	Mode string
	// Dir is where the transcript and engine log are written.
	Dir string
	// AutoApprove answers permission requests as they arrive instead of
	// waiting for a user.
	//
	// Mode should already have said this, and mostly it does — but an
	// engine may ask anyway. gemini's shell tool checks the command for
	// tokens lifted from earlier tool output (a prompt-injection guard)
	// before it consults the approval mode, so a yolo session still stops
	// dead on `git show <sha-it-just-read>`; there is no setting for it,
	// and answering "allow for this session" does not record an allowlist
	// entry, so the next command asks again. A session nobody is watching
	// then burns PermissionTimeout per tool call and gets cancelled.
	//
	// So this is a property of the session rather than something read out
	// of the mode: the caller states that no human will be reading, and
	// acpd stops pretending one might. Every request is still written to
	// the transcript, with its resolution, so the record is unchanged.
	//
	// It is the starting value only — SetMode carries it too, so a member
	// who tightens the mode to take the wheel gets the prompts back.
	AutoApprove bool
	// PermissionTimeout bounds how long a tool call waits on a user who
	// may have closed the tab. Zero means DefaultPermissionTimeout.
	PermissionTimeout time.Duration
}

// DefaultPermissionTimeout is how long an unanswered permission request
// blocks before being reported to the agent as cancelled. Long enough to
// survive the user reading the diff, short enough that an abandoned tab
// does not pin a turn forever.
const DefaultPermissionTimeout = 10 * time.Minute

// Session is one conversation: an engine process, the ACP client speaking
// to it, and the transcript everything lands in.
type Session struct {
	ID        string
	CWD       string
	Engine    string
	CreatedAt time.Time

	transcript *Transcript
	client     *acp.Client
	cmd        *exec.Cmd
	engineLog  *os.File

	// acpSessionID is the id the agent assigned, which is not ours: ours
	// names the sandbox and the HTTP route, the agent's is what goes on
	// the wire in every session/* call.
	acpSessionID string

	permissionTimeout time.Duration

	// availableModes is fixed at handshake — the engine advertises it once
	// and it does not change — so it needs no lock.
	availableModes []acp.SessionMode

	mu          sync.Mutex
	currentMode string
	// autoApprove says acpd answers this session's permission requests
	// itself. Under the lock and not fixed at create, because the member
	// can take the wheel: SetMode carries it, so tightening the approval
	// mode really does bring the prompts back. It used not to, and the
	// picker was quietly lying — a session created auto-approving stayed
	// auto-approving whatever the header said.
	autoApprove bool
	// modeError is why the session is not in the mode it was created
	// with, kept because the create call that would have reported it is
	// long over by the time anybody attaches.
	modeError string
	pending   map[string]chan permissionResolution
	nextReq   int64
	busy      bool
	finished  bool

	cancel context.CancelFunc
	done   chan struct{}
}

// StartSession spawns the engine, completes the ACP handshake and creates
// the agent-side session. It returns once the session is ready to take a
// prompt.
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	engine, ok := Engines[cfg.Engine]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q (known: %s)", cfg.Engine, knownEngines())
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("no API key supplied for engine %q", cfg.Engine)
	}
	if cfg.CWD == "" {
		return nil, fmt.Errorf("cwd is required")
	}

	transcript, err := OpenTranscript(cfg.Dir)
	if err != nil {
		return nil, err
	}

	timeout := cfg.PermissionTimeout
	if timeout <= 0 {
		timeout = DefaultPermissionTimeout
	}

	// The session outlives the HTTP request that created it, so it gets
	// its own context rather than borrowing one that ends at the response.
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	s := &Session{
		ID:                cfg.ID,
		CWD:               cfg.CWD,
		Engine:            cfg.Engine,
		CreatedAt:         time.Now().UTC(),
		autoApprove:       cfg.AutoApprove,
		transcript:        transcript,
		permissionTimeout: timeout,
		pending:           make(map[string]chan permissionResolution),
		cancel:            cancel,
		done:              make(chan struct{}),
	}

	if err := s.spawn(sessionCtx, engine, cfg); err != nil {
		cancel()
		transcript.Close()
		return nil, err
	}

	if err := s.handshake(sessionCtx, engine, cfg); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// spawn starts the engine process and begins reading its stdout.
func (s *Session) spawn(ctx context.Context, engine Engine, cfg SessionConfig) error {
	engineLog, err := os.OpenFile(filepath.Join(cfg.Dir, "engine.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening engine log: %w", err)
	}
	s.engineLog = engineLog

	cmd := exec.CommandContext(ctx, engine.Command, engine.Args...)
	cmd.Dir = cfg.CWD
	// An explicit environment, not the inherited one. The credential goes
	// to the engine and to nothing else: acpd's own /proc/<pid>/environ
	// stays clean, and no other process in the pod inherits it.
	cmd.Env = []string{
		"HOME=" + envOr("HOME", "/workspaces/.home"),
		"PATH=" + envOr("PATH", "/usr/local/bin:/usr/bin:/bin"),
		"TERM=dumb",
		engine.APIKeyEnv + "=" + cfg.APIKey,
	}
	cmd.Env = append(cmd.Env, engine.Env...)
	// Engine stderr is diagnostics, not conversation. It goes to a file
	// beside the transcript so a broken engine is debuggable without
	// flooding what the user reads.
	cmd.Stderr = engineLog

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("engine stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("engine stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", engine.Command, err)
	}
	// cfg.APIKey is not retained past this point.
	s.cmd = cmd

	client := acp.NewClient(stdout, stdin)
	client.OnNotification = s.onNotification
	client.OnRequest = s.onRequest
	s.client = client
	go client.Run()

	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.finished = true
		s.mu.Unlock()
		if err != nil {
			klog.FromContext(ctx).Error(err, "engine exited", "session", s.ID)
			_ = s.transcript.AppendValue(KindError, map[string]string{
				"message": fmt.Sprintf("engine exited: %v", err),
				"log":     filepath.Join(cfg.Dir, "engine.stderr.log"),
			})
		}
		close(s.done)
	}()
	return nil
}

// handshake runs initialize, authenticate where required, and session/new.
func (s *Session) handshake(ctx context.Context, engine Engine, cfg SessionConfig) error {
	init, err := s.client.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion,
		// No fs capabilities: the agent runs in the same pod as the files
		// and has its own tools for them. Declaring these would route
		// every read through acpd for no gain.
		ClientCapabilities: acp.ClientCapabilities{},
		ClientInfo:         &acp.ClientInfo{Name: "acpd", Version: "1"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	if methodID := chooseAuthMethod(init.AuthMethods, cfg.AuthMethodID, engine.AuthMethodID); methodID != "" {
		if err := s.client.Authenticate(ctx, acp.AuthenticateRequest{MethodID: methodID}); err != nil {
			return fmt.Errorf("authenticate as %q: %w", methodID, err)
		}
	}

	resp, err := s.client.NewSession(ctx, acp.NewSessionRequest{CWD: cfg.CWD})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	s.acpSessionID = resp.SessionID
	if resp.Modes != nil {
		s.availableModes = resp.Modes.AvailableModes
		s.currentMode = resp.Modes.CurrentModeID
	}

	if cfg.Mode == "" {
		return nil
	}
	// An engine with no modes at all has not refused one, it has never
	// heard of them, and there is nothing to report to anybody.
	if len(s.availableModes) == 0 {
		klog.FromContext(ctx).Info("engine advertises no session modes; ignoring the requested one",
			"session", s.ID, "engine", cfg.Engine, "mode", cfg.Mode)
		return nil
	}
	// A mode the caller asked for and did not get is worth saying loudly,
	// but not worth throwing the session away over. The engine is up, it
	// has a session, and it will answer questions — it will just ask
	// before it acts. Failing the create instead costs the member the
	// conversation, the sandbox and the clone, and leaves nothing behind
	// to read but an HTTP status.
	//
	// So it is recorded in two places, because it has two readers: the
	// transcript, where it sits above the first prompt as the reason this
	// conversation keeps stopping, and the session itself, where it
	// outlives a reload and a tab opened later.
	if err := s.SetMode(ctx, cfg.Mode, cfg.AutoApprove); err != nil {
		klog.FromContext(ctx).Error(err, "session did not start in the requested mode",
			"session", s.ID, "engine", cfg.Engine, "mode", cfg.Mode)
		s.mu.Lock()
		s.modeError = err.Error()
		s.mu.Unlock()
		_ = s.transcript.AppendValue(KindError, map[string]string{
			"message": fmt.Sprintf("this session asked to start in %q and the engine refused: %v."+
				" It will ask permission before each tool call.", cfg.Mode, err),
		})
	}
	return nil
}

// Modes reports the session's current mode and the set it may be switched
// to. Both are empty for an engine that does not implement modes.
func (s *Session) Modes() (string, []acp.SessionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentMode, s.availableModes
}

// ModeError reports why the session is not in the mode it was asked to
// start in, and is empty when it is (or was never asked).
func (s *Session) ModeError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modeError
}

// AutoApprove reports whether acpd answers this session's permission
// requests itself rather than waiting for a client.
func (s *Session) AutoApprove() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoApprove
}

// SetMode switches the session's approval mode, and with it whether acpd
// answers permission requests on the session's behalf.
//
// The two move together because they are one question to whoever is
// looking at the session — "will this ask me before it acts?" — answered
// in two layers, and a control that changed only the engine's half was
// worse than none: a member who tightened the mode to take the wheel
// still had acpd auto-answering underneath. autoApprove is the caller's
// to state; acpd does not read per-engine meaning into mode ids.
//
// Checked against what the engine advertised before it goes on the wire,
// so a typo fails here with the list rather than as an opaque engine
// error. Recorded in the transcript because a mode is the answer to "why
// was nothing asked before that command ran" — a session that stops
// prompting must say when it started doing so, and who asked.
func (s *Session) SetMode(ctx context.Context, modeID string, autoApprove bool) error {
	if modeID == "" {
		return fmt.Errorf("mode is required")
	}
	if !s.offersMode(modeID) {
		return fmt.Errorf("engine does not offer mode %q (offers: %s)", modeID, s.modeIDs())
	}
	if err := s.client.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionID: s.acpSessionID,
		ModeID:    modeID,
	}); err != nil {
		return fmt.Errorf("session/set_mode %q: %w", modeID, err)
	}

	s.mu.Lock()
	s.currentMode = modeID
	s.autoApprove = autoApprove
	// A switch that worked answers the complaint the create left behind,
	// whether or not it is the mode that was refused: the member has just
	// been told what the session is in, by the session doing it.
	s.modeError = ""
	s.mu.Unlock()

	// autoApprove rides the same entry: a reader asking why a command ran
	// unasked needs both halves of the answer, at the same point in time.
	_ = s.transcript.AppendValue(KindModeChanged, map[string]any{
		"currentModeId": modeID,
		"autoApprove":   autoApprove,
	})
	return nil
}

func (s *Session) offersMode(modeID string) bool {
	for _, m := range s.availableModes {
		if m.ID == modeID {
			return true
		}
	}
	return false
}

func (s *Session) modeIDs() string {
	ids := make([]string, 0, len(s.availableModes))
	for _, m := range s.availableModes {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

// chooseAuthMethod picks the method to authenticate with, or "" to skip
// authenticate entirely.
//
// An agent that advertises no methods needs no authentication — calling
// authenticate anyway is an error on some agents. Otherwise the caller's
// choice wins, then the engine default if the agent offers it, then the
// sole method when there is only one.
func chooseAuthMethod(advertised []acp.AuthMethod, requested, engineDefault string) string {
	if len(advertised) == 0 {
		return ""
	}
	has := func(id string) bool {
		for _, m := range advertised {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if requested != "" && has(requested) {
		return requested
	}
	if engineDefault != "" && has(engineDefault) {
		return engineDefault
	}
	if len(advertised) == 1 {
		return advertised[0].ID
	}
	// Several offered and none of them ours: pick nothing rather than
	// guess into an OAuth flow that will block on a browser nobody is
	// watching.
	return ""
}

// allowOnceOption picks the option an auto-approved session answers with,
// and reports whether there was one.
//
// Once, not always: an "allow always" answer asks the engine to remember a
// rule derived from this call, which is a broader thing to decide than the
// call itself and — for the check that makes AutoApprove necessary at all
// — is refused anyway. Answering each request on its own merits costs a
// round trip nobody is waiting through. An always option is still better
// than nothing if once is not offered.
func allowOnceOption(options []acp.PermissionOption) (string, bool) {
	fallback := ""
	for _, o := range options {
		switch o.Kind {
		case acp.PermissionAllowOnce:
			return o.OptionID, true
		case acp.PermissionAllowAlways:
			if fallback == "" {
				fallback = o.OptionID
			}
		}
	}
	return fallback, fallback != ""
}

// onNotification writes agent traffic straight through to the transcript.
//
// Session updates are recorded under their own ACP kind, so a variant this
// build does not understand is still persisted and still reaches the
// browser, which can ignore it without acpd having to be taught about it
// first.
func (s *Session) onNotification(method string, params json.RawMessage) {
	if method != acp.MethodSessionUpdate {
		return
	}
	var notif acp.SessionUpdateNotification
	if err := json.Unmarshal(params, &notif); err != nil {
		_ = s.transcript.AppendValue(KindError, map[string]string{
			"message": fmt.Sprintf("undecodable session/update: %v", err),
		})
		return
	}
	// An agent may leave a mode on its own — plan mode ends when the plan
	// does — so the mode acpd reports has to follow the engine's word and
	// not only its own set_mode calls.
	if notif.Update.SessionUpdateKind == acp.UpdateCurrentMode && notif.Update.CurrentModeID != "" {
		s.mu.Lock()
		s.currentMode = notif.Update.CurrentModeID
		s.mu.Unlock()
	}
	data, err := json.Marshal(notif.Update)
	if err != nil {
		return
	}
	if err := s.transcript.Append(notif.Update.SessionUpdateKind, data); err != nil {
		klog.Background().Error(err, "appending session update", "session", s.ID)
	}
}

// onRequest answers agent-initiated requests. Only permission prompts are
// supported; fs/* is declined because we declared no fs capability, so an
// agent asking anyway is asking for something it was told we lack.
//
// This runs on its own goroutine (acp.Client dispatches requests that way
// precisely so a handler may block), which is what lets it wait on a human.
func (s *Session) onRequest(method string, params json.RawMessage) (any, *acp.RPCError) {
	if method != acp.MethodRequestPermission {
		return nil, &acp.RPCError{
			Code:    acp.MethodNotFound,
			Message: fmt.Sprintf("acpd does not implement %s", method),
		}
	}

	var req acp.RequestPermissionParams
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &acp.RPCError{Code: acp.InvalidParams, Message: err.Error()}
	}

	// An auto-approved session answers before anything is made pending:
	// no client may see this as a request it could still resolve, because
	// no client ever will. Both events are still written — the transcript
	// is the record of what the agent did, and "it asked, and was allowed
	// without being read" is part of that.
	//
	// Not when every option denies: approving is then not on offer, so the
	// request falls through to a human, or to the timeout.
	autoOption, autoOK := "", false

	s.mu.Lock()
	if s.autoApprove {
		autoOption, autoOK = allowOnceOption(req.Options)
	}
	s.nextReq++
	requestID := fmt.Sprintf("perm-%d", s.nextReq)
	ch := make(chan permissionResolution, 1)
	if !autoOK {
		s.pending[requestID] = ch
	}
	s.mu.Unlock()

	if err := s.transcript.AppendValue(KindPermissionRequest, PermissionRequest{
		RequestID: requestID,
		ToolCall:  req.ToolCall,
		Options:   req.Options,
	}); err != nil {
		return nil, &acp.RPCError{Code: acp.InternalError, Message: err.Error()}
	}

	if autoOK {
		_ = s.transcript.AppendValue(KindPermissionResolved, map[string]any{
			"requestId": requestID,
			"outcome":   acp.PermissionSelected,
			"optionId":  autoOption,
			"reason":    "auto-approved: the session is running with autoApprove",
		})
		return acp.RequestPermissionResult{Outcome: acp.PermissionOutcome{
			Outcome:  acp.PermissionSelected,
			OptionID: autoOption,
		}}, nil
	}

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	select {
	case res := <-ch:
		outcome := acp.PermissionOutcome{Outcome: acp.PermissionSelected, OptionID: res.optionID}
		if res.cancelled {
			outcome = acp.PermissionOutcome{Outcome: acp.PermissionCancelled}
		}
		_ = s.transcript.AppendValue(KindPermissionResolved, map[string]any{
			"requestId": requestID,
			"outcome":   outcome.Outcome,
			"optionId":  outcome.OptionID,
		})
		return acp.RequestPermissionResult{Outcome: outcome}, nil

	case <-time.After(s.permissionTimeout):
		_ = s.transcript.AppendValue(KindPermissionResolved, map[string]any{
			"requestId": requestID,
			"outcome":   acp.PermissionCancelled,
			"reason":    "timed out waiting for the user",
		})
		return acp.RequestPermissionResult{
			Outcome: acp.PermissionOutcome{Outcome: acp.PermissionCancelled},
		}, nil

	case <-s.done:
		return nil, &acp.RPCError{Code: acp.InternalError, Message: "session ended"}
	}
}

// ResolvePermission answers a pending permission request. It reports
// whether the request was still waiting: a stale click on a request that
// already timed out is not an error worth failing the HTTP call over, but
// the caller should know it changed nothing.
func (s *Session) ResolvePermission(requestID, optionID string, cancelled bool) bool {
	s.mu.Lock()
	ch, ok := s.pending[requestID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- permissionResolution{optionID: optionID, cancelled: cancelled}:
		return true
	default:
		// Already answered by another caller; the first answer stands.
		return false
	}
}

// Prompt sends a user turn and returns immediately.
//
// The turn runs in the background because a prompt can take minutes and
// its output is already going to the transcript: making the HTTP call wait
// would put a proxy timeout between the user and their answer while adding
// nothing, since the browser is following the stream either way.
func (s *Session) Prompt(text string) error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return fmt.Errorf("session %s has ended", s.ID)
	}
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("session %s is already handling a turn", s.ID)
	}
	s.busy = true
	s.mu.Unlock()

	if err := s.transcript.AppendValue(KindUserPrompt, map[string]string{"text": text}); err != nil {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		return err
	}

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()

		resp, err := s.client.Prompt(context.Background(), acp.PromptRequest{
			SessionID: s.acpSessionID,
			Prompt:    []acp.ContentBlock{{Type: "text", Text: text}},
		})
		if err != nil {
			_ = s.transcript.AppendValue(KindError, map[string]string{
				"message": fmt.Sprintf("prompt failed: %v", err),
			})
			return
		}
		_ = s.transcript.AppendValue(KindTurnEnd, map[string]string{
			"stopReason": resp.StopReason,
		})
	}()
	return nil
}

// Cancel interrupts the current turn. ACP models this as a notification,
// so the agent finishes with a cancelled stopReason rather than the call
// failing.
func (s *Session) Cancel() error {
	return s.client.Notify("session/cancel", map[string]string{"sessionId": s.acpSessionID})
}

// Busy reports whether a turn is in flight.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Waiting reports whether a tool call is blocked on a permission answer
// nobody has given yet.
//
// Separate from Busy because they are the same bit to a client that can
// only see Busy, and they mean opposite things to whoever is reading the
// list: a busy session is working and wants to be left alone, a waiting
// one has stopped and wants you. Left indistinguishable, the second one
// sits there until PermissionTimeout and then loses the turn — the one
// state where not knowing costs something.
//
// Always false on an auto-approving session: onRequest answers before
// anything is made pending, so there is never a request outstanding.
func (s *Session) Waiting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
}

// Transcript is the session's event log.
func (s *Session) Transcript() *Transcript { return s.transcript }

// Close ends the engine process and releases followers.
func (s *Session) Close() error {
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if s.engineLog != nil {
		_ = s.engineLog.Close()
	}
	return s.transcript.Close()
}

func knownEngines() string {
	names := make([]string, 0, len(Engines))
	for name := range Engines {
		names = append(names, name)
	}
	return fmt.Sprint(names)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
