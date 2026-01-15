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
	defer stopListeningToSignals() // Ensure we stop listening to signals.

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// fmt.Fprintf(os.Stderr, "Completed successfully\n")
}

func run(ctx context.Context) error {
	// log := klog.FromContext(ctx)

	rootCommand := &cobra.Command{
		Use:   "dev-sandbox",
		Short: "Gemini Dev Sandbox Agent",
		// Default to running the dev daemon if no subcommand is provided
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("dev-sandbox command does not take any arguments")
			}
			daemonCmd := commands.DevDaemonCommand{}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}
	rootCommand.SilenceUsage = true  // Usage is only printed for command syntax errors
	rootCommand.SilenceErrors = true // We print errors ourselves

	rootCommand.AddCommand(commands.BuildDevDaemonCommand())
	rootCommand.AddCommand(commands.BuildDevCommand())
	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())
	rootCommand.AddCommand(commands.BuildInjectCommand())

	rootCommand.AddCommand(commands.BuildAgentCommand())
	rootCommand.AddCommand(commands.BuildCreateCommand())
	rootCommand.AddCommand(commands.BuildBootstrapCommand())
	rootCommand.AddCommand(commands.BuildCodeCommand())
	rootCommand.AddCommand(commands.BuildTmuxCommand())
	rootCommand.AddCommand(commands.BuildGithubFixIssueCommand())
	rootCommand.AddCommand(commands.BuildGithubFeedbackCommand())

	rootCommand.AddCommand(commands.BuildThreadsCommand())

	return rootCommand.ExecuteContext(ctx)
}
