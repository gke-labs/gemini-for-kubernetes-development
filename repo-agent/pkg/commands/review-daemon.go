package commands

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/spf13/cobra"
)

func BuildReviewDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the review agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("review command does not take any arguments")
			}
			return RunReviewDaemon(cmd.Context())
		},
	}
}

func RunReviewDaemon(ctx context.Context) error {
	cscmd := CodeServerCommand{}

	if err := cscmd.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, ReviewGVR, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		_ = cscmd.StopCodeServer(ctx)
	}()

	_ = RunReview(ctx)

	return cscmd.Wait()
}
