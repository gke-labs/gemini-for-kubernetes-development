package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	// "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// TerminalMessage represents a message from the client
type TerminalMessage struct {
	Type string `json:"type"` // "input" or "resize"
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

type pipeConn struct {
	r io.Reader
	w io.Writer
}

func (c *pipeConn) Read(b []byte) (n int, err error) { return c.r.Read(b) }
func (c *pipeConn) Write(b []byte) (n int, err error) { return c.w.Write(b) }
func (c *pipeConn) Close() error                     { return nil } // Pipes are closed separately
func (c *pipeConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }
func (c *pipeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }
func (c *pipeConn) SetDeadline(t time.Time) error    { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

func (s *Server) terminal(c *gin.Context) {
	// namespace := c.Param("namespace")
	sandboxName := c.Param("name")

	user := s.Auth.GetUserFromContext(c)
	if user == "" {
		c.String(http.StatusUnauthorized, "Unauthorized")
		return
	}
	// TODO: Verify user has access to this sandbox/namespace

	// Upgrade to WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		klog.Errorf("Failed to upgrade to websocket: %v", err)
		return
	}
	defer ws.Close()

	// Find the pod
	podID, err := sandbox.FindSandboxPod(c.Request.Context(), sandboxName)
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Failed to find sandbox pod: %v", err)))
		return
	}

	// Prepare pipes for SSH over kubectl exec
	// execOutR -> [backend reads SSH] -> [ssh client]
	// [ssh client] -> [backend writes SSH] -> execInW
	execInR, execInW := io.Pipe()
	execOutR, execOutW := io.Pipe()

	// Context for the exec command
	ctx := c.Request.Context()

	// Run kubectl exec in goroutine
	go func() {
		defer execInR.Close()
		defer execOutW.Close()

		opts := sandbox.ExecOptions{
			Command:     []string{sandbox.RepoSandboxBinary, "sshd"},
			StdinReader: execInR,
			Stdout:      execOutW,
			Stderr:      execOutW,
			TTY:         false, // SSH transport doesn't need TTY
		}
		
		klog.Infof("Starting sshd in pod %s", podID.Name)
		if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, opts); err != nil {
			klog.Errorf("ExecInPod failed: %v", err)
		}
		klog.Infof("ExecInPod finished for pod %s", podID.Name)
	}()

	// Connect SSH Client
	conn := &pipeConn{r: execOutR, w: execInW}
	
	// Establish SSH connection
	// We might need to wait a bit for the pod command to start?
	// NewClientConn should handle the handshake.
	
	sshClientConfig := &ssh.ClientConfig{
		User:            "root", // Repo-agent runs as root usually
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	cConn, chans, reqs, err := ssh.NewClientConn(conn, "sandbox", sshClientConfig)
	if err != nil {
		klog.Errorf("Failed to handshake ssh: %v", err)
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n*** SSH Handshake failed: %v ***\r\n", err)))
		return
	}
	defer cConn.Close()

	sshClient := ssh.NewClient(cConn, chans, reqs)
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		klog.Errorf("Failed to create session: %v", err)
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n*** Failed to create SSH session: %v ***\r\n", err)))
		return
	}
	defer session.Close()

	// Request PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	// Default size, will be updated by resize event
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		klog.Errorf("request for pseudo terminal failed: %v", err)
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n*** Request PTY failed: %v ***\r\n", err)))
		return
	}

	// Output pipe
	stdout, err := session.StdoutPipe()
	if err != nil {
		klog.Errorf("Unable to setup stdout for session: %v", err)
		return
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		klog.Errorf("Unable to setup stderr for session: %v", err)
		return
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		klog.Errorf("Unable to setup stdin for session: %v", err)
		return
	}

	// Start shell
	if err := session.Shell(); err != nil {
		klog.Errorf("failed to start shell: %v", err)
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n*** Failed to start shell: %v ***\r\n", err)))
		return
	}

	// Forward Output to WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if err := ws.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
					klog.Errorf("failed to write to websocket: %v", err)
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					klog.Errorf("stdout read error: %v", err)
				}
				break
			}
		}
	}()
	
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				if err := ws.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
					klog.Errorf("failed to write to websocket: %v", err)
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					klog.Errorf("stderr read error: %v", err)
				}
				break
			}
		}
	}()

	// Handle Input from WebSocket
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				klog.Errorf("error reading from websocket: %v", err)
			}
			break
		}

		var msg TerminalMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			klog.Errorf("invalid message format: %v", err)
			continue
		}

		switch msg.Type {
		case "input":
			if _, err := stdin.Write([]byte(msg.Data)); err != nil {
				klog.Errorf("failed to write to stdin: %v", err)
				return
			}
		case "resize":
			if err := session.WindowChange(msg.Rows, msg.Cols); err != nil {
				klog.Errorf("failed to change window size: %v", err)
			}
		}
	}
	
	klog.Info("Terminal session ended")
}
