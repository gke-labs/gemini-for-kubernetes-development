package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/imagebuilder"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

var (
	DevGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	IssueGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

type SandboxCommand struct {
	WorkspaceDir     string
	RepoURL          string
	UserDotfilesRepo string
	CloneURL         string
	AgentName        string
	AgentPrompt      string
	BranchName       string
	PushEnabled      bool
	GithubUserOrigin string
	GithubUserLogin  string
	GithubUserEmail  string
	GithubUserName   string
	IssueID          string
	PromptFilePath   string
	TokensDir        string
	RepoDir          string

	// output
	TaskDir         string
	OutputGVR       *schema.GroupVersionResource
	OutputName      string
	OutputNamespace string
}

func (c *SandboxCommand) InitDefaults() {
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "/workspaces"
	}
	if c.TaskDir == "" {
		c.TaskDir = c.WorkspaceDir
	}
	if c.TokensDir == "" {
		c.TokensDir = "/tokens"
	}
	if c.RepoURL == "" {
		c.RepoURL = os.Getenv("GIT_HTML_URL")
	}
	if c.UserDotfilesRepo == "" {
		c.UserDotfilesRepo = os.Getenv("USER_DOTFILESREPO")
	}
	if c.CloneURL == "" {
		c.CloneURL = os.Getenv("GIT_CLONE_URL")
	}
	if c.AgentName == "" {
		c.AgentName = os.Getenv("AGENT_NAME")
	}
	if c.AgentPrompt == "" {
		c.AgentPrompt = os.Getenv("AGENT_PROMPT")
	}
	if c.BranchName == "" {
		c.BranchName = os.Getenv("DEV_BRANCH")
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
	if c.PromptFilePath == "" {
		c.PromptFilePath = "/workspaces/agent-prompt.txt"
	}

	if c.IssueID != "" {
		c.OutputGVR = &IssueGVR
	} else {
		c.OutputGVR = &DevGVR
	}
	if c.OutputName == "" {
		c.OutputName = os.Getenv("NAME")
	}
	if c.OutputNamespace == "" {
		c.OutputNamespace = os.Getenv("NAMESPACE")
	}
}

func BuildSandboxCommand() *cobra.Command {
	sandboxCommand := SandboxCommand{}
	cmd := &cobra.Command{
		Use:   "dev", // Keeping "dev" as the command name for backward compatibility/simplicity
		Short: "Run the sandbox agent setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("dev command does not take any arguments")
			}
			sandboxCommand.InitDefaults()
			return sandboxCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&sandboxCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	cmd.Flags().StringVar(&sandboxCommand.UserDotfilesRepo, "user-dotfiles-repo", os.Getenv("USER_DOTFILESREPO"), "User dotfiles repo")
	cmd.Flags().StringVar(&sandboxCommand.CloneURL, "clone-url", os.Getenv("GIT_CLONE_URL"), "Git clone URL")
	cmd.Flags().StringVar(&sandboxCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&sandboxCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&sandboxCommand.BranchName, "branch-name", os.Getenv("DEV_BRANCH"), "Dev branch name")
	cmd.Flags().BoolVar(&sandboxCommand.PushEnabled, "push-enabled", os.Getenv("GIT_PUSH_ENABLED") == "true", "Enable git push")
	cmd.Flags().StringVar(&sandboxCommand.GithubUserOrigin, "github-user-origin", os.Getenv("GITHUB_USER_ORIGIN"), "Github user origin")
	cmd.Flags().StringVar(&sandboxCommand.GithubUserLogin, "github-user-login", os.Getenv("GITHUB_USER_LOGIN"), "Github user login")
	cmd.Flags().StringVar(&sandboxCommand.GithubUserEmail, "github-user-email", os.Getenv("GITHUB_USER_EMAIL"), "Github user email")
	cmd.Flags().StringVar(&sandboxCommand.GithubUserName, "github-user-name", os.Getenv("GITHUB_USER_NAME"), "Github user name")
	cmd.Flags().StringVar(&sandboxCommand.IssueID, "issue-id", os.Getenv("ISSUEID"), "Issue ID")

	return cmd
}

func (c *SandboxCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	var (
		gvr           schema.GroupVersionResource
		commitMessage string
	)

	if c.IssueID != "" {
		commitMessage = "fix for issue #" + c.IssueID
	} else {
		commitMessage = "Agent changes for: " + c.AgentPrompt
	}

	ao, err := agentoutput.New(*c.OutputGVR, c.OutputName, c.OutputNamespace)
	if err != nil {
		log.Error(err, "failed to create k8s client: %w", err)
		return err
	}

	updateState := func(state, message string) {
		err := ao.SetAgentState(ctx, state, message)
		if err != nil {
			log.Error(err, "updating agent state failed")
		}
	}

	if c.IssueID != "" {
		updateState("handling issue", "")
	}

	repoURL := c.RepoURL
	if repoURL == "" {
		return fmt.Errorf("GIT_HTML_URL environment variable not set")
	}

	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid GIT_HTML_URL format: %s", repoURL)
	}
	repoDir := filepath.Join(c.WorkspaceDir, parts[1])

	ib := imagebuilder.ImageBuilder{
		DotFilesRepo: c.UserDotfilesRepo,
		CloneURL:     c.CloneURL,
		Destination:  repoDir,
	}
	// if repoDir doesnt exist, we need to clone it
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		if err := ib.CloneRepo(ctx); err != nil {
			updateState("error", fmt.Sprintf("cloning repo failed: %v", err))
			return fmt.Errorf("Cloning repo failed: %w", err)
		}
	}

	if err := ib.InstallDotfilesRepo(ctx); err != nil {
		// Note: we don't fail the entire startup if dotfiles installation fails
		log.Error(err, "installing dotfiles repo", "repo", ib.DotFilesRepo)
	}

	// Change to repo dir
	if err := os.Chdir(repoDir); err != nil {
		return fmt.Errorf("failed to chdir to %s: %w", repoDir, err)
	}

	cfg := sandbox.Config{
		AgentName:        c.AgentName,
		AgentPrompt:      c.AgentPrompt,
		BranchName:       c.BranchName,
		PushEnabled:      c.PushEnabled,
		GithubUserOrigin: c.GithubUserOrigin,
		GithubUserLogin:  c.GithubUserLogin,
		GithubUserEmail:  c.GithubUserEmail,
		GithubUserName:   c.GithubUserName,
		GVR:              gvr,

		WorkspacesDir: c.WorkspaceDir,
		RepoDir:       repoDir,
		TokensDir:     c.TokensDir,
		TaskDir:       c.TaskDir,
		AgentOutput:   ao,
	}

	// Prepare git branch (checkout)
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		updateState("error", fmt.Sprintf("preparing git branch failed: %v", err))
		return fmt.Errorf("preparing git branch: %w", err)
	}

	shouldRunAgent := false
	if c.IssueID != "" {
		// Issue mode: Run if prompt file is missing
		if _, err := os.Stat(c.PromptFilePath); os.IsNotExist(err) {
			shouldRunAgent = true
		} else {
			log.Info("agent-prompt.txt exists, skipping code generation")
		}
	} else {
		// Dev mode: Run if agent prompt is provided
		if cfg.AgentPrompt != "" {
			shouldRunAgent = true
		}
	}

	if shouldRunAgent {
		log.Info("Running agent", "agent", cfg.AgentName)
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				updateState("QUOTA ERROR", err.Error())
			} else {
				updateState("error", fmt.Sprintf("running agent failed: %v", err))
				return fmt.Errorf("running agent: %w", err)
			}
		} else {
			if c.IssueID != "" {
				updateState("done", "")
			}
			if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMessage); err != nil {
				updateState("error", fmt.Sprintf("processing git changes failed: %v", err))
				return fmt.Errorf("processing git changes: %w", err)
			}
		}
	}

	if c.IssueID == "" {
		updateState("ready", "")
	}
	return nil
}
