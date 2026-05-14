package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
)

// RunCodeOptions holds options for the RunCode function.
type RunCodeOptions struct {
	SandboxName string
}

// BuildCodeCommand creates a new cobra command for launching VS Code connected to the dev sandbox.
func BuildCodeCommand() *cobra.Command {
	var opt RunCodeOptions

	cmd := &cobra.Command{
		Use:   "code [sandbox-name]",
		Short: "Launch VS Code connected to the dev sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("code command requires exactly one argument: the sandbox name")
			}
			opt.SandboxName = args[0]

			return RunCode(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunCode launches VS Code connected to the specified dev sandbox.
func RunCode(ctx context.Context, opt RunCodeOptions) error {
	// 1. Find the pod
	podID, err := sandbox.FindSandboxPod(ctx, opt.SandboxName)
	if err != nil {
		return err
	}
	if podID == nil {
		return fmt.Errorf("no pod found for sandbox %q", opt.SandboxName)
	}

	// 2. Update ~/.ssh/config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home dir: %w", err)
	}

	sshConfigPath := filepath.Join(homeDir, ".ssh", "config")
	sshHost := opt.SandboxName

	if err := UpdateSSHConfig(ctx, sshConfigPath, sshHost, *podID); err != nil {
		return fmt.Errorf("failed to update ssh config: %w", err)
	}

	remotePath := "/workspaces/" + opt.SandboxName

	if err := launchVSCode(ctx, sshHost, remotePath); err != nil {
		return fmt.Errorf("failed to launch VS Code: %w", err)
	}

	return nil
}

// launchVSCode launches VS Code connected to the given SSH host.
func launchVSCode(ctx context.Context, sshHost string, remotePath string) error {
	// code --folder-uri vscode-remote://ssh-remote+<sshHost>/<remotePath

	uri := fmt.Sprintf("vscode-remote://ssh-remote+%s%s", sshHost, remotePath)

	fmt.Printf("Launching VS Code connecting to %s...\n", uri)

	cmd := exec.CommandContext(ctx, "code", "--folder-uri", uri)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to launch code: %w", err)
	}

	return nil
}
