package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gitcli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
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
	log := klog.FromContext(ctx)

	if opts.CommitSHA == "" {
		return fmt.Errorf("commit-sha is required")
	}
	if opts.Branch == "" {
		return fmt.Errorf("branch is required")
	}

	// 1. Try to find the repository directory
	repoDir := findRepoDir(ctx)

	if repoDir != "" {
		log.Info("Changing directory to repository", "dir", repoDir)
		if err := os.Chdir(repoDir); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", repoDir, err)
		}
	} else {
		// If we couldn't find it, check if we are already in one
		if _, err := gitcli.GetHeadCommitID(); err != nil {
			log.Info("Could not determine repository directory and current directory is not a git repository")
		} else {
			log.Info("Running in current directory (already a git repository)")
		}
	}

	log.Info("Rolling back", "commit", opts.CommitSHA, "branch", opts.Branch)

	// 2. git reset --hard <commit-sha>
	if err := gitcli.ResetHard(opts.CommitSHA); err != nil {
		return fmt.Errorf("failed to git reset --hard: %w", err)
	}

	// 3. git push --force origin <branch>
	if err := gitcli.Push(opts.Remote, opts.Branch, true); err != nil {
		return fmt.Errorf("failed to git push --force: %w", err)
	}

	log.Info("Rollback completed successfully")
	return nil
}

func findRepoDir(ctx context.Context) string {
	// Strategy 0: Check if current directory is a git repo
	if info, err := os.Stat(".git"); err == nil && info.IsDir() {
		return "."
	}

	// Strategy 1: Use CLONE_REPOS env var
	cloneRepos := os.Getenv("CLONE_REPOS")
	if cloneRepos != "" {
		for _, cloneRepo := range strings.Split(cloneRepos, ";") {
			tokens := strings.Split(cloneRepo, "=")
			if len(tokens) == 2 {
				dir := tokens[0]
				if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
					return dir
				}
			}
		}
	}

	// Strategy 2: Use REPO or GIT_HTML_URL env vars
	repoEnv := os.Getenv("REPO")
	if repoEnv == "" {
		repoEnv = os.Getenv("GIT_HTML_URL")
	}
	if repoEnv != "" {
		if repo, err := github.ParseRepo(repoEnv); err == nil {
			// Check /workspaces/<repo>
			dir := filepath.Join("/workspaces", repo.FilesystemName())
			if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
				return dir
			}
		}
	}

	// Strategy 3: Check /workspaces itself
	if info, err := os.Stat("/workspaces/.git"); err == nil && info.IsDir() {
		return "/workspaces"
	}

	// Strategy 4: Fallback to searching /workspaces/* for any .git repo
	matches, _ := filepath.Glob("/workspaces/*")
	for _, match := range matches {
		if info, err := os.Stat(filepath.Join(match, ".git")); err == nil && info.IsDir() {
			return match
		}
	}

	return ""
}

