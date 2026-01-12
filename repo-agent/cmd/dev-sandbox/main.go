package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/codeserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/imagebuilder"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/commands"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

var (
	gvr = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}
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

	// fmt.Fprintf(os.Stderr, "Completed successfully\n")
}

func run(ctx context.Context) error {
	// log := klog.FromContext(ctx)

	rootCommand := &cobra.Command{}
	rootCommand.SilenceUsage = true

	initCommand := &cobra.Command{
		Use: "init",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("init command does not take any arguments")
			}
			return InitContainer(cmd.Context())
		},
	}
	rootCommand.AddCommand(initCommand)

	rootCommand.AddCommand(commands.BuildSSHDCommand())
	rootCommand.AddCommand(commands.BuildCodeServerCommand())

	rootCommand.AddCommand(commands.BuildAgentCommand())
	rootCommand.AddCommand(commands.BuildCreateCommand())
	rootCommand.AddCommand(commands.BuildBootstrapCommand())
	rootCommand.AddCommand(commands.BuildCodeCommand())
	rootCommand.AddCommand(commands.BuildTmuxCommand())
	rootCommand.AddCommand(commands.BuildGithubFixIssueCommand())
	rootCommand.AddCommand(commands.BuildGithubFeedbackCommand())

	rootCommand.AddCommand(commands.BuildThreadsCommand())

	return rootCommand.ExecuteContext(ctx)
}

func InitContainer(ctx context.Context) error {
	log := klog.FromContext(ctx)

	go agentoutput.Run("dev", gvr)

	repoURL := os.Getenv("GIT_HTML_URL")
	if repoURL == "" {
		return fmt.Errorf("GIT_HTML_URL environment variable not set")
	}

	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid GIT_HTML_URL format: %s", repoURL)
	}
	repoDir := filepath.Join("/workspaces", parts[1])

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

	var b imagebuilder.ImageBuilder
	if dotFilesRepo := os.Getenv("USER_DOTFILESREPO"); dotFilesRepo != "" {
		_ = agentoutput.SetAgentState(ctx, gvr, "provisioning", "installing dotfiles")
		if err := b.InstallDotfilesRepo(ctx, dotFilesRepo); err != nil {
			// Note: we don't fail the entire startup if dotfiles installation fails
			log.Error(err, "installing dotfiles repo", "repo", dotFilesRepo)
			_ = agentoutput.SetAgentState(ctx, gvr, "warning", fmt.Sprintf("dotfiles install failed: %v", err))
		}
	}

	// Prepare git branch (checkout)
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		_ = agentoutput.SetAgentState(ctx, gvr, "error", fmt.Sprintf("checkout failed: %v", err))
		return fmt.Errorf("preparing git branch: %w", err)
	}

	if cfg.AgentPrompt != "" {
		log.Info("Running agent with prompt", "prompt", cfg.AgentPrompt)
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				_ = agentoutput.SetAgentState(ctx, gvr, "QUOTA ERROR", err.Error())
			} else {
				_ = agentoutput.SetAgentState(ctx, gvr, "error", fmt.Sprintf("running agent failed: %v", err))
				return fmt.Errorf("running agent: %w", err)
			}
		} else {
			commitMsg := "Agent changes for: " + cfg.AgentPrompt
			if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMsg); err != nil {
				_ = agentoutput.SetAgentState(ctx, gvr, "error", fmt.Sprintf("processing git changes failed: %v", err))
				return fmt.Errorf("processing git changes: %w", err)
			}
		}
	}

	cmdCodeSrv, err := codeserver.Start()
	if err != nil {
		_ = agentoutput.SetAgentState(ctx, gvr, "error", fmt.Sprintf("failed to start code-server: %v", err))
		return fmt.Errorf("failed to start code-server: %w", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			if err := cmdCodeSrv.Process.Kill(); err != nil {
				log.Error(err, "killing process")
			}
		}
	}()

	_ = agentoutput.SetAgentState(ctx, gvr, "ready", "")

	// Wait for code-server to exit
	if err := cmdCodeSrv.Wait(); err != nil {
		_ = agentoutput.SetAgentState(ctx, gvr, "error", fmt.Sprintf("code-server exited: %v", err))
		return fmt.Errorf("code-server process exited with error: %w", err)
	}

	return nil
}
