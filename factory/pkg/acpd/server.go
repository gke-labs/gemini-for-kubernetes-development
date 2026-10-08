package acpd

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
	"k8s.io/klog/v2"
)

// APIKeyHeader carries the engine credential on session create.
//
// Deliberately not Authorization: that header means "the caller is
// authorized to call me", and this is a downstream credential being handed
// over for relay. Conflating them would make acpd's access rule "possesses
// some API key", which is not access control, because an attacker brings
// their own. Authorization is left free for real caller auth.
const APIKeyHeader = "X-Engine-Api-Key"

// DefaultStateDir is where transcripts live. Under /workspaces because
// that is the PVC: durability lasts as long as the sandbox and no longer,
// which is the whole retention policy.
const DefaultStateDir = "/workspaces/.acpd/sessions"

// TaskTokenHeader carries a task's token. While a task runs, only the
// task — the one process given its token — may create or drive its
// session; everyone else watches.
const TaskTokenHeader = "X-Factory-Task-Token"

// Tasks is what acpd needs from a host that runs tasks, for sessions that
// belong to one: where the task keeps its files, whether it is still
// running, and the token it was started with.
type Tasks interface {
	// Dir is the task's directory, an error when there is no such task.
	Dir(task string) (string, error)
	Running(task string) bool
	// Token is the token the task was started with, "" when it has none:
	// it was not started by this host, or not since the host started.
	Token(task string) string
}

// Server is acpd: a session registry and the HTTP surface over it.
type Server struct {
	stateDir string
	cwd      string
	// tasks is nil for an acpd that hosts no tasks, which then refuses
	// sessions that name one.
	tasks Tasks

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewServer returns a server storing transcripts under stateDir and
// starting engines in cwd.
func NewServer(stateDir, cwd string) *Server {
	return &Server{
		stateDir: stateDir,
		cwd:      cwd,
		sessions: make(map[string]*Session),
	}
}

// SetTasks lets sessions belong to the host's tasks. Called before the
// server takes requests.
func (s *Server) SetTasks(t Tasks) { s.tasks = t }

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /sessions", s.handleCreateSession)
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /sessions/{id}/prompt", s.handlePrompt)
	mux.HandleFunc("GET /sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /sessions/{id}/permission", s.handlePermission)
	mux.HandleFunc("POST /sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /sessions/{id}/mode", s.handleSetMode)
	return mux
}

// Close ends every session. Used on shutdown; a session cannot outlive
// acpd because the engine is its child and the credential is gone.
func (s *Server) Close() {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = make(map[string]*Session)
	s.mu.Unlock()

	for _, sess := range sessions {
		_ = sess.Close()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"sessions": s.count(),
	})
}

