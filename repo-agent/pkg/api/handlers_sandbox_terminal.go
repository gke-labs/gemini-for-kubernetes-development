package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
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

// Chat mode (`?chat=<taskType>`): instead of a shell, the terminal drops
// straight into `gemini --resume latest` — continuing the conversation a
// prior factory task left behind. Sessions are keyed by HOME + cwd; every
// factory task type now runs under the workspace PVC's home, so resumed
// conversations survive pod restarts and pauses. The map is the chat
// whitelist, and the wire contract with the factory task scripts — if a
// task's HOME moves there, it moves here. First use case: continue a plan.
var chatHomeByTask = map[string]string{
	"plan":    "/workspaces/.home",
	"triage":  "/workspaces/.home",
	"review":  "/workspaces/.home",
	"fix":     "/workspaces/.home",
	"agent":   "/workspaces/.home",
	"explore": "/workspaces/.home",
}

// shellSingleQuote makes s safe inside single quotes for sh.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var trailingIssueRE = regexp.MustCompile(`-(\d+)$`)

// chatOrientation is the first message of a freshly created chat session
// (tmux -A: reattaches and second tabs skip it). It closes the
// informational gap in a resumed conversation: the agent never wrote the
// artifact file — the task script extracted its output there — so without
// being told, it cannot know where the plan lives or that editing it is
// how changes reach the board and the eventual fix.
func chatOrientation(taskType, sandboxName string) string {
	if taskType == "explore" {
		return "You are reconnected to the exploration session for this repository. " +
			"This message is informational only — it is not a task and asks for no work. " +
			"Your understanding docs live under docs-exploration/ and the contract is " +
			"docs-exploration/SKILL.md — follow it. As questions get answered, distill the " +
			"answers into the relevant docs. Do not modify any code outside docs-exploration/. " +
			"When the user asks you to save, commit docs-exploration and push to origin " +
			"exploration/notes. Acknowledge in one sentence and wait for the user."
	}
	if taskType != "plan" {
		return ""
	}
	m := trailingIssueRE.FindStringSubmatch(sandboxName)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("You are reconnected to the planning conversation for issue #%s. "+
		"This message is informational only — it is not a task and asks for no work. "+
		"The current plan lives at /workspaces/plan-issue-%s.md; the board displays that file and "+
		"it is executed only after the user approves it on the board. If the user asks you to change "+
		"the plan, edit that file directly so it always holds the complete, current plan. "+
		"Do not implement the plan and do not modify any other files. "+
		"Read the plan file to refresh your context, acknowledge in one sentence, and wait for the "+
		"user to tell you what to do next.", m[1], m[1])
}

// chatCommand builds the tmux attach for a resumed agent conversation.
// Each task type gets its own tmux session (chat-plan, …) so it coexists
// with the "board" shell; -A means a second tab joins the same chat. The
// repo checkout is found in the pod (the one /workspaces/*/.git), not
// trusted from the client.
func chatCommand(taskType, home, engine, apiKey, orientation string) string {
	prep := ""
	var resume, keyExport string
	switch engine {
	case "claude":
		// Sessions are engine-private: a claude task's conversation can
		// only be resumed by claude. --continue = latest session for the
		// cwd; the orientation rides as the positional initial prompt.
		// Task runs use -p (print mode), which skips Claude Code's
		// first-run wizard — so an interactive chat is the first launch
		// that would trigger it: theme, API-key confirmation, security
		// notes, folder trust. All four answers live in ~/.claude.json
		// (on the PVC); seed them so the user lands in the conversation,
		// not the wizard. Merge-with-setdefault keeps any answers the
		// user already gave; on failure the wizard appears, nothing
		// breaks. The key approval is recorded as its last 20 chars —
		// Claude Code's own convention for customApiKeyResponses.
		keyExport = "export ANTHROPIC_API_KEY=" + shellSingleQuote(apiKey)
		prep = `python3 -c 'import json,os,sys
p=os.path.join(os.environ["HOME"],".claude.json")
d={}
try:
    d=json.load(open(p))
except Exception:
    pass
d.setdefault("theme","dark")
d["hasCompletedOnboarding"]=True
key=os.environ.get("ANTHROPIC_API_KEY","")
if key:
    r=d.setdefault("customApiKeyResponses",{})
    a=r.setdefault("approved",[])
    if key[-20:] not in a:
        a.append(key[-20:])
    r.setdefault("rejected",[])
proj=d.setdefault("projects",{}).setdefault(sys.argv[1],{})
proj["hasTrustDialogAccepted"]=True
proj.setdefault("hasCompletedProjectOnboarding",True)
json.dump(d,open(p,"w"),indent=2)' "$PWD" 2>/dev/null; `
		// Interactive `claude --continue` filters out print-mode sessions
		// (observed on 2.1.278: -p --continue finds the task's session,
		// interactive --continue says "No conversation found to continue")
		// — but an explicit --resume <id> loads it fine. Resolve the
		// newest session for this project dir (cwd escaped the way Claude
		// Code does: non-alphanumerics → '-') and resume it by id; with
		// no session at all, open a fresh chat without the "reconnected"
		// orientation, which would be a lie.
		orient := ""
		if orientation != "" {
			orient = " " + shellSingleQuote(orientation)
		}
		resume = `proj="$HOME/.claude/projects/$(printf %s "$PWD" | tr -c "a-zA-Z0-9" "-")"; ` +
			`sid=$(ls -t "$proj"/*.jsonl 2>/dev/null | head -1); ` +
			`if [ -n "$sid" ]; then exec claude --resume "$(basename "$sid" .jsonl)"` + orient + `; else exec claude; fi`
	default:
		// Trust like the task runs do (no interactive prompt), and widen
		// the workspace to /workspaces: artifact files (plan-issue-N.md)
		// live one level above the repo checkout, deliberately outside
		// git clean's reach — without the extra root, write_file could
		// not touch them. --skip-trust covers only the cwd workspace, so
		// the /workspaces root is seeded into gemini's trust store
		// (~/.gemini/trustedFolders.json — task scripts never touch it);
		// on any failure the prompt appears, nothing breaks. The /root
		// rescue is the pre-HOME-unification transition shim (-p keeps
		// mtimes so a stale /root session never masquerades as latest).
		keyExport = "export GEMINI_API_KEY=" + shellSingleQuote(apiKey) + "; export GEMINI_CLI_TRUST_WORKSPACE=true"
		prep = `if [ -d /root/.gemini/tmp ] && [ "$HOME" != /root ]; then mkdir -p "$HOME/.gemini/tmp" && cp -Rnp /root/.gemini/tmp/. "$HOME/.gemini/tmp/" 2>/dev/null; fi; ` +
			`python3 -c 'import json,os,sys; p=os.path.join(os.environ["HOME"],".gemini","trustedFolders.json"); os.makedirs(os.path.dirname(p),exist_ok=True); d=json.load(open(p)) if os.path.exists(p) else {}; [d.setdefault(f,"TRUST_FOLDER") for f in sys.argv[1:]]; json.dump(d,open(p,"w"),indent=2)' /workspaces "$PWD" 2>/dev/null; `
		resume = "exec gemini --skip-trust --include-directories /workspaces --resume latest"
		if orientation != "" {
			resume += " -i " + shellSingleQuote(orientation)
		}
	}
	// prep and resume are ARGUMENTS, never part of the format string: a
	// literal %s inside them (the claude resolver's `printf %s`) would be
	// eaten as a Sprintf verb and emit %!s(MISSING) — shell garbage that
	// exits instantly ([exited] loop on every attach).
	inner := fmt.Sprintf(
		"export HOME=%s; %s; "+
			"d=$(ls -d /workspaces/*/.git 2>/dev/null | head -1); "+
			`cd "${d%%/.git}" 2>/dev/null || cd /workspaces; `+
			"%s%s",
		home, keyExport, prep, resume)
	return "TERM=xterm-256color exec tmux new-session -A -s chat-" + taskType + " " + shellSingleQuote(inner)
}

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
	Type string `json:"type"` // "input" | "resize"
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// sandboxTerminal serves the sandbox card (session namespace only).
func (s *Server) sandboxTerminal(c *gin.Context) {
	s.streamSandboxTerminal(c, s.Auth.GetNamespaceFromContext(c), c.Param("name"))
}

