package api

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

// The sandbox card's terminal: two layers instead of the overseer tab's
// three (ws → exec → sshd) — xterm.js ↔ WebSocket ↔ pods/exec running
// `tmux new -As board`. Stability comes from three things the old stack
// lacked: ws ping/pong keepalives, session state living in tmux (a
// dropped pipe loses nothing), and the frontend silently reconnecting
// into the same tmux session. Session-namespace only, like the card.

const (
	terminalPingInterval = 20 * time.Second
	terminalReadTimeout  = 70 * time.Second
	terminalSession      = "board"
)

// wsSizeQueue feeds xterm resize frames to the exec stream.
type wsSizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func (q *wsSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// wsWriter serializes exec output onto the websocket.
type wsWriter struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

type terminalFrame struct {
	T    string `json:"t"` // "i" stdin | "r" resize
	Data string `json:"d,omitempty"`
	Cols uint16 `json:"c,omitempty"`
	Rows uint16 `json:"r,omitempty"`
}

func (s *Server) sandboxTerminal(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")
	if !safeTaskName.MatchString(name) {
		c.JSON(400, gin.H{"error": "invalid sandbox name"})
		return
	}

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		klog.Errorf("terminal: websocket upgrade failed: %v", err)
		return
	}
	defer ws.Close()

	writeText := func(msg string) {
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte(msg))
	}

	podID, err := sandbox.FindSandboxPodInNamespace(c.Request.Context(), name, namespace)
	if err != nil || podID == nil {
		writeText("\r\nsandbox pod is not running (paused?) — wake it from the card first\r\n")
		return
	}

	// Detached from the HTTP request: the exec stream lives as long as
	// the websocket, and only as long.
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Request.Context()))
	defer cancel()

	stdinR, stdinW := io.Pipe()
	sizeQueue := &wsSizeQueue{ch: make(chan remotecommand.TerminalSize, 4)}
	out := &wsWriter{ws: ws}

	// Keepalive: ping on an interval, extend the read deadline on pong —
	// idle timeouts along the path were the old terminal's silent killer.
	_ = ws.SetReadDeadline(time.Now().Add(terminalReadTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(terminalReadTimeout))
	})
	go func() {
		ticker := time.NewTicker(terminalPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				out.mu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				out.mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Reader: browser frames → stdin / resize.
	go func() {
		defer cancel()
		defer stdinW.Close()
		defer close(sizeQueue.ch)
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			_ = ws.SetReadDeadline(time.Now().Add(terminalReadTimeout))
			var frame terminalFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			switch frame.T {
			case "i":
				if _, err := stdinW.Write([]byte(frame.Data)); err != nil {
					return
				}
			case "r":
				select {
				case sizeQueue.ch <- remotecommand.TerminalSize{Width: frame.Cols, Height: frame.Rows}:
				default:
				}
			}
		}
	}()

	// The session lives in tmux: this exec merely attaches, so a dropped
	// connection loses nothing and the next one resumes where you were.
	execErr := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command:     []string{"sh", "-c", "TERM=xterm-256color exec tmux new-session -A -s " + terminalSession},
		TTY:         true,
		StdinReader: stdinR,
		Stdout:      out,
		Stderr:      out,
		SizeQueue:   sizeQueue,
	})
	if execErr != nil && ctx.Err() == nil {
		klog.Infof("terminal: exec ended for %s/%s: %v", namespace, name, execErr)
		writeText("\r\n[connection to sandbox ended]\r\n")
	}
}
