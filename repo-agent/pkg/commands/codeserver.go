package commands

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/codeserver"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// BuildCodeServerCommand creates a new cobra command for running the code-server.
func BuildCodeServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "codeserver",
		Short: "Run the code-server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunCodeServer(cmd.Context())
		},
	}
}

// RunCodeServer runs the code-server and waits for it to exit.
func RunCodeServer(ctx context.Context) error {
	log := klog.FromContext(ctx)
	log.Info("Starting code-server command")

	cmd, err := codeserver.Start()
	if err != nil {
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("code-server exited with error: %w", err)
	}

	return nil
}
