package commands

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/spf13/cobra"
)

func BuildDevDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the dev sandbox daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			return RunDevDaemon(cmd.Context())
		},
	}
}

func RunDevDaemon(ctx context.Context) error {
	cscmd := CodeServerCommand{}

	if err := cscmd.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, DevGVR, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		_ = cscmd.StopCodeServer(ctx)
	}()

	// Run setup and agent
	if err := RunDev(ctx); err != nil {
		return err
	}

	return cscmd.Wait()
}
