package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type IssueDaemonCommand struct {
	IssueCommand      IssueCommand
	CodeServerCommand CodeServerCommand
}

func (c *IssueDaemonCommand) InitDefaults() {
	c.IssueCommand.InitDefaults()
	c.CodeServerCommand.InitDefaults()
}

func BuildIssueDaemonCommand() *cobra.Command {
	daemonCmd := IssueDaemonCommand{}
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the issue agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("issue command does not take any arguments")
			}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&daemonCmd.IssueCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.BranchName, "branch-name", os.Getenv("ISSUE_BRANCH"), "Issue branch name")
	cmd.Flags().BoolVar(&daemonCmd.IssueCommand.PushEnabled, "push-enabled", os.Getenv("GIT_PUSH_ENABLED") == "true", "Enable git push")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.GithubUserOrigin, "github-user-origin", os.Getenv("GITHUB_USER_ORIGIN"), "Github user origin")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&daemonCmd.IssueCommand.IssueID, "issue-id", os.Getenv("ISSUEID"), "Issue ID")

	return cmd
}

func (c *IssueDaemonCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, IssueGVR, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	if err := c.IssueCommand.Run(ctx); err != nil {
		return err
	}

	return c.CodeServerCommand.Wait()
}
