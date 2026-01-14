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

func BuildIssueCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "issue",
		Short: "Run the issue agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("issue command does not take any arguments")
			}
			return RunIssue(cmd.Context())
		},
	}
}

func RunIssue(ctx context.Context) error {
	log := klog.FromContext(ctx)

	go agentoutput.Run("issue", IssueGVR)

	_ = agentoutput.SetAgentState(ctx, IssueGVR, "handling issue", "")

	// Create config from env vars
	cfg := sandbox.Config{
		AgentName:        os.Getenv("AGENT_NAME"),
		AgentPrompt:      os.Getenv("AGENT_PROMPT"),
		BranchName:       os.Getenv("ISSUE_BRANCH"),
		PushEnabled:      os.Getenv("GIT_PUSH_ENABLED") == "true",
		GithubUserOrigin: os.Getenv("GITHUB_USER_ORIGIN"),
		GithubUserLogin:  os.Getenv("GITHUB_USER_LOGIN"),
		GithubUserEmail:  os.Getenv("GITHUB_USER_EMAIL"),
		GithubUserName:   os.Getenv("GITHUB_USER_NAME"),
		GVR:              IssueGVR,
		ReportStatus:     true,
	}

	// Prepare git branch
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		_ = agentoutput.SetAgentState(ctx, IssueGVR, "error", err.Error())
		return fmt.Errorf("failed to prepare git branch: %w", err)
	}

	if _, err := os.Stat("../agent-prompt.txt"); os.IsNotExist(err) {
		// Try solving the issue
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				_ = agentoutput.SetAgentState(ctx, IssueGVR, "QUOTA ERROR", err.Error())
			} else {
				_ = agentoutput.SetAgentState(ctx, IssueGVR, "error", err.Error())
				return fmt.Errorf("failed solving issue: %w", err)
			}
		} else {
			_ = agentoutput.SetAgentState(ctx, IssueGVR, "done", "")
			// Push the changes
			commitMessage := "fix for issue # " + os.Getenv("ISSUEID")
			if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMessage); err != nil {
				_ = agentoutput.SetAgentState(ctx, IssueGVR, "error", err.Error())
				return fmt.Errorf("failed to process git changes: %w", err)
			}
		}
	} else {
		log.Info("agent-prompt.txt exists, skipping code generation")
	}

	return nil
}
