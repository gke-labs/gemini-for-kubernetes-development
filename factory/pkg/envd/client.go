package envd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	process "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd/spec/process"
	processconnect "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd/spec/process/processconnect"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
	"k8s.io/klog/v2"
)

type Client struct {
	baseURL       string
	processClient processconnect.ProcessClient
	closePF       func()

	// For reconnection
	namespace   string
	sandboxName string
	localPort   int
	pfCmd       *exec.Cmd
}

func (c *Client) WriteFile(ctx context.Context, destPath string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/files?path=%s", c.baseURL, url.QueryEscape(destPath)), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (%s): %s", resp.Status, string(body))
	}
	return nil
}

func GetSandboxPodName(ctx context.Context, namespace, sandboxName string) (string, error) {
	// Verify that the sandbox resource actually exists in Kubernetes first
	// to avoid waiting indefinitely if the name is mistyped.
	checkCmd := exec.CommandContext(ctx, "kubectl", "get", "sandbox", sandboxName, "-n", namespace)
	if err := checkCmd.Run(); err != nil {
		return "", fmt.Errorf("sandbox '%s' does not exist in namespace '%s'", sandboxName, namespace)
	}

	for i := 0; i < 120; i++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		out, err := exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", fmt.Sprintf("sandbox=%s", sandboxName), "-n", namespace, "-o", "json").Output()
		if err == nil {
			var podList struct {
				Items []struct {
					Metadata struct {
						Name              string  `json:"name"`
						DeletionTimestamp *string `json:"deletionTimestamp"`
					} `json:"metadata"`
					Status struct {
						Phase      string `json:"phase"`
						Conditions []struct {
							Type   string `json:"type"`
							Status string `json:"status"`
						} `json:"conditions"`
					} `json:"status"`
				} `json:"items"`
			}
			if err := json.Unmarshal(out, &podList); err == nil {
				hasTerminating := false
				activePodName := ""

				for _, pod := range podList.Items {
					if pod.Metadata.DeletionTimestamp != nil {
						hasTerminating = true
					} else {
						if pod.Status.Phase == "Running" {
							for _, cond := range pod.Status.Conditions {
								if cond.Type == "Ready" && cond.Status == "True" {
									activePodName = pod.Metadata.Name
								}
							}
						}
					}
				}

				if !hasTerminating && activePodName != "" {
					return activePodName, nil
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("timed out waiting for sandbox pod %s to become ready", sandboxName)
}

func Connect(ctx context.Context, namespace, sandboxName string) (*Client, error) {
	serviceName := sandboxName + "-lb"

	fmt.Printf("Waiting for sandbox pod %s to become ready (and any terminating pods to exit)...\n", sandboxName)
	_, err := GetSandboxPodName(ctx, namespace, sandboxName)
	if err != nil {
		return nil, err
	}

	// If running inside Kubernetes cluster, connect directly to the service DNS
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		baseURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:49983", serviceName, namespace)
		fmt.Printf("Running inside Kubernetes cluster. Connecting directly to service: %s\n", baseURL)

		ready := false
		for i := 0; i < 40; i++ {
			time.Sleep(500 * time.Millisecond)
			resp, err := http.Get(baseURL + "/health")
			if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				resp.Body.Close()
				ready = true
				break
			}
		}
		if !ready {
			return nil, fmt.Errorf("timed out connecting directly to envd service %s", serviceName)
		}

		processClient := processconnect.NewProcessClient(http.DefaultClient, baseURL)

		return &Client{
			baseURL:       baseURL,
			processClient: processClient,
			closePF:       func() {},
			namespace:     namespace,
			sandboxName:   sandboxName,
		}, nil
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("finding free port: %w", err)
	}
	localPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	pfCmd := exec.CommandContext(ctx, "kubectl", "port-forward", fmt.Sprintf("svc/%s", serviceName), fmt.Sprintf("%d:49983", localPort), "-n", namespace)
	if err := pfCmd.Start(); err != nil {
		return nil, fmt.Errorf("starting port-forward: %w", err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", localPort)

	closeFunc := func() {
		if pfCmd.Process != nil {
			_ = pfCmd.Process.Kill()
		}
	}

	ready := false
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		resp, err := http.Get(baseURL + "/health")
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			ready = true
			break
		}
	}
	if !ready {
		closeFunc()
		return nil, fmt.Errorf("timed out connecting to envd service %s via port-forward", serviceName)
	}

	processClient := processconnect.NewProcessClient(http.DefaultClient, baseURL)

	return &Client{
		baseURL:       baseURL,
		processClient: processClient,
		closePF:       closeFunc,
		namespace:     namespace,
		sandboxName:   sandboxName,
		localPort:     localPort,
		pfCmd:         pfCmd,
	}, nil
}

func (c *Client) Close() {
	if c.closePF != nil {
		c.closePF()
	}
	if c.pfCmd != nil && c.pfCmd.Process != nil {
		_ = c.pfCmd.Process.Kill()
	}
}

func (c *Client) ReconnectPortForward(ctx context.Context) error {
	if c.pfCmd == nil {
		return nil
	}

	// First, check if the connection is already healthy
	resp, err := http.Get(c.baseURL + "/health")
	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body.Close()
		return nil // Already healthy
	}

	klog.Infof("Re-establishing port-forward to service for sandbox %s...", c.sandboxName)

	if c.pfCmd.Process != nil {
		_ = c.pfCmd.Process.Kill()
		_ = c.pfCmd.Wait()
	}

	serviceName := c.sandboxName + "-lb"
	c.pfCmd = exec.CommandContext(context.Background(), "kubectl", "port-forward", fmt.Sprintf("svc/%s", serviceName), fmt.Sprintf("%d:49983", c.localPort), "-n", c.namespace)
	if err := c.pfCmd.Start(); err != nil {
		return fmt.Errorf("restarting port-forward: %w", err)
	}

	ready := false
	for i := 0; i < 40; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		time.Sleep(500 * time.Millisecond)
		resp, err := http.Get(c.baseURL + "/health")
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			ready = true
			break
		}
	}
	if !ready {
		return fmt.Errorf("timed out waiting for restarted port-forward to become ready")
	}

	klog.Infof("Port-forward successfully re-established on port %d", c.localPort)
	return nil
}

