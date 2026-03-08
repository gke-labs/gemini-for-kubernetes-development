package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	if o.CommitSHA == "" {
		o.CommitSHA = os.Getenv("COMMIT-SHA")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("BRANCH_NAME")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("BRANCH")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("ISSUE_BRANCH")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("DEV_BRANCH")
	}
	if o.Branch == "" {
		o.Branch = os.Getenv("ISSUE-BRANCH")
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

	// Find the git repository directory.
	// In the sandbox, the repo is usually in /workspaces/<repo-name>.
	repoDir := os.Getenv("REPO")
	if repoDir == "" {
		repoDir = os.Getenv("REPO_NAME")
	}

	targetDir := ""
	if repoDir != "" {
		path := filepath.Join("/workspaces", repoDir)
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			targetDir = path
		}
	}

	if targetDir == "" {
		// Fallback: search /workspaces for any directory with a .git folder.
		if entries, err := os.ReadDir("/workspaces"); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					path := filepath.Join("/workspaces", entry.Name())
					if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
						targetDir = path
						break
					}
				}
			}
		}
	}

	if targetDir != "" {
		fmt.Printf("Changing directory to %s\n", targetDir)
		if err := os.Chdir(targetDir); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", targetDir, err)
		}
	}

	fmt.Printf("Rolling back to commit %s on branch %s\n", opts.CommitSHA, opts.Branch)

	// Ensure we are on the right branch before resetting.
	if err := gitcli.CheckoutBranch(opts.Branch); err != nil {
		fmt.Printf("Warning: failed to checkout branch %s: %v. Continuing anyway...\n", opts.Branch, err)
	}

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
