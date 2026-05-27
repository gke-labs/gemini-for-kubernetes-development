package commands

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	KeyGithubToken    = "GITHUB_TOKEN"
	KeyGeminiAPIKey   = "GEMINI_API_KEY"
	KeyGithubLogin    = "GITHUB_LOGIN"
	KeyGithubEmail    = "GITHUB_EMAIL"
	SecretFactoryUser = "factory-user"
)

type RootFlags struct {
	Namespace  string
	Image      string
	DiskSize   string
	SecretName string
	Timeout    time.Duration
}

var rootFlags RootFlags

func NewRootCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "AI Factory CLI for fixing bugs and reviewing PRs in Kubernetes sandboxes",
		Long: `AI Factory CLI spins up isolated Kubernetes sandboxes (agents.x-k8s.io),
port-forwards directly to the envd daemon via Connect-RPC, and executes LLM-powered
coding tasks without local side effects or host dependencies.`,
		SilenceUsage: true,
	}

	cmd.PersistentFlags().StringVarP(&rootFlags.Namespace, "namespace", "n", os.Getenv("NAMESPACE"), "Kubernetes namespace (defaults to $NAMESPACE, gh user, or default)")
	cmd.PersistentFlags().StringVar(&rootFlags.Image, "image", "ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest", "Sandbox base image")
	cmd.PersistentFlags().StringVar(&rootFlags.DiskSize, "workspace-disk-size", "10Gi", "Workspace PVC disk size")
	cmd.PersistentFlags().StringVar(&rootFlags.SecretName, "secret-name", SecretFactoryUser, "Kubernetes secret containing credentials")
	cmd.PersistentFlags().DurationVar(&rootFlags.Timeout, "timeout", 30*time.Minute, "Overall execution timeout")

	cmd.PersistentPreRun = func(_ *cobra.Command, _ []string) {
		if rootFlags.Namespace == "" {
			out, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login").Output()
			if err == nil {
				login := strings.TrimSpace(string(out))
				if login != "" && login != "null" {
					rootFlags.Namespace = login
				}
			}
			if rootFlags.Namespace == "" {
				rootFlags.Namespace = "default"
			}
		}
	}

	cmd.AddGroup(&cobra.Group{
		ID:    "workflows",
		Title: "AI Workflows:",
	})
	cmd.AddGroup(&cobra.Group{
		ID:    "management",
		Title: "Cluster & User Management:",
	})

	upCmd := NewUpCommand(ctx)
	upCmd.GroupID = "management"
	cmd.AddCommand(upCmd)

	statusCmd := NewStatusCommand(ctx)
	statusCmd.GroupID = "management"
	cmd.AddCommand(statusCmd)

	fixCmd := NewFixCommand(ctx)
	fixCmd.GroupID = "workflows"
	cmd.AddCommand(fixCmd)

	prCmd := NewPRCommand(ctx)
	prCmd.GroupID = "workflows"
	cmd.AddCommand(prCmd)

	watchCmd := NewWatchCommand(ctx)
	watchCmd.GroupID = "workflows"
	cmd.AddCommand(watchCmd)

	agentCmd := NewAgentCommand(ctx)
	agentCmd.GroupID = "workflows"
	cmd.AddCommand(agentCmd)

	userCmd := NewUserCommand(ctx)
	userCmd.GroupID = "management"
	cmd.AddCommand(userCmd)

	sandboxCmd := NewSandboxCommand(ctx)
	sandboxCmd.GroupID = "management"
	cmd.AddCommand(sandboxCmd)

	daemonCmd := NewDaemonCommand(ctx)
	daemonCmd.GroupID = "management"
	cmd.AddCommand(daemonCmd)

	return cmd
}
