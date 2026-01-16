package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
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

	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.UserDotfilesRepo, "user-dotfiles-repo", os.Getenv("USER_DOTFILESREPO"), "User dotfiles repo")
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.CloneURL, "clone-url", os.Getenv("GIT_CLONE_URL"), "Git clone URL")
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&daemonCmd.ReviewCommand.DiffURL, "diff-url", os.Getenv("GIT_DIFF_URL"), "Git diff URL")
	cmd.Flags().IntVar(&daemonCmd.ReviewCommand.MaxReviewFiles, "max-review-files", 0, "Max review files")

	return cmd
}

func (c *ReviewDaemonCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, ReviewGVR, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	// We ignore the error here to keep the pod running for debugging/code-server access
	if err := c.ReviewCommand.Run(ctx); err != nil {
		log.Error(err, "failed to run review command")
	}

	return c.CodeServerCommand.Wait()
}
