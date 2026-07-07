package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/spf13/cobra"
)

type WatchFlags struct {
	Repo         string
	PollInterval time.Duration
	Assignee     string
	Labels       []string
	DryRun       bool
	WatchTimeout time.Duration
	MaxActions   int
	MaxPending   int
	Mode         string
	QueueDir     string
	Once         bool
	IssueMode    string
	PRMode       string
	ChoresMode   string
	ScanLimit    int
	TaskTimeout  time.Duration
}

func NewWatchCommand(ctx context.Context) *cobra.Command {
	var flags WatchFlags

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch a GitHub repo for test failures and assigned issues to automatically fix and review",
		Example: `  # Watch for unassigned issues with specific labels
  factory watch --repo owner/repo --assignee "" --labels "bug,help wanted"

  # Watch for assigned issues with labels
  factory watch --repo owner/repo --assignee "factory-bot" --labels "p0,urgent"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.Repo == "" {
				return fmt.Errorf("--repo is required (e.g. owner/repo)")
			}
			parts := strings.Split(flags.Repo, "/")
			if len(parts) != 2 {
				return fmt.Errorf("invalid repo format, expected owner/repo, got %s", flags.Repo)
			}

			issueMode := os.Getenv("ISSUE_MODE")
			if flags.IssueMode != "" {
				issueMode = flags.IssueMode
			}
			if issueMode == "" {
				issueMode = "enabled"
			}

			prMode := os.Getenv("PR_MODE")
			if flags.PRMode != "" {
				prMode = flags.PRMode
			}
			if prMode == "" {
				prMode = "enabled"
			}

			choresMode := os.Getenv("CHORES_MODE")
			if flags.ChoresMode != "" {
				choresMode = flags.ChoresMode
			}
			cfg, _ := config.LoadConfig()
			if cfg != nil && cfg.Chores.Mode == "disabled" {
				choresMode = "disabled"
			}
			if choresMode == "" {
				choresMode = "enabled"
			}

			opts := watch.Options{
				Owner:            parts[0],
				Repo:             parts[1],
				Interval:         flags.PollInterval,
				Assignee:         flags.Assignee,
				AssigneeChanged:  cmd.Flags().Changed("assignee"),
				Labels:           flags.Labels,
				DryRun:           flags.DryRun,
				WatchTimeout:     flags.WatchTimeout,
				MaxActions:       flags.MaxActions,
				MaxPending:       flags.MaxPending,
				Mode:             flags.Mode,
				QueueDir:         flags.QueueDir,
				Once:             flags.Once,
				IssueMode:        issueMode,
				PRMode:           prMode,
				ChoresMode:       choresMode,
				EphemeralStorage: rootFlags.EphemeralStorage,
				Secrets:          rootFlags.ResolvedSecrets,
				ScanLimit:        flags.ScanLimit,
				TaskTimeout:      flags.TaskTimeout,
				Namespace:        rootFlags.Namespace,
				SecretName:       rootFlags.SecretName,
				Image:            rootFlags.Image,
				DiskSize:         rootFlags.DiskSize,
			}

			return watch.RunWatch(ctx, opts)
		},
	}

	cmd.Flags().StringVar(&flags.Repo, "repo", "", "GitHub repository (e.g. owner/repo)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().StringVar(&flags.Assignee, "assignee", "factory-bot", "GitHub username to watch for assigned issues (use empty string for unassigned issues)")
	cmd.Flags().StringSliceVar(&flags.Labels, "labels", nil, "Comma-separated list of labels to filter issues by")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")
	cmd.Flags().IntVar(&flags.MaxActions, "max-actions", 40, "Maximum number of actions to take in a single watch loop")
	cmd.Flags().IntVar(&flags.MaxPending, "max-pending", 40, "Maximum number of pending/running sandboxes allowed before skipping actions")
	cmd.Flags().StringVar(&flags.Mode, "mode", "all", "Watch mode: all (scan & run), scan (only scan & queue), run (only process queue)")
	cmd.Flags().StringVar(&flags.QueueDir, "queue-dir", "/workspaces/queues", "Directory path for the task queues")
	cmd.Flags().BoolVar(&flags.Once, "once", false, "Run watch once and exit (waits for active tasks to complete)")
	cmd.Flags().StringVar(&flags.IssueMode, "issue-mode", "", "Issue mode: enabled or disabled (defaults to ISSUE_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.PRMode, "pr-mode", "", "PR mode: enabled or disabled (defaults to PR_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.ChoresMode, "chores-mode", "", "Chores mode: enabled or disabled (defaults to CHORES_MODE env or enabled)")
	cmd.Flags().IntVar(&flags.ScanLimit, "scan-limit", 100, "Maximum number of issues/PRs to fetch from GitHub API in a scan cycle")
	cmd.Flags().DurationVar(&flags.TaskTimeout, "task-timeout", 3*time.Hour, "Timeout for each task execution (default 3h)")

	return cmd
}
