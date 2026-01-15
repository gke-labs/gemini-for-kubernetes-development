package commands

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

var (
	IssueGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

type IssueCommand struct {
	PromptFilePath   string
	AgentName        string
	AgentPrompt      string
	BranchName       string
	PushEnabled      bool
	GithubUserOrigin string
	GithubUserLogin  string
	GithubUserEmail  string
	GithubUserName   string
	IssueID          string
}

func (c *IssueCommand) InitDefaults() {
	if c.PromptFilePath == "" {
		c.PromptFilePath = "/workspaces/agent-prompt.txt"
	}
	if c.AgentName == "" {
		c.AgentName = os.Getenv("AGENT_NAME")
	}
	if c.AgentPrompt == "" {
		c.AgentPrompt = os.Getenv("AGENT_PROMPT")
	}
	if c.BranchName == "" {
		c.BranchName = os.Getenv("ISSUE_BRANCH")
	}
	if !c.PushEnabled {
		if val := os.Getenv("GIT_PUSH_ENABLED"); val == "true" {
			c.PushEnabled = true
		}
	}
	if c.GithubUserOrigin == "" {
		c.GithubUserOrigin = os.Getenv("GITHUB_USER_ORIGIN")
	}
	if c.GithubUserLogin == "" {
		c.GithubUserLogin = os.Getenv("GITHUB_USER_LOGIN")
	}
	if c.GithubUserEmail == "" {
		c.GithubUserEmail = os.Getenv("GITHUB_USER_EMAIL")
	}
	if c.GithubUserName == "" {
		c.GithubUserName = os.Getenv("GITHUB_USER_NAME")
	}
	if c.IssueID == "" {
		c.IssueID = os.Getenv("ISSUEID")
	}
}

func BuildIssueCommand() *cobra.Command {
	issueCommand := IssueCommand{}
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Run the issue agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("issue command does not take any arguments")
			}
			issueCommand.InitDefaults()
			return issueCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&issueCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&issueCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&issueCommand.BranchName, "branch-name", os.Getenv("ISSUE_BRANCH"), "Issue branch name")
	cmd.Flags().BoolVar(&issueCommand.PushEnabled, "push-enabled", os.Getenv("GIT_PUSH_ENABLED") == "true", "Enable git push")
	cmd.Flags().StringVar(&issueCommand.GithubUserOrigin, "github-user-origin", os.Getenv("GITHUB_USER_ORIGIN"), "Github user origin")
	cmd.Flags().StringVar(&issueCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&issueCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&issueCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&issueCommand.IssueID, "issue-id", os.Getenv("ISSUEID"), "Issue ID")

	return cmd
}

func (c *IssueCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	go agentoutput.Run("issue", IssueGVR)

	updateState := func(state, message string) {
		err := agentoutput.SetAgentState(ctx, IssueGVR, state, message)
		if err != nil {
			log.Error(err, "updating agent state failed")
		}
	}

	updateState("handling issue", "")

	// Create config from env vars
	cfg := sandbox.Config{
		AgentName:        c.AgentName,
		AgentPrompt:      c.AgentPrompt,
		BranchName:       c.BranchName,
		PushEnabled:      c.PushEnabled,
		GithubUserOrigin: c.GithubUserOrigin,
		GithubUserLogin:  c.GithubUserLogin,
		GithubUserEmail:  c.GithubUserEmail,
		GithubUserName:   c.GithubUserName,
		GVR:              IssueGVR,
		ReportStatus:     true,
	}

	// Prepare git branch
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		updateState("error", err.Error())
		return fmt.Errorf("failed to prepare git branch: %w", err)
	}

	if _, err := os.Stat(c.PromptFilePath); os.IsNotExist(err) {
		// Try solving the issue
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				updateState("QUOTA ERROR", err.Error())
			} else {
				updateState("error", err.Error())
				return fmt.Errorf("failed solving issue: %w", err)
			}
		} else {
			updateState("done", "")
			// Push the changes
			commitMessage := "fix for issue # " + c.IssueID
			if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMessage); err != nil {
				updateState("error", err.Error())
				return fmt.Errorf("failed to process git changes: %w", err)
			}
		}
	} else {
		log.Info("agent-prompt.txt exists, skipping code generation")
	}

	return nil
}
