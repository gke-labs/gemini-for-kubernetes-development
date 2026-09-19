package api

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// The ssh-over-exec terminal implementation that used to live here is
// retired: both the sandbox card and the Overseer tab now share the
// tmux-backed pods/exec transport in handlers_sandbox_terminal.go
// (keepalives, sessions that survive drops, reconnect-and-reattach).

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(_ *http.Request) bool {
		return true // Allow all origins for now
	},
}
