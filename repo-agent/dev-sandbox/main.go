package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sshd"
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

	sshdCommand := &cobra.Command{
		Use: "sshd",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("sshd command does not take any arguments")
			}
			return RunSSHD(cmd.Context())
		},
	}
	rootCommand.AddCommand(sshdCommand)

	rootCommand.AddCommand(commands.BuildCreateCommand())
	rootCommand.AddCommand(commands.BuildBootstrapCommand())
	rootCommand.AddCommand(commands.NewCodeCommand())
	rootCommand.AddCommand(commands.NewTmuxCommand())

	return rootCommand.ExecuteContext(ctx)
}

func RunSSHD(ctx context.Context) error {
	log := klog.FromContext(ctx)

	conn := sshd.NewStdinStdoutConn(os.Stdin, os.Stdout)

	server := sshd.NewServer()

	if err := server.Start(ctx, conn); err != nil {
		log.Error(err, "SSH server exited with error")
		return fmt.Errorf("ssh server: %w", err)
	}

	// log.Info("SSH server exited successfully")
	return nil
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

	// Prepare git branch (checkout)
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		_ = agentoutput.SetAgentState(gvr, "error", fmt.Sprintf("checkout failed: %v", err))
		return fmt.Errorf("preparing git branch: %w", err)
	}

	if cfg.AgentPrompt != "" {
		log.Info("Running agent with prompt", "prompt", cfg.AgentPrompt)
		if err := sandbox.RunAgent(cfg); err != nil {
			_ = agentoutput.SetAgentState(gvr, "error", fmt.Sprintf("running agent failed: %v", err))
			return fmt.Errorf("running agent: %w", err)
		}

		commitMsg := "Agent changes for: " + cfg.AgentPrompt
		if err := sandbox.ProcessGitChanges(cfg, oldCommitID, commitMsg); err != nil {
			_ = agentoutput.SetAgentState(gvr, "error", fmt.Sprintf("processing git changes failed: %v", err))
			return fmt.Errorf("processing git changes: %w", err)
		}
	}

	var b ImageBuilder
	if dotFilesRepo := os.Getenv("USER_DOTFILESREPO"); dotFilesRepo != "" {
		_ = agentoutput.SetAgentState(gvr, "provisioning", "installing dotfiles")
		if err := b.InstallDotfilesRepo(ctx, dotFilesRepo); err != nil {
			// Note: we don't fail the entire startup if dotfiles installation fails
			log.Error(err, "installing dotfiles repo", "repo", dotFilesRepo)
			_ = agentoutput.SetAgentState(gvr, "warning", fmt.Sprintf("dotfiles install failed: %v", err))
		}
	}

	cmdCodeSrv, err := startCodeServer(ctx)
	if err != nil {
		_ = agentoutput.SetAgentState(gvr, "error", fmt.Sprintf("failed to start code-server: %v", err))
		return fmt.Errorf("failed to start code-server: %w", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			if err := cmdCodeSrv.Process.Kill(); err != nil {
				log.Error(err, "killing process")
			}
		}
	}()

	_ = agentoutput.SetAgentState(gvr, "ready", "")

	// Wait for code-server to exit
	if err := cmdCodeSrv.Wait(); err != nil {
		_ = agentoutput.SetAgentState(gvr, "error", fmt.Sprintf("code-server exited: %v", err))
		return fmt.Errorf("code-server process exited with error: %w", err)
	}

	return nil
}

func startCodeServer(ctx context.Context) (*exec.Cmd, error) {
	log.Println("starting code-server")
	repoURL := os.Getenv("GIT_HTML_URL")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid GIT_HTML_URL: %s", repoURL)
	}
	repo := parts[1]
	codeServerPath := "/usr/bin/code-server"
	args := []string{"--auth=none", "--bind-addr=0.0.0.0:13337", "/workspaces/" + repo}
	cmd := exec.CommandContext(ctx, codeServerPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("running code-server command failed: %w", err)
	}
	log.Printf("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}

// ImageBuilder is responsible for initializing the container, e.g. installing dotfiles
type ImageBuilder struct {
}

// InstallDotfilesRepo clones and install a dotfiles repo
func (b *ImageBuilder) InstallDotfilesRepo(ctx context.Context, dotFilesRepo string) error {
	log := klog.FromContext(ctx)

	// Get the user's cache directory
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("getting user cache dir: %w", err)
	}

	dotfilesDir := filepath.Join(cacheDir, "dev-sandbox", "dotfiles")

	if err := b.GitClone(ctx, dotFilesRepo, dotfilesDir); err != nil {
		return err
	}

	// Well-known entrypoints
	entrypoints := []string{
		"setup",
	}

	var foundEntrypoint string
	for _, entrypoint := range entrypoints {
		p := filepath.Join(dotfilesDir, entrypoint)
		if _, err := os.Stat(p); err != nil {
			if !os.IsNotExist(err) {
				log.Error(err, "error checking for entrypoint", "entrypoint", p)
			}
			continue
		}
		foundEntrypoint = p
		break
	}

	if foundEntrypoint == "" {
		return fmt.Errorf("unable to find entrypoint in dotfiles repo %q", dotFilesRepo)
	}

	cmd := exec.CommandContext(ctx, foundEntrypoint)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running dotfiles entrypoint %q from repo %q: %w", strings.Join(cmd.Args, " "), dotFilesRepo, err)
	}

	return nil
}

// GitClone clones a git repo to the dest directory.
func (b *ImageBuilder) GitClone(ctx context.Context, source string, dest string) error {
	log := klog.FromContext(ctx)

	args := []string{
		"git",
		"clone",
		source,
		dest,
	}

	cmdString := strings.Join(args, " ")
	log.Info("cloning git repo", "source", source, "command", cmdString)

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloning git repo %q with %q: %w", source, cmdString, err)
	}

	return nil
}
