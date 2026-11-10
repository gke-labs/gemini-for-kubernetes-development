package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeTermd is a daemon's /v1/terminals: it records the create and the
// attach's offset, greets with an attached frame and output, and echoes
// every frame it is sent back as output.
type fakeTermd struct {
	mu      sync.Mutex
	created terminalCreate
	offset  string
	path    string
}

func (f *fakeTermd) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/terminals", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /v1/terminals/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.offset, f.path = r.URL.Query().Get("offset"), r.URL.Path
		f.mu.Unlock()
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"attached","offset":7}`))
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte("prompt$ "))
		for {
			kind, p, err := ws.ReadMessage()
			if err != nil {
				return
			}
			_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte("echo:"), p...))
			_ = kind
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The board shell on a daemon with terminals: repo-agent starts the
// "board" session, attaches at the browser's offset, and passes frames
// through unchanged - the daemon's control frames as text, output as
// binary, the browser's input on to the daemon.
func TestDaemonTerminalProxiesFramesAndOffset(t *testing.T) {
	f := &fakeTermd{}
	daemon := f.server(t)
	s := &Server{}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		s.daemonTerminal(context.Background(), ws, daemon.URL, "ns", "sb", "", r.URL.Query().Get("offset"))
	}))
	t.Cleanup(front.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(front.URL, "http")+"/?offset=1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))

	read := func() (int, string) {
		t.Helper()
		kind, p, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		return kind, string(p)
	}
	if kind, p := read(); kind != websocket.TextMessage || !strings.Contains(p, `"attached"`) {
		t.Errorf("first frame %d %q, want the daemon's attached frame as text", kind, p)
	}
	if kind, p := read(); kind != websocket.BinaryMessage || p != "prompt$ " {
		t.Errorf("second frame %d %q, want the output as binary", kind, p)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","data":"ls\r"}`)); err != nil {
		t.Fatal(err)
	}
	if _, p := read(); p != `echo:{"type":"input","data":"ls\r"}` {
		t.Errorf("echo %q, want the browser's frame passed through", p)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != (terminalCreate{ID: "board", Program: "shell"}) {
		t.Errorf("created %+v, want the board shell", f.created)
	}
	if f.offset != "1234" || f.path != "/v1/terminals/board/attach" {
		t.Errorf("attached at %s?offset=%s, want board at 1234", f.path, f.offset)
	}
}

func TestTerminalProgram(t *testing.T) {
	for engine, want := range map[string]string{"gemini": "gemini", "claude": "claude", "antigravity": "agy", "": "gemini"} {
		if got := terminalProgram(engine); got != want {
			t.Errorf("terminalProgram(%q) = %q, want %q", engine, got, want)
		}
	}
}
