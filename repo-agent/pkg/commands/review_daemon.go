package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/taskrunner"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type ReviewDaemonCommand struct {
	ReviewCommand     ReviewCommand
	CodeServerCommand CodeServerCommand
}

func (c *ReviewDaemonCommand) InitDefaults() {
	c.ReviewCommand.InitDefaults()
	c.CodeServerCommand.InitDefaults()
}

func BuildReviewDaemonCommand() *cobra.Command {
	daemonCmd := ReviewDaemonCommand{}
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the review agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}
	// Flags are technically not needed for the daemon anymore as it uses TaskRunner,
	// but we keep them to avoid breaking changes if they are used by ReviewCommand init.
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	// ... other flags can remain or be cleaned up.

	return cmd
}

func (c *ReviewDaemonCommand) Run(ctx context.Context) error {
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

	ao, err := agentoutput.New(ReviewGVR, "", "")
	if err != nil {
		log.Error(err, "failed to create k8s client")
		return err
	}

	// 1. Start Code Server (Background)
	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	// 2. Start Agent Server (Log Serving)
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

	// 3. Start Task Runner (Process Tasks)
	tr, err := taskrunner.NewTaskRunner(ao)
	if err != nil {
		_ = ao.SetAgentState(ctx, "error", err.Error())
		log.Error(err, "failed to create task runner")
		return err
	}

	// Run TaskRunner in background
	go tr.Run(ctx)

	log.Info("Review Daemon started. Waiting for tasks...")
	_ = ao.SetAgentState(ctx, "Ready", "")

	// 4. Wait for Code Server (blocks until termination)
	return c.CodeServerCommand.Wait()
}
