package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/taskrunner"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

var (
	DevGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	IssueGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

type SandboxDaemonCommand struct {
	CodeServerCommand CodeServerCommand
	// Flattened fields from deleted SandboxCommand to keep Daemon logic working
	// We only need fields necessary for startup configuration if any
	// Actually daemon mostly relies on env vars and TaskRunner now.
	// But let's keep flags for compatibility or future use.
	IssueID string
}

func (c *SandboxDaemonCommand) InitDefaults() {
	c.CodeServerCommand.InitDefaults()
	if c.IssueID == "" {
		c.IssueID = os.Getenv("ISSUEID")
	}
}

func BuildSandboxDaemonCommand() *cobra.Command {
	daemonCmd := SandboxDaemonCommand{}
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the sandbox daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&daemonCmd.IssueID, "issue-id", os.Getenv("ISSUEID"), "Issue ID")

	return cmd
}

func (c *SandboxDaemonCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	var gvr schema.GroupVersionResource
	if c.IssueID != "" {
		gvr = IssueGVR
	} else {
		gvr = DevGVR
	}
	ao, err := agentoutput.New(gvr, "", "")
	if err != nil {
		log.Error(err, "failed to create k8s client: %w", err)
		return err
	}

	if err := c.startDockerd(ctx); err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		return fmt.Errorf("failed to start dockerd: %w", err)
	}

	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	// Start Agent Server (Log Serving)
	agentServer := agentserver.NewAgentServer()
	if err := agentServer.Start(); err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		log.Error(err, "failed to start agent server")
		return err
	}
	defer func() {
		if err := agentServer.Stop(); err != nil {
			log.Error(err, "failed to stop agent server")
		}
	}()

	// Start Task Runner (Process Tasks)
	tr, err := taskrunner.NewTaskRunner(ao)
	if err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		log.Error(err, "failed to create task runner")
		return err
	}

	// Run TaskRunner in background
	go tr.Run(ctx)

	_ = ao.SetAgentState(ctx, "Ready", "")

	log.Info("Sandbox Daemon started. Waiting for tasks...")

	return c.CodeServerCommand.Wait() // Wait for code server (or context cancel)
}

func (c *SandboxDaemonCommand) startDockerd(ctx context.Context) error {
	log := klog.FromContext(ctx)

	path, err := exec.LookPath("dockerd")
	if err != nil {
		return nil // Not installed, skip
	}

	log.Info("Preparing and starting dockerd")

	// 1. Mount tmpfs on /var/lib/docker if not already tmpfs
	// gVisor only supports tmpfs as an upper layer for overlay.
	out, err := exec.Command("stat", "-f", "-c", "%T", "/var/lib/docker").Output()
	if err != nil || strings.TrimSpace(string(out)) != "tmpfs" {
		log.Info("Mounting tmpfs on /var/lib/docker")
		_ = os.MkdirAll("/var/lib/docker", 0755)
		if err := exec.Command("mount", "-t", "tmpfs", "-o", "size=2G", "tmpfs", "/var/lib/docker").Run(); err != nil {
			log.Error(err, "failed to mount tmpfs on /var/lib/docker")
		}
	}

	// 2. Enable IP forwarding
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644); err != nil {
		log.Error(err, "failed to enable ip_forward")
	}

	// 3. Setup NAT rules
	// Find default route interface and its IP address
	devCmd := "ip route show default | sed 's/.*\\sdev\\s\\(\\S*\\)\\s.*$/\\1/'"
	if devOut, err := exec.Command("sh", "-c", devCmd).Output(); err == nil {
		dev := strings.TrimSpace(string(devOut))
		if dev != "" {
			addrCmd := fmt.Sprintf("ip addr show dev %s | grep -w inet | sed 's/^\\s*inet\\s\\(\\S*\\)\\/.*$/\\1/'", dev)
			if addrOut, err := exec.Command("sh", "-c", addrCmd).Output(); err == nil {
				addr := strings.TrimSpace(string(addrOut))
				if addr != "" {
					iptablesCmd := "iptables"
					if _, err := exec.LookPath("iptables-legacy"); err == nil {
						iptablesCmd = "iptables-legacy"
					}
					log.Info("Setting up iptables NAT rules", "dev", dev, "addr", addr, "cmd", iptablesCmd)
					_ = exec.Command(iptablesCmd, "-t", "nat", "-A", "POSTROUTING", "-o", dev, "-j", "SNAT", "--to-source", addr, "-p", "tcp").Run()
					_ = exec.Command(iptablesCmd, "-t", "nat", "-A", "POSTROUTING", "-o", dev, "-j", "SNAT", "--to-source", addr, "-p", "udp").Run()
				}
			}
		}
	}

	// 4. Start dockerd with flags to disable its own iptables management
	cmd := exec.CommandContext(ctx, path, "--iptables=false", "--ip6tables=false", "-D")

	// Redirect logs to /tmp/dockerd.log
	f, err := os.Create("/tmp/dockerd.log")
	if err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start dockerd: %w", err)
	}

	return nil
}
