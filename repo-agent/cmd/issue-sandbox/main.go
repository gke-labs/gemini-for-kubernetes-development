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
		Use:   "issue-sandbox",
		Short: "Gemini Issue Sandbox Agent",
		// Default to running the issue daemon if no subcommand is provided
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("issue-sandbox does not take arguments, use subcommands")
			}
			return commands.RunIssueDaemon(cmd.Context())
		},
	}

	rootCommand.AddCommand(commands.BuildIssueCommand())
	rootCommand.AddCommand(commands.BuildIssueDaemonCommand())
	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())

	return rootCommand.ExecuteContext(ctx)
}
