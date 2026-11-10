package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// The sandbox card's terminal on a daemon that serves terminal sessions
// (factory's termd, /v1/terminals): the program runs on a PTY the daemon
// owns, so it outlives every connection, and a browser that drops comes
// back at its byte offset. repo-agent only proxies: it starts (or finds)
// the session, then passes websocket frames through unchanged both ways,
// so the browser speaks the daemon's protocol - JSON control in text
// frames (attached, snapshot, exit; input, resize), output in binary.

// terminalCreate is termd's POST /v1/terminals.
type terminalCreate struct {
	ID      string `json:"id"`
	Program string `json:"program"`
	Resume  bool   `json:"resume,omitempty"`
}

// terminalAPIKeyHeader carries the engine's key on create, as on acpd's
// sessions: the daemon keeps none.
const terminalAPIKeyHeader = "X-Engine-Api-Key"

// terminalProgram is the termd program for a sandbox engine's CLI.
func terminalProgram(engine string) string {
	switch engine {
	case "claude":
		return "claude"
	case "antigravity":
		return "agy"
	default:
		return "gemini"
	}
}

// programEngine is the engine whose CLI a launcher program is, "" for
// none.
func programEngine(program string) string {
	switch program {
	case "gemini", "claude":
		return program
	case "agy":
		return "antigravity"
	}
	return ""
}

// sandboxTaskEngine is the engine the sandbox's tasks ran with; a sandbox
// stamped with none ran gemini.
func (s *Server) sandboxTaskEngine(ctx context.Context, namespace, name string) string {
	if sb, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{}); err == nil {
		return sandboxEngine(sb.GetAnnotations(), "gemini")
	}
	return "gemini"
}

// engineAPIKey is the engine's key from the namespace's factory-user
// secret - the identity the tasks run under - handed to the engine at
// launch, never stored on the pod. Without one, say tells the user the
// engine will ask them to sign in.
func (s *Server) engineAPIKey(ctx context.Context, namespace, engine string, say func(string)) string {
	keyName := "GEMINI_API_KEY" // gemini and antigravity
	if engine == "claude" {
		keyName = "ANTHROPIC_API_KEY"
	}
	key := ""
	if secret, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{}); err == nil {
		key = string(secret.Data[keyName])
	}
	if key == "" {
		say(fmt.Sprintf("\r\n(no %s in this namespace's factory-user secret — the agent will ask you to authenticate)\r\n", keyName))
	}
	return key
}

// daemonTerminal serves ws from the daemon at base (http://127.0.0.1:<port>,
// a port-forward): the board shell; with chatTask the CLI of the engine the
// sandbox's tasks ran, continuing its conversation; with program (gemini,
// claude, agy) that engine's CLI, the launcher's. offset is where the
// browser left off ("" for a new terminal).
func (s *Server) daemonTerminal(ctx context.Context, ws *websocket.Conn, base, namespace, name, chatTask, program, offset string) {
	writeText := func(msg string) {
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte(msg))
	}
	req := terminalCreate{ID: terminalSession, Program: "shell"}
	apiKey := ""
	switch {
	case program != "":
		apiKey = s.engineAPIKey(ctx, namespace, programEngine(program), writeText)
		req = terminalCreate{ID: "agent-" + program, Program: program, Resume: true}
	case chatTask != "":
		engine := s.sandboxTaskEngine(ctx, namespace, name)
		apiKey = s.engineAPIKey(ctx, namespace, engine, writeText)
		req = terminalCreate{ID: "chat-" + chatTask, Program: terminalProgram(engine), Resume: true}
	}
	if err := createTerminal(ctx, base, req, apiKey); err != nil {
		klog.Infof("terminal: starting %s in %s/%s: %v", req.ID, namespace, name, err)
		writeText("\r\n[could not start the terminal: " + err.Error() + "]\r\n")
		return
	}

	attach := "ws" + strings.TrimPrefix(base, "http") + "/v1/terminals/" + url.PathEscape(req.ID) + "/attach"
	if n, err := strconv.ParseInt(offset, 10, 64); err == nil && n >= 0 {
		attach += "?offset=" + strconv.FormatInt(n, 10)
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	daemon, _, err := websocket.DefaultDialer.DialContext(dctx, attach, nil)
	cancel()
	if err != nil {
		klog.Infof("terminal: attaching %s in %s/%s: %v", req.ID, namespace, name, err)
		writeText("\r\n[could not attach to the terminal]\r\n")
		return
	}
	defer daemon.Close()
	proxyTerminal(ctx, ws, daemon)
}

// createTerminal starts the session, or finds it running (termd's
// attach-or-create by id).
func createTerminal(ctx context.Context, base string, req terminalCreate, apiKey string) error {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/terminals", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		hreq.Header.Set(terminalAPIKeyHeader, apiKey)
	}
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s", resp.Status)
	}
	return nil
}

// proxyTerminal passes frames between the browser and the daemon until
// either side goes, keeping the browser's side alive with pings (the
// daemon pings its own side, which gorilla answers while reading).
func proxyTerminal(ctx context.Context, browser, daemon *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &wsWriter{ws: browser}

	_ = browser.SetReadDeadline(time.Now().Add(terminalReadTimeout))
	browser.SetPongHandler(func(string) error {
		return browser.SetReadDeadline(time.Now().Add(terminalReadTimeout))
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
				err := browser.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				out.mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Daemon → browser.
	go func() {
		defer cancel()
		for {
			kind, p, err := daemon.ReadMessage()
			if err != nil {
				return
			}
			out.mu.Lock()
			err = browser.WriteMessage(kind, p)
			out.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	// Browser → daemon. Only this goroutine writes to daemon (gorilla's
	// pong replies go through WriteControl, which may run alongside).
	go func() {
		defer cancel()
		for {
			kind, p, err := browser.ReadMessage()
			if err != nil {
				return
			}
			_ = browser.SetReadDeadline(time.Now().Add(terminalReadTimeout))
			if err := daemon.WriteMessage(kind, p); err != nil {
				return
			}
		}
	}()

	<-ctx.Done()
	_ = daemon.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}
