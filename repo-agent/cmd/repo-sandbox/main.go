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
			return fmt.Errorf("repo-sandbox command requires a subcommand (e.g., dev-daemon, review-daemon)")
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
	rootCommand.AddCommand(commands.BuildGithubFixIssueCommand())
	rootCommand.AddCommand(commands.BuildGithubTriageIssueCommand())
	rootCommand.AddCommand(commands.BuildGithubFeedbackCommand())
	rootCommand.AddCommand(commands.BuildGithubAutopollCommand())
	rootCommand.AddCommand(commands.BuildIterateCommand())

	rootCommand.AddCommand(commands.BuildThreadsCommand())

	// Commands from review-sandbox
	reviewDaemon := commands.BuildReviewDaemonCommand()
	reviewDaemon.Use = "review-daemon"
	rootCommand.AddCommand(reviewDaemon)

	rootCommand.AddCommand(commands.BuildReviewCommand())

	// Common commands
	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())
	rootCommand.AddCommand(commands.BuildInjectCommand())
	rootCommand.AddCommand(commands.BuildGitSetupCommand())

	return rootCommand.ExecuteContext(ctx)
}
