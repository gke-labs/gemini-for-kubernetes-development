package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	Namespace  string
	Image      string
	DiskSize   string
	SecretName string
	Timeout    time.Duration
	Tmux       bool
	Cleanup    bool
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
	cmd.PersistentFlags().BoolVar(&rootFlags.Tmux, "tmux", false, "Run blocking remote tasks inside a named tmux session inside the sandbox")
	cmd.PersistentFlags().BoolVar(&rootFlags.Cleanup, "cleanup", false, "Delete the sandbox after the task is run or watch completes")

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
	if token, err := getTokenFromScript(); err == nil && token != "" {
		return token
	}
	if secret != nil {
		return string(secret.Data[KeyGeminiAPIKey])
	}
	return ""
}

func getTokenFromScript() (string, error) {
	dir := os.Getenv("TOKENSCRIPT_DIR")
	if dir == "" {
		return "", nil
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read tokenscript dir: %w", err)
	}

	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "..") {
			continue
		}

		path := filepath.Join(dir, f.Name())
		cmd := exec.Command(path)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to run tokenscript %s: %w", path, err)
		}

		return strings.TrimSpace(out.String()), nil
	}

	return "", nil
}

func wrapWithTmux(cmdStr string, sessionName string) string {
	// Disabled wrapping remote tasks in tmux as we now wrap the CLI itself.
	return cmdStr
}

func checkAndRunInTmux(sessionName string) (bool, error) {
	if !rootFlags.Tmux {
		return false, nil
	}
	if os.Getenv("FACTORY_TMUX") == "true" {
		return false, nil // Already running in tmux
	}

	// Check if the tmux session already exists
	hasSessionCmd := exec.Command("tmux", "has-session", "-t", sessionName)
	if err := hasSessionCmd.Run(); err == nil {
		fmt.Printf("Tmux session '%s' is already running in the background.\n", sessionName)
		fmt.Printf("To attach, run: tmux attach -t '%s'\n", sessionName)
		return true, nil // Parent process exits, doing nothing
	}

	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("failed to get executable path: %w", err)
	}

	args := os.Args[1:]
	
	var quotedArgs []string
	for _, arg := range args {
		quotedArgs = append(quotedArgs, fmt.Sprintf("'%s'", strings.ReplaceAll(arg, "'", "'\\''")))
	}
	// Always wait after command exits so logs are preserved
	shellCmd := fmt.Sprintf("'%s' %s; echo; echo 'Command finished. Press Enter to exit...'; read _", executable, strings.Join(quotedArgs, " "))
	
	// Start detached (-d)
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName, "sh", "-c", shellCmd}

	cmd := exec.Command("tmux", tmuxArgs...)
	cmd.Env = append(os.Environ(), "FACTORY_TMUX=true")
	
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	fmt.Printf("Starting command inside background tmux session '%s'...\n", sessionName)
	fmt.Printf("To view logs, run: tmux attach -t '%s'\n", sessionName)
	
	if err := cmd.Run(); err != nil {
		return true, fmt.Errorf("failed to start tmux session: %s: %w", stderr.String(), err)
	}

	return true, nil
}