// overseerTerminal serves the Overseer tab's existing route, which names
// the namespace explicitly (admin view across sandbox namespaces) — same
// authz as the route always had, new transport underneath.
func (s *Server) overseerTerminal(c *gin.Context) {
	s.streamSandboxTerminal(c, c.Param("namespace"), c.Param("name"))
}

func (s *Server) streamSandboxTerminal(c *gin.Context, namespace, name string) {
	if !safeTaskName.MatchString(name) || !safeTaskName.MatchString(namespace) {
		c.JSON(400, gin.H{"error": "invalid sandbox name"})
		return
	}
	chatTask := c.Query("chat")
	if chatTask != "" {
		if _, ok := chatHomeByTask[chatTask]; !ok {
			c.JSON(400, gin.H{"error": "unknown chat task type"})
			return
		}
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

	// A pod mid-boot (ContainerCreating, image pull) accepts no exec:
	// say so instead of a bare "connection ended" loop — the frontend's
	// backoff reconnect converges the moment the container is ready.
	if pod, perr := s.K8sManager.Clientset.CoreV1().Pods(podID.Namespace).Get(c.Request.Context(), podID.Name, v1.GetOptions{}); perr == nil {
		ready := false
		for _, cond := range pod.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				ready = true
			}
		}
		if !ready {
			writeText(fmt.Sprintf("\r\nsandbox pod is starting (%s) — reconnecting until it's ready…\r\n", pod.Status.Phase))
			return
		}
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
			switch frame.Type {
			case "input":
				if _, err := stdinW.Write([]byte(frame.Data)); err != nil {
					return
				}
			case "resize":
				select {
				case sizeQueue.ch <- remotecommand.TerminalSize{Width: frame.Cols, Height: frame.Rows}:
				default:
				}
			}
		}
	}()

	command := "TERM=xterm-256color exec tmux new-session -A -s " + terminalSession
	if chatTask != "" {
		// Sessions are engine-private, so the chat must attach with the
		// engine that launched into this sandbox (stamped by the
		// controller at launch); pre-stamp sandboxes default to gemini.
		engine := "gemini"
		if sb, serr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{}); serr == nil {
			if e := sb.GetAnnotations()["board.gemini.google.com/engine"]; e != "" {
				engine = e
			}
		}
		// The engine's key rides in from the factory-user secret (the
		// same identity the task ran under) — never stored on the pod.
		keyName := "GEMINI_API_KEY"
		if engine == "claude" {
			keyName = "ANTHROPIC_API_KEY"
		}
		apiKey := ""
		if secret, serr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{}); serr == nil {
			apiKey = string(secret.Data[keyName])
		}
		if apiKey == "" {
			writeText(fmt.Sprintf("\r\n(no %s in this namespace's factory-user secret — the agent will ask you to authenticate)\r\n", keyName))
		}
		command = chatCommand(chatTask, chatHomeByTask[chatTask], engine, apiKey, chatOrientation(chatTask, name))
	}

	// The session lives in tmux: this exec merely attaches, so a dropped
	// connection loses nothing and the next one resumes where you were.
	execErr := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command:     []string{"sh", "-c", command},
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
