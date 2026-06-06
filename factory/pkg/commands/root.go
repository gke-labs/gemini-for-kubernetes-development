package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
)

const (
	KeyGithubToken    = "GITHUB_TOKEN"
	KeyGeminiAPIKey   = "GEMINI_API_KEY"
	KeyGithubLogin    = "GITHUB_LOGIN"
	KeyGithubEmail    = "GITHUB_EMAIL"
	SecretFactoryUser = "factory-user"
)

type RootFlags struct {
	Namespace        string
	Image            string
	DiskSize         string
	SecretName       string
	Timeout          time.Duration
	Background       bool
	Cleanup          bool
	EphemeralStorage string
	Secrets          []string
	ResolvedSecrets  []factorysandbox.SecretMount
	Envs             []string
	ResolvedEnvs     []factorysandbox.EnvVar
	Detached         bool
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
	cmd.PersistentFlags().BoolVar(&rootFlags.Background, "background", false, "Run the CLI command as a background daemon process and redirect output to a log file")
	cmd.PersistentFlags().BoolVar(&rootFlags.Cleanup, "cleanup", false, "Delete the sandbox after the task is run or watch completes")
	cmd.PersistentFlags().StringVar(&rootFlags.EphemeralStorage, "ephemeral-storage", "", "Sandbox ephemeral storage request/limit size")
	cmd.PersistentFlags().StringSliceVar(&rootFlags.Secrets, "secret", nil, "Inject a secret with format secretName:mountPath (can be specified multiple times)")
	cmd.PersistentFlags().StringArrayVar(&rootFlags.Envs, "env", nil, "Inject an environment variable with format KEY=VALUE (can be specified multiple times)")
	cmd.PersistentFlags().BoolVar(&rootFlags.Detached, "detached", false, "Run the task in the background of the sandbox pod and return immediately")

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

	cleanupCmd := NewCleanupCommand(ctx)
	cleanupCmd.GroupID = "management"
	cmd.AddCommand(cleanupCmd)

	sandboxCmd := NewSandboxCommand(ctx)
	sandboxCmd.GroupID = "management"
	cmd.AddCommand(sandboxCmd)

	daemonCmd := NewDaemonCommand(ctx)
	daemonCmd.GroupID = "management"
	cmd.AddCommand(daemonCmd)

	return cmd
}

func getGeminiAPIKey(secret *corev1.Secret) string {
	return geminitokens.GetGeminiAPIKey(secret)
}

func checkAndRunInBackground(sessionName string) (bool, error) {
	if !rootFlags.Background {
		return false, nil
	}
	if os.Getenv("FACTORY_BACKGROUND") == "true" {
		return false, nil // Already running in background
	}

	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("failed to get executable path: %w", err)
	}

	// Build args, stripping out the --background flag
	var args []string
	for _, arg := range os.Args[1:] {
		if arg == "--background" {
			continue
		}
		args = append(args, arg)
	}

	// Determine log file path
	logDir := os.Getenv("FACTORY_LOGS")
	if logDir == "" {
		logDir = "."
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create logs directory: %w", err)
	}

	timestamp := time.Now().Format("20060102-150405")
	logFile := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", sessionName, timestamp))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return false, fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	// Spawn the process detached
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), "FACTORY_BACKGROUND=true")
	cmd.Stdout = f
	cmd.Stderr = f

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("failed to start background process: %w", err)
	}

	fmt.Printf("Started command in background. Logs are redirected to %s\n", logFile)
	return true, nil // Parent exits
}

func ResolveRootFlags(cmd *cobra.Command) (*config.FactoryConfig, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	if !cmd.Flags().Changed("image") && cfg.Image != "" {
		rootFlags.Image = cfg.Image
	}
	if !cmd.Flags().Changed("workspace-disk-size") && cfg.WorkspaceDiskSize != "" {
		rootFlags.DiskSize = cfg.WorkspaceDiskSize
	}
	if !cmd.Flags().Changed("ephemeral-storage") && cfg.EphemeralStorage != "" {
		rootFlags.EphemeralStorage = cfg.EphemeralStorage
	}
	if rootFlags.EphemeralStorage == "" {
		rootFlags.EphemeralStorage = "6Gi"
	}

	if cmd.Flags().Changed("secret") {
		var resolved []factorysandbox.SecretMount
		for _, s := range rootFlags.Secrets {
			parts := strings.SplitN(s, ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return nil, fmt.Errorf("invalid secret format: %s. Expected secretName:mountPath", s)
			}
			resolved = append(resolved, factorysandbox.SecretMount{
				Name:      parts[0],
				MountPath: parts[1],
			})
		}
		rootFlags.ResolvedSecrets = resolved
	} else {
		rootFlags.ResolvedSecrets = ToSandboxSecrets(cfg.Secrets)
	}

	if cmd.Flags().Changed("env") {
		var resolved []factorysandbox.EnvVar
		for _, e := range rootFlags.Envs {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) != 2 || parts[0] == "" {
				return nil, fmt.Errorf("invalid env format: %s. Expected KEY=VALUE", e)
			}
			resolved = append(resolved, factorysandbox.EnvVar{
				Name:  parts[0],
				Value: parts[1],
			})
		}
		rootFlags.ResolvedEnvs = resolved
	} else {
		rootFlags.ResolvedEnvs = ToSandboxEnvs(cfg.Env)
	}

	return cfg, nil
}

func ToSandboxSecrets(mounts []config.SecretMount) []factorysandbox.SecretMount {
	res := make([]factorysandbox.SecretMount, len(mounts))
	for i, m := range mounts {
		res[i] = factorysandbox.SecretMount{
			Name:      m.Name,
			MountPath: m.MountPath,
		}
	}
	return res
}

func ToSandboxEnvs(envs []config.EnvVar) []factorysandbox.EnvVar {
	res := make([]factorysandbox.EnvVar, len(envs))
	for i, e := range envs {
		res[i] = factorysandbox.EnvVar{
			Name:  e.Name,
			Value: e.Value,
		}
	}
	return res
}
