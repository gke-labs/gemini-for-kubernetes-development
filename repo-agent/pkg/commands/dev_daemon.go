package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type DevDaemonCommand struct {
	DevCommand        DevCommand
	CodeServerCommand CodeServerCommand
}

func (c *DevDaemonCommand) InitDefaults() {
	c.DevCommand.InitDefaults()
	c.CodeServerCommand.InitDefaults()
}

func BuildDevDaemonCommand() *cobra.Command {
	daemonCmd := DevDaemonCommand{}
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the dev sandbox daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&daemonCmd.DevCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.UserDotfilesRepo, "user-dotfiles-repo", os.Getenv("USER_DOTFILESREPO"), "User dotfiles repo")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.CloneURL, "clone-url", os.Getenv("GIT_CLONE_URL"), "Git clone URL")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.BranchName, "branch-name", os.Getenv("DEV_BRANCH"), "Dev branch name")
	cmd.Flags().BoolVar(&daemonCmd.DevCommand.PushEnabled, "push-enabled", os.Getenv("GIT_PUSH_ENABLED") == "true", "Enable git push")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.GithubUserOrigin, "github-user-origin", os.Getenv("GITHUB_USER_ORIGIN"), "Github user origin")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&daemonCmd.DevCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")

	return cmd
}

func (c *DevDaemonCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, DevGVR, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	if err := c.DevCommand.Run(ctx); err != nil {
		return err
	}

	return c.CodeServerCommand.Wait()
}