// createSessionRequest is the body of POST /sessions. The credential is
// not in here — it arrives in APIKeyHeader, so it cannot end up in a
// request log or, worse, in the transcript this call creates.
type createSessionRequest struct {
	ID           string `json:"id"`
	Engine       string `json:"engine"`
	AuthMethodID string `json:"authMethodId,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	// Mode is the approval mode to start in. Empty leaves the engine's
	// own default, which for gemini means prompting on every tool call.
	Mode string `json:"mode,omitempty"`
	// AutoApprove says no human will be reading this conversation, so
	// acpd should answer permission requests itself rather than let a
	// tool call wait out the timeout. Set it for sessions started by a
	// controller; see SessionConfig.AutoApprove for why a permissive Mode
	// is not enough on its own.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// Model is the model to start the engine on; empty, its default.
	Model string `json:"model,omitempty"`
	// Task makes the session the named task's: it is named after the task
	// and kept in the task's directory, beside its output. While the task
	// runs, only the task, sending its token in TaskTokenHeader, may
	// create or drive it. Created again once the task has ended, by
	// anybody, it continues the task's conversation.
	Task string `json:"task,omitempty"`
}

type sessionResponse struct {
	ID        string    `json:"id"`
	Engine    string    `json:"engine"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"createdAt"`
	Busy      bool      `json:"busy"`
	// Waiting says the turn stopped on a permission request that is still
	// unanswered. It is a refinement of Busy, never a replacement: a
	// waiting session is also busy, and a client that only knows Busy
	// stays correct.
	Waiting bool `json:"waiting,omitempty"`
	// Retrying is the model call the engine is retrying while the turn is
	// in flight: a session that is busy and silent because its model calls
	// fail (rate limits, quota, an overloaded model), not because it is
	// thinking. Another refinement of Busy.
	Retrying *EngineRetry `json:"retrying,omitempty"`
	Offset   int64        `json:"offset"`
	// Mode and AvailableModes let a client that did not create the
	// session — a browser attaching to one the controller started — show
	// and change what it is running under.
	Mode           string            `json:"mode,omitempty"`
	AvailableModes []acp.SessionMode `json:"availableModes,omitempty"`
	// ModeError is why Mode is not the mode the session was created with.
	// The session runs anyway; this is what a client shows instead of
	// letting it look like nobody asked.
	ModeError string `json:"modeError,omitempty"`
	// AutoApprove reports that acpd answers this session's permission
	// requests itself. A browser that attaches to one of these is a
	// spectator: it will see requests in the transcript, already resolved.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// Loaded says the session continues a conversation an earlier engine
	// had, so the agent remembers the transcript above; false, it starts
	// from nothing whatever the transcript holds.
	Loaded bool `json:"loaded,omitempty"`
	// Task is the task the session belongs to.
	Task string `json:"task,omitempty"`
	// Held says the session's task is running, so only the task may
	// prompt, answer, switch mode, cancel or close it.
	Held bool `json:"held,omitempty"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get(APIKeyHeader)
	if apiKey == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s header is required", APIKeyHeader))
		return
	}

	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Engine == "" {
		req.Engine = "gemini"
	}
	cwd := req.CWD
	if cwd == "" {
		cwd = s.cwd
	}
	dir := filepath.Join(s.stateDir, req.ID)
	if req.Task != "" {
		if s.tasks == nil {
			writeError(w, http.StatusBadRequest, "this acpd runs no tasks")
			return
		}
		if req.ID != req.Task {
			writeError(w, http.StatusBadRequest, "a task's session is named after the task")
			return
		}
		taskDir, err := s.tasks.Dir(req.Task)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		dir = filepath.Join(taskDir, "session")
		if !s.isTask(req.Task, r) {
			writeError(w, http.StatusConflict, fmt.Sprintf("task %s is running: only the task may start its session", req.Task))
			return
		}
	}

	s.mu.Lock()
	if _, exists := s.sessions[req.ID]; exists {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Sprintf("session %s already exists", req.ID))
		return
	}
	// Reserve the id before the slow part so two concurrent creates cannot
	// both spawn an engine for it.
	s.sessions[req.ID] = nil
	s.mu.Unlock()

	sess, err := StartSession(r.Context(), SessionConfig{
		ID:           req.ID,
		Engine:       req.Engine,
		APIKey:       apiKey,
		AuthMethodID: req.AuthMethodID,
		CWD:          cwd,
		Model:        req.Model,
		Mode:         req.Mode,
		AutoApprove:  req.AutoApprove,
		Dir:          dir,
	})
	if err != nil {
		s.mu.Lock()
		delete(s.sessions, req.ID)
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("starting session: %v", err))
		return
	}

	sess.Task = req.Task

	s.mu.Lock()
	s.sessions[req.ID] = sess
	s.mu.Unlock()

	mode, _ := sess.Modes()
	klog.FromContext(r.Context()).Info("session started", "session", sess.ID, "engine", sess.Engine, "cwd", sess.CWD, "mode", mode, "loaded", sess.Loaded(), "task", sess.Task)
	writeJSON(w, http.StatusCreated, s.describe(sess))
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]sessionResponse, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess == nil {
			continue // reserved, still starting
		}
		out = append(out, s.describe(sess))
	}
	s.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.describe(sess))
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.sessions[id]
	if sess != nil && s.heldFrom(sess, r) {
		s.mu.Unlock()
		writeHeld(w, sess)
		return
	}
	delete(s.sessions, id)
	s.mu.Unlock()

	if sess == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no session %s", id))
		return
	}
	if err := sess.Close(); err != nil {
		klog.FromContext(r.Context()).Error(err, "closing session", "session", id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupToDrive(w, r)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if body.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if err := sess.Prompt(body.Text); err != nil {
		// A turn already in flight is the caller's race, not a server
		// fault: 409 so the UI can disable the composer rather than retry.
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	// Accepted, not OK: the turn is running and its output is on the event
	// stream, which the caller is already following.
	writeJSON(w, http.StatusAccepted, map[string]any{"offset": sess.Transcript().Size()})
}

// handleEvents streams the transcript as newline-delimited JSON from
// ?offset= (bytes, default 0), following until the client goes away.
//
// The same bytes that persist are the ones streamed, so a reconnecting
// browser asking from its last offset gets exactly what it missed — there
// is no separate replay path that could disagree with the file.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookup(w, r)
	if !ok {
		return
	}

	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid offset %q", raw))
			return
		}
		offset = parsed
	}
	follow := r.URL.Query().Get("follow") != "false"

	flusher, canFlush := w.(http.Flusher)
	if !canFlush && follow {
		writeError(w, http.StatusInternalServerError, "streaming unsupported by this transport")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	// Chunked, not buffered: a proxy holding a turn's output until the
	// response ends would defeat the entire point of streaming.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// Flush the headers now rather than with the first event. WriteHeader
	// only buffers them, so a client opening the stream on a quiet session
	// would block in the request itself until the agent happened to say
	// something — which is exactly when a browser opens it.
	if canFlush {
		flusher.Flush()
	}

	transcript := sess.Transcript()
	ctx := r.Context()
	for {
		chunk, next, err := transcript.ReadFrom(offset)
		if err != nil {
			klog.FromContext(ctx).Error(err, "reading transcript", "session", sess.ID)
			return
		}
		if len(chunk) > 0 {
			if _, err := w.Write(chunk); err != nil {
				return // client went away
			}
			offset = next
			if canFlush {
				flusher.Flush()
			}
		}
		if !follow {
			return
		}
		if !transcript.Wait(ctx, offset) {
			return
		}
	}
}

