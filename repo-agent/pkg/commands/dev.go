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
		Resource: "devsandboxes",
	}
)

func BuildDevCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dev",
		Short: "Run the dev agent setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("dev command does not take any arguments")
			}
			return RunDev(cmd.Context())
		},
	}
}

func RunDev(ctx context.Context) error {
	log := klog.FromContext(ctx)

	go agentoutput.Run("dev", DevGVR)

	repoURL := os.Getenv("GIT_HTML_URL")
	if repoURL == "" {
		return fmt.Errorf("GIT_HTML_URL environment variable not set")
	}

	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid GIT_HTML_URL format: %s", repoURL)
	}
	repoDir := filepath.Join("/workspaces", parts[1])

	ib := imagebuilder.ImageBuilder{
		DotFilesRepo: os.Getenv("USER_DOTFILESREPO"),
		CloneURL:     os.Getenv("GIT_CLONE_URL"),
		Destination:  repoDir,
	}
	// if repoDir doesnt exist, we need to clone it
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		if err := ib.CloneRepo(ctx); err != nil {
			_ = agentoutput.SetAgentState(ctx, DevGVR, "error", fmt.Sprintf("cloning repo failed: %v", err))
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
		AgentName:        os.Getenv("AGENT_NAME"),
		AgentPrompt:      os.Getenv("AGENT_PROMPT"),
		BranchName:       os.Getenv("DEV_BRANCH"),
		PushEnabled:      os.Getenv("GIT_PUSH_ENABLED") == "true",
		GithubUserOrigin: os.Getenv("GITHUB_USER_ORIGIN"),
		GithubUserLogin:  os.Getenv("GITHUB_USER_LOGIN"),
		GithubUserEmail:  os.Getenv("GITHUB_USER_EMAIL"),
		GithubUserName:   os.Getenv("GITHUB_USER_NAME"),
		ReportStatus:     false,
	}

	// Prepare git branch (checkout)
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		_ = agentoutput.SetAgentState(ctx, DevGVR, "error", fmt.Sprintf("checkout failed: %v", err))
		return fmt.Errorf("preparing git branch: %w", err)
	}

	if cfg.AgentPrompt != "" {
		log.Info("Running agent with prompt", "prompt", cfg.AgentPrompt)
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				_ = agentoutput.SetAgentState(ctx, DevGVR, "QUOTA ERROR", err.Error())
			} else {
				_ = agentoutput.SetAgentState(ctx, DevGVR, "error", fmt.Sprintf("running agent failed: %v", err))
				return fmt.Errorf("running agent: %w", err)
			}
		} else {
			commitMsg := "Agent changes for: " + cfg.AgentPrompt
			if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMsg); err != nil {
				_ = agentoutput.SetAgentState(ctx, DevGVR, "error", fmt.Sprintf("processing git changes failed: %v", err))
				return fmt.Errorf("processing git changes: %w", err)
			}
		}
	}
	_ = agentoutput.SetAgentState(ctx, DevGVR, "ready", "")
	return nil
}