func (c *Client) Exec(ctx context.Context, cmdStr, cwd string, envs map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := connect.NewRequest(&process.StartRequest{
		Process: &process.ProcessConfig{
			Cmd:  "sh",
			Args: []string{"-c", cmdStr},
			Envs: envs,
			Cwd:  &cwd,
		},
	})

	stream, err := c.processClient.Start(ctx, req)
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}
	defer stream.Close()

	var pid uint32
	pidFound := make(chan struct{})
	once := false

	if stdin != nil {
		go func() {
			<-pidFound
			buf := make([]byte, 1024)
			for {
				n, err := stdin.Read(buf)
				if n > 0 {
					inputReq := connect.NewRequest(&process.SendInputRequest{
						Process: &process.ProcessSelector{
							Selector: &process.ProcessSelector_Pid{
								Pid: pid,
							},
						},
						Input: &process.ProcessInput{
							Input: &process.ProcessInput_Stdin{
								Stdin: buf[:n],
							},
						},
					})
					_, _ = c.processClient.SendInput(ctx, inputReq)
				}
				if err != nil {
					if err == io.EOF {
						closeReq := connect.NewRequest(&process.CloseStdinRequest{
							Process: &process.ProcessSelector{
								Selector: &process.ProcessSelector_Pid{
									Pid: pid,
								},
							},
						})
						_, _ = c.processClient.CloseStdin(ctx, closeReq)
					}
					break
				}
			}
		}()
	}

	for stream.Receive() {
		msg := stream.Msg()
		if msg.Event != nil {
			switch e := msg.Event.Event.(type) {
			case *process.ProcessEvent_Start:
				if !once {
					pid = e.Start.Pid
					close(pidFound)
					once = true
				}
			case *process.ProcessEvent_Data:
				if len(e.Data.GetStdout()) > 0 && stdout != nil {
					_, _ = stdout.Write(e.Data.GetStdout())
				}
				if len(e.Data.GetStderr()) > 0 && stderr != nil {
					_, _ = stderr.Write(e.Data.GetStderr())
				}
			}
		}
	}
	return stream.Err()
}

func (c *Client) RunTask(ctx context.Context, cmdStr string, envs map[string]string) error {
	return c.Exec(ctx, cmdStr, "/workspaces", envs, nil, os.Stdout, os.Stderr)
}

