package termd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gorilla/websocket"
)

// APIKeyHeader carries the engine's credential on POST /terminals, as on
// acpd's POST /sessions: the daemon keeps none of its own.
const APIKeyHeader = acpd.APIKeyHeader

// maxLive is how many sessions may run at once.
const maxLive = 16

// exitedKeep is how long an ended session stays listed, for a client to
// see how it ended, while nobody is attached.
const exitedKeep = time.Hour

// stateInterval is how often an attached client's state is looked at.
var stateInterval = time.Second

const (
	pingInterval = 25 * time.Second
	readTimeout  = 75 * time.Second
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,199}$`)

// resumeIDRE is an engine's conversation id: it goes on a command line.
var resumeIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// CreateRequest is POST /terminals.
type CreateRequest struct {
	// ID names the session. A session of that ID still running is the
	// one returned (attach-or-create, as tmux new -A); an ended one is
	// replaced. Empty, the daemon picks one.
	ID string `json:"id,omitempty"`
	// Program is shell (the default), gemini, claude or agy.
	Program string `json:"program,omitempty"`
	// Resume continues the engine's latest conversation in its working
	// directory, else opens a new one.
	Resume bool `json:"resume,omitempty"`
	// ResumeID continues that conversation of the engine's (its own id:
	// acpd's record, handed off to a terminal), rather than its latest.
	ResumeID string `json:"resumeId,omitempty"`
	// CWD is the working directory; empty, the workspace's checkout.
	CWD  string `json:"cwd,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// Frame is a control message on an attach websocket, as JSON in a text
