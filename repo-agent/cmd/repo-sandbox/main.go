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
}

func run(ctx context.Context) error {
	rootCommand := &cobra.Command{
		Use:   "repo-sandbox",
		Short: "Gemini Repository Sandbox Agent",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("repo-sandbox command requires a subcommand (e.g., dev-daemon)")
		},
	}
	rootCommand.SilenceUsage = true  // Usage is only printed for command syntax errors
	rootCommand.SilenceErrors = true // We print errors ourselves

	// Commands from dev-sandbox
	sandboxDaemon := commands.BuildSandboxDaemonCommand()
	sandboxDaemon.Use = "dev-daemon"
	rootCommand.AddCommand(sandboxDaemon)

	rootCommand.AddCommand(commands.BuildDevInitCommand())
	rootCommand.AddCommand(commands.BuildAgentCommand())
	rootCommand.AddCommand(commands.BuildCreateCommand())
	rootCommand.AddCommand(commands.BuildBootstrapCommand())
	rootCommand.AddCommand(commands.BuildCodeCommand())
	rootCommand.AddCommand(commands.BuildTmuxCommand())

	rootCommand.AddCommand(commands.BuildThreadsCommand())

	// Issue-fix and PR-review agent subcommands were removed with the
	// factory-CLI migration: those tasks run through the factory engine now
	// (see docs/design/factory-cli-migration.md). repo-sandbox remains the
	// dev-sandbox binary.

	// Common commands
	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())
	rootCommand.AddCommand(commands.BuildInjectCommand())

	return rootCommand.ExecuteContext(ctx)
}
