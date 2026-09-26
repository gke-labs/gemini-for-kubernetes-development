package acpd

import (
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

// DefaultPort is acpd's listen port, adjacent to envd's 49983 so the two
// in-pod daemons are recognisably a family.
const DefaultPort = 49984

// DefaultStateDir is where transcripts live. Under /workspaces because
// that is the PVC: durability lasts as long as the sandbox and no longer,
// which is the whole retention policy.
const DefaultStateDir = "/workspaces/.acpd/sessions"

// Server is acpd: a session registry and the HTTP surface over it.
type Server struct {
	stateDir string
	cwd      string

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
}

type sessionResponse struct {
	ID        string    `json:"id"`
	Engine    string    `json:"engine"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"createdAt"`
	Busy      bool      `json:"busy"`
	Offset    int64     `json:"offset"`
	// Mode and AvailableModes let a client that did not create the
	// session — a browser attaching to one the controller started — show
	// and change what it is running under.
	Mode           string            `json:"mode,omitempty"`
	AvailableModes []acp.SessionMode `json:"availableModes,omitempty"`
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
		Mode:         req.Mode,
		Dir:          filepath.Join(s.stateDir, req.ID),
	})
	if err != nil {
		s.mu.Lock()
		delete(s.sessions, req.ID)
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("starting session: %v", err))
		return
	}

	s.mu.Lock()
	s.sessions[req.ID] = sess
	s.mu.Unlock()

	mode, _ := sess.Modes()
	klog.FromContext(r.Context()).Info("session started", "session", sess.ID, "engine", sess.Engine, "cwd", sess.CWD, "mode", mode)
	writeJSON(w, http.StatusCreated, describe(sess))
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]sessionResponse, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess == nil {
			continue // reserved, still starting
		}
		out = append(out, describe(sess))
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
	writeJSON(w, http.StatusOK, describe(sess))
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.sessions[id]
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
	sess, ok := s.lookup(w, r)
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
	sess, ok := s.lookup(w, r)
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
	sess, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if body.Mode == "" {
		writeError(w, http.StatusBadRequest, "mode is required")
		return
	}
	if err := sess.SetMode(r.Context(), body.Mode); err != nil {
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
	writeJSON(w, http.StatusOK, describe(sess))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookup(w, r)
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

func (s *Server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func describe(sess *Session) sessionResponse {
	mode, available := sess.Modes()
	return sessionResponse{
		ID:             sess.ID,
		Engine:         sess.Engine,
		CWD:            sess.CWD,
		CreatedAt:      sess.CreatedAt,
		Busy:           sess.Busy(),
		Offset:         sess.Transcript().Size(),
		Mode:           mode,
		AvailableModes: available,
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
