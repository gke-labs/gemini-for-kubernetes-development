package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gitcli"
	"github.com/spf13/cobra"
)

type RollbackOptions struct {
	CommitSHA string
	Branch    string
	Remote    string
}

func (o *RollbackOptions) InitDefaults() {
	if o.CommitSHA == "" {
		o.CommitSHA = os.Getenv("COMMIT_SHA")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("BRANCH_NAME")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("ISSUE_BRANCH")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("DEV_BRANCH")
	}
	if o.Branch == "" {
		cloneURL := os.Getenv("GIT_CLONE_URL")
		if cloneURL != "" && strings.Contains(cloneURL, "#refs/heads/") {
			parts := strings.SplitN(cloneURL, "#refs/heads/", 2)
			o.Branch = parts[1]
		}
	}
	if o.Remote == "" {
		o.Remote = "origin"
	}
}

func BuildRollbackCommand() *cobra.Command {
	opts := RollbackOptions{}
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Rollback to a previous commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.InitDefaults()
			return RunRollback(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.CommitSHA, "commit-sha", "", "Commit SHA to rollback to")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "Branch name")
	return cmd
}

func RunRollback(ctx context.Context, opts RollbackOptions) error {
	if opts.CommitSHA == "" {
		return fmt.Errorf("commit-sha is required")
	}
	if opts.Branch == "" {
		return fmt.Errorf("branch is required")
	}

	fmt.Printf("Rolling back to commit %s on branch %s\n", opts.CommitSHA, opts.Branch)

	// 1. git reset --hard <commit-sha>
	if err := gitcli.ResetHard(opts.CommitSHA); err != nil {
		return fmt.Errorf("failed to git reset --hard: %w", err)
	}

	// 2. git push --force origin <branch>
	if err := gitcli.Push(opts.Remote, opts.Branch, true); err != nil {
		return fmt.Errorf("failed to git push --force: %w", err)
	}

	fmt.Println("Rollback completed successfully")
	return nil
}
