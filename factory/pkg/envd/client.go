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

	"connectrpc.com/connect"
	process "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd/spec/process"
	processconnect "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd/spec/process/processconnect"
)

type Client struct {
	baseURL       string
	processClient processconnect.ProcessClient
	closePF       func()
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
	}, nil
}

func (c *Client) Close() {
	if c.closePF != nil {
		c.closePF()
	}
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
