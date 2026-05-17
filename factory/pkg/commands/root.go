package commands

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	namespace  string
	image      string
	diskSize   string
	secretName string
	timeout    time.Duration
)

func NewRootCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "AI Factory CLI for fixing bugs and reviewing PRs in Kubernetes sandboxes",
		Long: `AI Factory CLI spins up isolated Kubernetes sandboxes (agents.x-k8s.io),
port-forwards directly to the envd daemon via Connect-RPC, and executes LLM-powered
coding tasks without local side effects or host dependencies.`,
		SilenceUsage: true,
	}

	cmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", os.Getenv("NAMESPACE"), "Kubernetes namespace (defaults to $NAMESPACE, gh user, or default)")
	cmd.PersistentFlags().StringVar(&image, "image", "ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest", "Sandbox base image")
	cmd.PersistentFlags().StringVar(&diskSize, "workspace-disk-size", "10Gi", "Workspace PVC disk size")
	cmd.PersistentFlags().StringVar(&secretName, "secret-name", "factory-user", "Kubernetes secret containing credentials")
	cmd.PersistentFlags().DurationVar(&timeout, "timeout", 30*time.Minute, "Overall execution timeout")

	cmd.PersistentPreRun = func(_ *cobra.Command, _ []string) {
		if namespace == "" {
			out, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login").Output()
			if err == nil {
				login := strings.TrimSpace(string(out))
				if login != "" && login != "null" {
					namespace = login
				}
			}
			if namespace == "" {
				namespace = "default"
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

	issueCmd := NewIssueCommand(ctx)
	issueCmd.GroupID = "workflows"
	cmd.AddCommand(issueCmd)

	prCmd := NewPRCommand(ctx)
	prCmd.GroupID = "workflows"
	cmd.AddCommand(prCmd)

	watchCmd := NewWatchCommand(ctx)
	watchCmd.GroupID = "workflows"
	cmd.AddCommand(watchCmd)

	userCmd := NewUserCommand(ctx)
	userCmd.GroupID = "management"
	cmd.AddCommand(userCmd)

	sandboxCmd := NewSandboxCommand(ctx)
	sandboxCmd.GroupID = "management"
	cmd.AddCommand(sandboxCmd)

	return cmd
}
