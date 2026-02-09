package commands

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
)

type GitSetupCommand struct {
	Name  string
	Email string
}

func BuildGitSetupCommand() *cobra.Command {
	gitSetup := GitSetupCommand{}
	cmd := &cobra.Command{
		Use:   "git-setup",
		Short: "Configure git user and email",
		RunE: func(cmd *cobra.Command, args []string) error {
			return gitSetup.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&gitSetup.Name, "name", "", "User name")
	cmd.Flags().StringVar(&gitSetup.Email, "email", "", "User email")
	
	return cmd
}

func (c *GitSetupCommand) Run(ctx context.Context) error {
	if c.Name != "" {
		cmd := exec.Command("git", "config", "--global", "user.name", c.Name)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to set user.name: %s: %w", string(out), err)
		}
		fmt.Printf("Git user.name set to: %s
", c.Name)
	}

	if c.Email != "" {
		cmd := exec.Command("git", "config", "--global", "user.email", c.Email)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to set user.email: %s: %w", string(out), err)
		}
		fmt.Printf("Git user.email set to: %s
", c.Email)
	}

	return nil
}
