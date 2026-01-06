package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func BuildAgentCommand() *cobra.Command {
	agentCommand := &cobra.Command{
		Use:  "agent",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("agent command does not take any arguments")
			}
			return RunAgent(cmd.Context())
		},
	}
	agentCommand.Hidden = true
	return agentCommand
}

func RunAgent(ctx context.Context) error {
	log := klog.FromContext(ctx)

	if err := cloneRepos(ctx); err != nil {
		return fmt.Errorf("cloning repos: %w", err)
	}

	log.Info("dev-sandbox agent started; waiting for commands")
	<-ctx.Done()

	return nil
}

func cloneRepos(ctx context.Context) error {
	log := klog.FromContext(ctx)

	cloneRepos := os.Getenv("CLONE_REPOS")
	if cloneRepos == "" {
		log.Info("CLONE_REPOS not set; skipping git clone")
		return nil
	}

	for _, cloneRepo := range strings.Split(cloneRepos, ";") {
		tokens := strings.Split(cloneRepo, "=")
		if len(tokens) != 2 {
			return fmt.Errorf("invalid CLONE_REPOS entry: %q", cloneRepo)
		}
		repoDir := tokens[0]
		repoURL := tokens[1]

		log.Info("cloning repo", "repoURL", repoURL, "repoDir", repoDir)
		cmd := exec.CommandContext(ctx, "git", "clone", repoURL, repoDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to clone repo %q in %q: %w", repoURL, repoDir, err)
		}
	}
	return nil
}
