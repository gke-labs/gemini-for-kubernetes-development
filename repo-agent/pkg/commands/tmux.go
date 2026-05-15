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

// RunTmuxOptions holds options for the RunTmux function.
type RunTmuxOptions struct {
	SandboxName string
}

// BuildTmuxCommand creates a new cobra command for launching tmux connected to the dev sandbox.
func BuildTmuxCommand() *cobra.Command {
	var opt RunTmuxOptions

	cmd := &cobra.Command{
		Use:   "tmux [sandbox-name]",
		Short: "Launch tmux connected to the dev sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("tmux command requires exactly one argument: the sandbox name")
			}
			opt.SandboxName = args[0]

			return RunTmux(cmd.Context(), opt)
		},
	}
	return cmd
}

// RunTmux launches tmux connected to the specified dev sandbox.
func RunTmux(ctx context.Context, opt RunTmuxOptions) error {
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

	if err := LaunchTmux(ctx, sshHost); err != nil {
		return fmt.Errorf("running tmux: %w", err)
	}

	return nil
}

// LaunchTmux launches tmux connected to the given SSH host.
func LaunchTmux(ctx context.Context, sshHost string) error {
	// ssh -t <sshHost> "tmux attach || tmux new-session"

	args := []string{
		"ssh", "-t", sshHost, "tmux attach || tmux new-session",
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ssh tmux: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("waiting for ssh tmux: %w", err)
	}

	return nil
}