func (c *Client) RunTaskResilient(ctx context.Context, cmdStr string, envs map[string]string, taskDir string, detached bool) error {
	pidFile := fmt.Sprintf("%s/pid", taskDir)
	logFile := fmt.Sprintf("%s/execution.log", taskDir)
	exitCodeFile := fmt.Sprintf("%s/exit_code", taskDir)

	// Ensure the task directory exists inside the pod before writing to it
	if err := c.Exec(ctx, fmt.Sprintf("mkdir -p %s", taskDir), "/workspaces", nil, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to create task directory in pod: %w", err)
	}

	// 1. Launch the command in the background using nohup
	detachedCmd := fmt.Sprintf("nohup sh -c \"echo \\$\\$ > %s; %s > %s 2>&1; echo \\$? > %s\" >/dev/null 2>&1 &", pidFile, cmdStr, logFile, exitCodeFile)

	klog.Infof("Launching task in background of sandbox pod (Task directory: %s)...", taskDir)
	if err := c.Exec(ctx, detachedCmd, "/workspaces", envs, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to launch background task: %w", err)
	}

	if detached {
		fmt.Printf("Task launched in detached mode. Task directory: %s\n", taskDir)
		return nil
	}

	// 2. Tailing & status loop
	var offset int64

	// Set up signal channel for Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Context for active cancellation
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Separate goroutine to monitor Ctrl+C
	go func() {
		select {
		case <-sigChan:
			fmt.Printf("\nInterrupt received. Aborting task in sandbox pod...\n")
			cancel()
		case <-loopCtx.Done():
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-loopCtx.Done():
			// The context was canceled (either Ctrl+C, timeout, or external cancellation).
			// We must kill the process in the pod before exiting!
			// We use context.Background() since loopCtx is canceled.
			killCtx, killCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer killCancel()

			fmt.Printf("Terminating process in pod...\n")
			killCmd := fmt.Sprintf("if [ -f %s ]; then pids=\"$(cat %s) $(pgrep -P $(cat %s) 2>/dev/null)\"; kill $pids 2>/dev/null || true; fi", pidFile, pidFile, pidFile)
			_ = c.Exec(killCtx, killCmd, "/workspaces", nil, nil, nil, nil)
			return loopCtx.Err()

		case <-ticker.C:
			// Read new logs
			// We run 'tail -c +<offset>' to read log delta.
			var logBuf bytes.Buffer
			tailCmd := fmt.Sprintf("if [ -f %s ]; then tail -c +%d %s; fi", logFile, offset+1, logFile)

			if err := c.Exec(loopCtx, tailCmd, "/workspaces", nil, nil, &logBuf, nil); err == nil {
				newData := logBuf.Bytes()
				if len(newData) > 0 {
					_, _ = os.Stdout.Write(newData)
					offset += int64(len(newData))

					if geminitokens.ContainsQuotaError(newData) {
						if key := envs["GEMINI_API_KEY"]; key != "" {
							if err := geminitokens.AddQuotaExceededKey(key, 4*time.Hour); err != nil {
								klog.Errorf("Failed to mark key as quota exceeded: %v", err)
							}
						}
					}
				}
			} else {
				klog.Warningf("Log streaming connection flaked: %v. Reconnecting...", err)
				if reconnectErr := c.ReconnectPortForward(loopCtx); reconnectErr != nil {
					klog.Errorf("Failed to reconnect port-forward: %v", reconnectErr)
				}
			}

			// Check if exit code file exists
			var exitBuf bytes.Buffer
			checkCmd := fmt.Sprintf("if [ -f %s ]; then cat %s; fi", exitCodeFile, exitCodeFile)
			if err := c.Exec(loopCtx, checkCmd, "/workspaces", nil, nil, &exitBuf, nil); err == nil {
				exitStr := strings.TrimSpace(exitBuf.String())
				if exitStr != "" {
					// Process completed!
					// Do one final tail to flush any remaining log lines.
					var finalBuf bytes.Buffer
					finalTailCmd := fmt.Sprintf("if [ -f %s ]; then tail -c +%d %s; fi", logFile, offset+1, logFile)
					if err := c.Exec(loopCtx, finalTailCmd, "/workspaces", nil, nil, &finalBuf, nil); err == nil {
						newData := finalBuf.Bytes()
						if len(newData) > 0 {
							_, _ = os.Stdout.Write(newData)
						}
					}

					// Parse exit code
					code, err := strconv.Atoi(exitStr)
					if err != nil {
						return fmt.Errorf("invalid exit code '%s': %w", exitStr, err)
					}
					if code != 0 {
						return fmt.Errorf("task failed with exit code %d", code)
					}
					return nil
				}
			} else {
				klog.Warningf("Status check connection flaked: %v. Reconnecting...", err)
				if reconnectErr := c.ReconnectPortForward(loopCtx); reconnectErr != nil {
					klog.Errorf("Failed to reconnect port-forward: %v", reconnectErr)
				}
			}
		}
	}
}
