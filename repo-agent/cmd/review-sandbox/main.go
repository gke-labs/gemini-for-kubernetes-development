package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/commands"
	"github.com/spf13/cobra"
)

func main() {
	ctx := context.Background()

	// Listen to signals so we can gracefully shutdown
	ctx, stopListeningToSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopListeningToSignals()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	rootCommand := &cobra.Command{
		Use:   "review-sandbox",
		Short: "Gemini Review Sandbox Agent",
		// Default to running the review if no subcommand is provided, for backward compatibility
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("review-sandbox does not take arguments, use subcommands")
			}
			daemonCmd := commands.ReviewDaemonCommand{}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}

	rootCommand.AddCommand(commands.BuildReviewCommand())
	rootCommand.AddCommand(commands.BuildReviewDaemonCommand())
	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())
	rootCommand.AddCommand(commands.BuildInjectCommand())

	return rootCommand.ExecuteContext(ctx)
}