// frame; output is in binary frames.
//
// Daemon to client: attached {offset, reset} first, then snapshot {data}
// when the client's bytes are no longer kept, state {state, message,
// title} first and whenever what the program is doing changes (activity.go),
// and exit {code} when the program ends. Client to daemon: input {data} and resize {cols, rows};
// a binary frame is input too.
type Frame struct {
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Reset  bool   `json:"reset,omitempty"`
	Code   int    `json:"code,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
	// State, Message and Title are a state frame's (Info's).
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
	Title   string `json:"title,omitempty"`
}

// Manager is the daemon's terminal sessions.
type Manager struct {
	root string
	// environ is the daemon's environment; a var for the tests.
	environ func() []string

	mu       sync.Mutex
	sessions map[string]*Session
	seq      int
}

// NewManager starts sessions in root's checkout (the workspace).
func NewManager(root string) *Manager {
	return &Manager{root: root, environ: os.Environ, sessions: map[string]*Session{}}
}

// Create starts a session, or returns the running one of req.ID (created
// false).
func (m *Manager) Create(req CreateRequest, apiKey string) (*Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := req.ID
	if id == "" {
		m.seq++
		id = fmt.Sprintf("term-%d", m.seq)
	}
	if !idRE.MatchString(id) {
		return nil, false, fmt.Errorf("invalid terminal id %q", id)
	}
	if s, ok := m.sessions[id]; ok {
		if !s.Info().Exited {
			return s, false, nil
		}
		delete(m.sessions, id)
		s.release()
	}
	live := 0
	for _, s := range m.sessions {
		if !s.Info().Exited {
			live++
		}
	}
	if live >= maxLive {
		return nil, false, fmt.Errorf("%d terminals are running already; close one", live)
	}
	spec, err := m.spec(req, apiKey)
	if err != nil {
		return nil, false, err
	}
	s, err := start(id, spec)
	if err != nil {
		return nil, false, err
	}
	m.sessions[id] = s
	return s, true, nil
}

// spec is what req runs: the program, its arguments, and the pod's
// environment less credentials (acpd.InheritedEnv) plus the terminal's
// and the engine's own.
func (m *Manager) spec(req CreateRequest, apiKey string) (Spec, error) {
	dir := req.CWD
	if dir == "" {
		dir = checkout(m.root)
	}
	if req.ResumeID != "" && !resumeIDRE.MatchString(req.ResumeID) {
		return Spec{}, fmt.Errorf("invalid resume id %q", req.ResumeID)
	}
	if !filepath.IsAbs(dir) {
		return Spec{}, fmt.Errorf("cwd %q is not absolute", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Spec{}, fmt.Errorf("cwd %q is not a directory", dir)
	}
	env := acpd.InheritedEnv(m.environ())
	env = append(env,
		"HOME="+getenv(m.environ(), "HOME", "/workspaces/.home"),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	program := req.Program
	if program == "" {
		program = "shell"
	}
	var argv []string
	switch program {
	case "shell":
		sh := "/bin/bash"
		if _, err := os.Stat(sh); err != nil {
			sh = "/bin/sh"
		}
		argv = []string{sh, "-l"}
	case "gemini":
		env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
		argv = engineArgv("gemini --skip-trust --include-directories "+m.root, "--resume latest", req.Resume)
		if req.ResumeID != "" {
			argv = []string{"/bin/sh", "-c", "exec gemini --skip-trust --include-directories " + m.root + " --resume " + req.ResumeID}
		}
	case "claude":
		argv = engineArgv("claude", "--continue", req.Resume)
		if req.ResumeID != "" {
			argv = []string{"/bin/sh", "-c", "exec claude --resume " + req.ResumeID}
		}
	case "agy":
		// agy's CLI takes no conversation id: its latest in the checkout,
		// which a conversation just handed off is.
		argv = engineArgv("agy --add-dir "+m.root, "--continue", req.Resume || req.ResumeID != "")
	default:
		return Spec{}, fmt.Errorf("unknown program %q (shell, gemini, claude, agy)", program)
	}
	if apiKey != "" {
		switch program {
		case "gemini", "agy":
			env = append(env, "GEMINI_API_KEY="+apiKey)
		case "claude":
			env = append(env, "ANTHROPIC_API_KEY="+apiKey)
		}
	}
	return Spec{Argv: argv, Env: env, Dir: dir, Program: program, Cols: req.Cols, Rows: req.Rows}, nil
}

// engineArgv runs an engine's CLI; resuming, its latest conversation,
// else - none to resume - a new one.
func engineArgv(cmd, resume string, wantResume bool) []string {
	script := "exec " + cmd
	if wantResume {
		script = cmd + " " + resume + " || exec " + cmd
	}
	return []string{"/bin/sh", "-c", script}
}

// checkout is the workspace's repository checkout - its first directory
// with a .git - else the workspace.
func checkout(root string) string {
	gits, _ := filepath.Glob(filepath.Join(root, "*", ".git"))
	sort.Strings(gits)
	if len(gits) > 0 {
		return filepath.Dir(gits[0])
	}
	return root
}

func getenv(environ []string, key, def string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == key && v != "" {
			return v
		}
	}
	return def
}

// Get is the session of id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List is every session, oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	out := make([]Info, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Delete ends a session and forgets it.
func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok {
		s.Kill(3 * time.Second)
		s.release()
	}
	return ok
}

// Reap forgets ended sessions nobody has looked at for exitedKeep, every
// interval until stop closes.
func (m *Manager) Reap(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		for id, s := range m.sessions {
			in := s.Info()
			if in.Exited && in.Clients == 0 && in.Ended != nil && time.Since(*in.Ended) > exitedKeep {
				delete(m.sessions, id)
				s.release()
			}
		}
		m.mu.Unlock()
	}
}

// Close ends every session.
func (m *Manager) Close() {
	m.mu.Lock()
	all := m.sessions
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func(s *Session) { defer wg.Done(); s.Kill(2 * time.Second); s.release() }(s)
	}
	wg.Wait()
}

// Handler serves the sessions:
//
//	GET    /terminals                     every session
//	POST   /terminals                     start one (CreateRequest); attach-or-create by id
//	GET    /terminals/{id}                one
//	DELETE /terminals/{id}                end it
//	GET    /terminals/{id}/attach?offset=N  websocket: output from byte N (Frame)
func (m *Manager) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /terminals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.List())
	})
	mux.HandleFunc("POST /terminals", m.handleCreate)
	mux.HandleFunc("GET /terminals/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.Get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "no such terminal")
			return
		}
		writeJSON(w, http.StatusOK, s.Info())
	})
	mux.HandleFunc("DELETE /terminals/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !m.Delete(r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "no such terminal")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /terminals/{id}/attach", m.handleAttach)
	return mux
}

func (m *Manager) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	s, created, err := m.Create(req, r.Header.Get(APIKeyHeader))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.Info())
}

// The daemon listens on the pod's loopback only, behind a port-forward the
// API server authorises: any origin that gets this far is a caller.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     func(*http.Request) bool { return true },
}

func (m *Manager) handleAttach(w http.ResponseWriter, r *http.Request) {
	s, ok := m.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such terminal")
		return
	}
	want := int64(-1)
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		want = n
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	serveAttach(ws, s, want)
}

// serveAttach streams s to one client until either ends.
func serveAttach(ws *websocket.Conn, s *Session, want int64) {
	defer ws.Close()
	att := s.attach(want)
	defer s.detach()

	var wmu sync.Mutex
	send := func(kind int, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return ws.WriteMessage(kind, p)
	}
	sendFrame := func(f Frame) error {
		b, _ := json.Marshal(f)
		return send(websocket.TextMessage, b)
	}
	announce := func(a Attachment) error {
		if err := sendFrame(Frame{Type: "attached", Offset: a.Offset, Reset: a.Reset}); err != nil {
			return err
		}
		if a.Snapshot != "" {
			return sendFrame(Frame{Type: "snapshot", Data: a.Snapshot})
		}
		return nil
	}

	stop := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(stop); s.wake() }) }
	defer end()

	// What the program is doing, whenever it changes; started once the
	// client has its attached frame, which comes first.
	watchState := func() {
		t := time.NewTicker(stateInterval)
		defer t.Stop()
		var last Frame
		for {
			in := s.Info()
			f := Frame{Type: "state", State: in.State, Message: in.Message, Title: in.Title}
			if f != last {
				if sendFrame(f) != nil {
					end()
					return
				}
				last = f
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}

	// Output.
	go func() {
		defer end()
		if err := announce(att); err != nil {
			return
		}
		go watchState()
		off := att.Offset
		for {
			p, err := s.read(off, stop)
			switch {
			case errors.Is(err, errGone):
				// Fell behind the kept bytes: the screen as it is, and on.
				a := s.position(-1)
				if announce(a) != nil {
					return
				}
				off = a.Offset
				continue
			case errors.Is(err, io.EOF):
				_ = sendFrame(Frame{Type: "exit", Code: s.Info().ExitCode})
				wmu.Lock()
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "exited"), time.Now().Add(time.Second))
				wmu.Unlock()
				return
			case err != nil:
				return
			}
			if send(websocket.BinaryMessage, p) != nil {
				return
			}
			off += int64(len(p))
		}
	}()

	// Keepalive: a client that stops answering pings is gone.
	_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(readTimeout)) })
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				wmu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				wmu.Unlock()
				if err != nil {
					end()
					return
				}
			}
		}
	}()

	// Input.
	ws.SetReadLimit(1 << 20)
	for {
		kind, p, err := ws.ReadMessage()
		if err != nil {
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
		if kind == websocket.BinaryMessage {
			_ = s.Input(p)
			continue
		}
		var f Frame
		if json.Unmarshal(p, &f) != nil {
			continue
		}
		switch f.Type {
		case "input":
			_ = s.Input([]byte(f.Data))
		case "resize":
			s.Resize(f.Cols, f.Rows)
		}
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}
