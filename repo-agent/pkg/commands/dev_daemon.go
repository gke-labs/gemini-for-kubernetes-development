package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

type SandboxDaemonCommand struct {
	SandboxCommand    SandboxCommand
	CodeServerCommand CodeServerCommand
}

func (c *SandboxDaemonCommand) InitDefaults() {
	c.SandboxCommand.InitDefaults()
	c.CodeServerCommand.InitDefaults()
}

func BuildSandboxDaemonCommand() *cobra.Command {
	daemonCmd := SandboxDaemonCommand{}
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the sandbox daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			daemonCmd.InitDefaults()
			return daemonCmd.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.UserDotfilesRepo, "user-dotfiles-repo", os.Getenv("USER_DOTFILESREPO"), "User dotfiles repo")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.CloneURL, "clone-url", os.Getenv("GIT_CLONE_URL"), "Git clone URL")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.BranchName, "branch-name", os.Getenv("DEV_BRANCH"), "Dev branch name")
	cmd.Flags().BoolVar(&daemonCmd.SandboxCommand.PushEnabled, "push-enabled", os.Getenv("GIT_PUSH_ENABLED") == "true", "Enable git push")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.GithubUserOrigin, "github-user-origin", os.Getenv("GITHUB_USER_ORIGIN"), "Github user origin")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&daemonCmd.SandboxCommand.IssueID, "issue-id", os.Getenv("ISSUEID"), "Issue ID")

	return cmd
}

func (c *SandboxDaemonCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	var gvr schema.GroupVersionResource
	if c.SandboxCommand.IssueID != "" {
		gvr = IssueGVR
	} else {
		gvr = DevGVR
	}

	if err := c.CodeServerCommand.Start(ctx); err != nil {
		_ = agentoutput.SetAgentState(ctx, gvr, "error", err.Error())
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	defer func() {
		if err := c.CodeServerCommand.StopCodeServer(ctx); err != nil {
			log.Error(err, "failed to stop code-server")
		}
	}()

	// We ignore the error here to keep the pod running for debugging/code-server access
	if err := c.SandboxCommand.Run(ctx); err != nil {
		log.Error(err, "failed to run sandbox command")
	}

	return c.CodeServerCommand.Wait()
}
