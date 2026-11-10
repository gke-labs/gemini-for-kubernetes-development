package termd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/gorilla/websocket"
)

// client is one attach websocket, reading frames into what it saw.
type client struct {
	t      *testing.T
	ws     *websocket.Conn
	first  Frame
	out    bytes.Buffer
	frames []Frame
}

func dial(t *testing.T, srv *httptest.Server, id, offset string) *client {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminals/" + id + "/attach"
	if offset != "" {
		u += "?offset=" + offset
	}
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	c := &client{t: t, ws: ws}
	_, p, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("reading the attached frame: %v", err)
	}
	if err := json.Unmarshal(p, &c.first); err != nil || c.first.Type != "attached" {
		t.Fatalf("first frame %q, want attached", p)
	}
	return c
}

func (c *client) send(f Frame) {
	b, _ := json.Marshal(f)
	if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		c.t.Fatal(err)
	}
}

// until reads until cond holds of what the client saw, or fails.
func (c *client) until(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %s; output %q, frames %+v", what, tail(c.out.String()), c.frames)
		}
		_ = c.ws.SetReadDeadline(deadline)
		kind, p, err := c.ws.ReadMessage()
		if err != nil {
			c.t.Fatalf("waiting for %s: %v; output %q", what, err, tail(c.out.String()))
		}
		if kind == websocket.BinaryMessage {
			c.out.Write(p)
			continue
		}
		var f Frame
		_ = json.Unmarshal(p, &f)
		c.frames = append(c.frames, f)
	}
}

func (c *client) frame(kind string) (Frame, bool) {
	for _, f := range c.frames {
		if f.Type == kind {
			return f, true
		}
	}
	return Frame{}, false
}

func tail(s string) string {
	if len(s) > 400 {
		return "…" + s[len(s)-400:]
	}
	return s
}