func (s *Server) handlePermission(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupToDrive(w, r)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		OptionID  string `json:"optionId"`
		Cancelled bool   `json:"cancelled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if body.RequestID == "" {
		writeError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	if body.OptionID == "" && !body.Cancelled {
		writeError(w, http.StatusBadRequest, "optionId is required unless cancelled is set")
		return
	}
	if !sess.ResolvePermission(body.RequestID, body.OptionID, body.Cancelled) {
		// Gone, not an error: the request timed out or someone else
		// answered first. 409 tells the UI to re-read the transcript
		// rather than to retry.
		writeError(w, http.StatusConflict, fmt.Sprintf("permission request %s is no longer waiting", body.RequestID))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetMode switches the session's approval mode mid-conversation.
//
// Allowed while a turn is in flight on purpose: the reason to reach for
// this is usually a prompt that has just appeared, and making the member
// stop the turn first would throw away the work that produced it. The
// engine applies the new mode to the next tool call either way.
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupToDrive(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
		// AutoApprove goes with the mode rather than having its own call:
		// they answer the same question in two layers, and leaving acpd
		// auto-answering under a mode the member just tightened is the
		// bug this field exists to close. Absent means false — a caller
		// switching modes without saying so wants to be asked.
		AutoApprove bool `json:"autoApprove,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if body.Mode == "" {
		writeError(w, http.StatusBadRequest, "mode is required")
		return
	}
	if err := sess.SetMode(r.Context(), body.Mode, body.AutoApprove); err != nil {
		// A mode the engine does not offer is the caller's mistake, and
		// the message names what it does offer. Anything else is the
		// engine failing to answer.
		if !sess.offersMode(body.Mode) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.describe(sess))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupToDrive(w, r)
	if !ok {
		return
	}
	if err := sess.Cancel(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cancelling: %v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	id := r.PathValue("id")
	s.mu.Lock()
	sess, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok || sess == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no session %s", id))
		return nil, false
	}
	return sess, true
}

// lookupToDrive is lookup for a call that changes the session, refused to
// all but its owner while the session's task runs.
func (s *Server) lookupToDrive(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, ok := s.lookup(w, r)
	if !ok {
		return nil, false
	}
	if s.heldFrom(sess, r) {
		writeHeld(w, sess)
		return nil, false
	}
	return sess, true
}

// held reports whether sess's task is running, which makes the session
// the task's to drive.
func (s *Server) held(sess *Session) bool {
	return sess.Task != "" && s.tasks != nil && s.tasks.Running(sess.Task)
}

// heldFrom reports whether sess is held from the caller: its task runs and
// the caller is not the task.
func (s *Server) heldFrom(sess *Session, r *http.Request) bool {
	return sess.Task != "" && !s.isTask(sess.Task, r)
}

// isTask reports whether the request may act for task: the task has ended,
// so its session is anybody's, or the request carries its token.
func (s *Server) isTask(task string, r *http.Request) bool {
	if !s.tasks.Running(task) {
		return true
	}
	token := s.tasks.Token(task)
	return token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(TaskTokenHeader)), []byte(token)) == 1
}

func writeHeld(w http.ResponseWriter, sess *Session) {
	writeError(w, http.StatusConflict, fmt.Sprintf("session %s belongs to task %s, which is running: it can be watched, not driven", sess.ID, sess.Task))
}

// CloseTask ends the sessions that belong to task: it was cancelled, and
// its agent goes with it.
func (s *Server) CloseTask(task string) {
	s.mu.Lock()
	var closing []*Session
	for id, sess := range s.sessions {
		if sess != nil && sess.Task == task {
			closing = append(closing, sess)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
	for _, sess := range closing {
		_ = sess.Close()
	}
}

func (s *Server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) describe(sess *Session) sessionResponse {
	mode, available := sess.Modes()
	return sessionResponse{
		Task:           sess.Task,
		Held:           s.held(sess),
		ID:             sess.ID,
		Engine:         sess.Engine,
		CWD:            sess.CWD,
		CreatedAt:      sess.CreatedAt,
		Busy:           sess.Busy(),
		Waiting:        sess.Waiting(),
		Retrying:       sess.Retrying(),
		Offset:         sess.Transcript().Size(),
		Mode:           mode,
		AvailableModes: available,
		ModeError:      sess.ModeError(),
		AutoApprove:    sess.AutoApprove(),
		Loaded:         sess.Loaded(),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		klog.Background().Error(err, "writing response")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// StateDirFromEnv resolves the transcript root, honouring ACPD_STATE_DIR.
func StateDirFromEnv() string {
	if dir := os.Getenv("ACPD_STATE_DIR"); dir != "" {
		return dir
	}
	return DefaultStateDir
}
