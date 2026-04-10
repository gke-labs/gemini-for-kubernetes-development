package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/taskrunner"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

var (
	DevGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	IssueGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
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

	// Ensure cache and tmp directories exist on /workspaces
	// This is important for Go builds to avoid ephemeral storage exhaustion.
	dirs := []string{
		os.Getenv("GOCACHE"),
		os.Getenv("GOMODCACHE"),
		os.Getenv("TMPDIR"),
		os.Getenv("GOTMPDIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error(err, "failed to create directory", "path", dir)
		}
	}

	// Start periodic cleanup in background
	go startPeriodicCleanup(ctx)

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

	scriptPath := "/usr/local/bin/start-dockerd.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		log.Info("start-dockerd.sh not found, skipping dockerd startup")
		return nil
	}

	log.Info("Starting dockerd via script", "script", scriptPath)

	cmd := exec.CommandContext(ctx, scriptPath)

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
		return fmt.Errorf("failed to start dockerd script: %w", err)
	}

	return nil
}
