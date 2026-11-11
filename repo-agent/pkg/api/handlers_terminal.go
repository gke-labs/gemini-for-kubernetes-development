package api

import (
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// The ssh-over-exec terminal implementation that used to live here is
// retired: both the sandbox card and the Overseer tab now share the
// tmux-backed pods/exec transport in handlers_sandbox_terminal.go
// (keepalives, sessions that survive drops, reconnect-and-reattach).

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser clients (no Origin header)
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}