func newServer(t *testing.T) (*Manager, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	m := NewManager(root)
	m.environ = func() []string { return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GITHUB_TOKEN=secret"} }
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(func() { srv.Close(); m.Close() })
	return m, srv
}

func create(t *testing.T, srv *httptest.Server, body string, wantStatus int) Info {
	t.Helper()
	resp, err := http.Post(srv.URL+"/terminals", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST /terminals %s: status %d, want %d", body, resp.StatusCode, wantStatus)
	}
	var in Info
	_ = json.NewDecoder(resp.Body).Decode(&in)
	return in
}

// A shell on a PTY the daemon owns: a client types into it, drops, and
// comes back at its offset with nothing repeated; a new client gets it
// all from byte 0; the program's end is an exit frame. Creating the same
// id again while it runs is the same session (tmux new -A).
func TestShellSessionResumesAtOffset(t *testing.T) {
	m, srv := newServer(t)
	in := create(t, srv, `{"id":"board","cols":80,"rows":24}`, http.StatusCreated)
	if in.ID != "board" || in.Program != "shell" || in.Exited {
		t.Fatalf("created %+v", in)
	}
	if again := create(t, srv, `{"id":"board"}`, http.StatusOK); again.Created != in.Created {
		t.Errorf("second create made a new session: %+v vs %+v", again, in)
	}

	c := dial(t, srv, "board", "")
	if !c.first.Reset || c.first.Offset != 0 {
		t.Errorf("new client: attached %+v, want reset from 0", c.first)
	}
	c.send(Frame{Type: "input", Data: "echo marker-$((40+2))\n"})
	c.until("the command's output", func() bool { return strings.Contains(c.out.String(), "marker-42") })
	// What the environment hands the shell: the terminal's, not the
	// daemon's credentials.
	c.send(Frame{Type: "input", Data: "echo term=$TERM tok=${GITHUB_TOKEN:-none}\n"})
	c.until("the environment", func() bool { return strings.Contains(c.out.String(), "term=xterm-256color tok=none") })
	seen := int64(c.out.Len())
	_ = c.ws.Close()

	// Back at its offset: no reset, and only what came after.
	r := dial(t, srv, "board", itoa(seen))
	if r.first.Reset || r.first.Offset != seen {
		t.Errorf("resumed client: attached %+v, want offset %d without reset", r.first, seen)
	}
	r.send(Frame{Type: "input", Data: "echo after-resume\n"})
	r.until("output after resuming", func() bool { return strings.Contains(r.out.String(), "after-resume") })
	if strings.Contains(r.out.String(), "marker-42") {
		t.Errorf("resumed client got output it had already seen: %q", r.out.String())
	}

	// A second client at the same time, from scratch: everything.
	n := dial(t, srv, "board", "")
	n.until("the whole session", func() bool { return strings.Contains(n.out.String(), "after-resume") })
	if !strings.Contains(n.out.String(), "marker-42") {
		t.Errorf("new client missed the session's start: %q", tail(n.out.String()))
	}
	if got := m.List(); len(got) != 1 || got[0].Clients != 2 {
		t.Errorf("listing %+v, want one session with two clients", got)
	}

	r.send(Frame{Type: "input", Data: "exit 3\n"})
	r.until("the exit frame", func() bool { _, ok := r.frame("exit"); return ok })
	if f, _ := r.frame("exit"); f.Code != 3 {
		t.Errorf("exit frame %+v, want code 3", f)
	}
	// Ended, it is replaced by the next create of its id.
	if again := create(t, srv, `{"id":"board"}`, http.StatusCreated); again.Exited {
		t.Errorf("create after exit: %+v", again)
	}
}

// A client whose bytes are no longer kept - new to a long session, or
// too far behind - gets the screen as it is now, from the emulator.
func TestSnapshotWhenOutputIsNoLongerKept(t *testing.T) {
	defer func(k int) { keep = k }(keep)
	keep = 16 << 10

	_, srv := newServer(t)
	create(t, srv, `{"id":"long","cols":80,"rows":24}`, http.StatusCreated)
	c := dial(t, srv, "long", "")
	c.send(Frame{Type: "input", Data: "i=0; while [ $i -lt 3000 ]; do echo line-$i-padding-padding; i=$((i+1)); done; printf '\\033[2J\\033[HSCREEN-%s' NOW; echo\n"})
	// Not the echoed command line, which has SCREEN-%s.
	c.until("the final screen", func() bool { return strings.Contains(c.out.String(), "\x1b[HSCREEN-NOW") })
	_ = c.ws.Close()

	for _, offset := range []string{"", "0"} {
		s := dial(t, srv, "long", offset)
		if !s.first.Reset || s.first.Offset == 0 {
			t.Errorf("offset %q: attached %+v, want a reset past byte 0", offset, s.first)
		}
		s.until("the snapshot", func() bool { _, ok := s.frame("snapshot"); return ok })
		snap, _ := s.frame("snapshot")
		// Drawn on a fresh terminal of the same size, it is the screen:
		// SCREEN-NOW on the top row, the prompt below, the cursor after it.
		screen := vt.NewEmulator(80, 24)
		_, _ = screen.Write([]byte(snap.Data))
		lines := strings.Split(screen.String(), "\n")
		pos := screen.CursorPosition()
		if lines[0] != "SCREEN-NOW" || len(lines) < 2 || !strings.HasSuffix(strings.TrimSpace(lines[1]), "$") || pos.Y != 1 || pos.X != len(strings.TrimRight(lines[1], " "))+1 {
			t.Errorf("offset %q (%s): snapshot draws %q with the cursor at %v; snapshot %q", offset, EmulatorName, screen.String(), pos, tail(snap.Data))
		}
		_ = s.ws.Close()
	}
}

// What can be created: a known program, an absolute existing directory,
// a valid id; the engine's key goes in the engine's own variable.
func TestSpec(t *testing.T) {
	m, _ := newServer(t)
	for _, tc := range []struct {
		req     CreateRequest
		wantErr string
	}{
		{CreateRequest{Program: "vim"}, "unknown program"},
		{CreateRequest{CWD: "relative"}, "not absolute"},
		{CreateRequest{CWD: "/does/not/exist"}, "not a directory"},
	} {
		if _, err := m.spec(tc.req, ""); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("spec(%+v) = %v, want %q", tc.req, err, tc.wantErr)
		}
	}
	if _, _, err := m.Create(CreateRequest{ID: "../x"}, ""); err == nil {
		t.Error("an id with a slash was accepted")
	}
	for program, want := range map[string]string{"gemini": "GEMINI_API_KEY=k", "agy": "GEMINI_API_KEY=k", "claude": "ANTHROPIC_API_KEY=k"} {
		spec, err := m.spec(CreateRequest{Program: program, Resume: true}, "k")
		if err != nil {
			t.Fatal(err)
		}
		if !contains(spec.Env, want) || contains(spec.Env, "GITHUB_TOKEN=secret") {
			t.Errorf("%s env %v, want %s and no GITHUB_TOKEN", program, spec.Env, want)
		}
		if script := spec.Argv[len(spec.Argv)-1]; !strings.Contains(script, "|| exec "+program) {
			t.Errorf("%s resume script %q, want a fallback to a new conversation", program, script)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// A program that asks its terminal something while no client is attached
// gets its answer from the emulator: it does not hang waiting for one.
func TestQueriesAnsweredWithNoClient(t *testing.T) {
	script := `stty -echo -icanon min 1; printf '\033[6n'; r=$(dd bs=1 count=6 2>/dev/null | tr -d '\033'); printf 'GOT[%s]\n' "$r"; sleep 5`
	s, err := start("q", Spec{Argv: []string{"/bin/sh", "-c", script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Kill(time.Second); s.release() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		out := string(s.buf)
		s.mu.Unlock()
		if strings.Contains(out, "GOT[[1;1R]") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: no answer to the cursor position query; output %q", EmulatorName, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
