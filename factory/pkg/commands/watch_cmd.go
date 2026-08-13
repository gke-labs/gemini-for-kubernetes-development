package commands

import (
	"context"
	"os"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/spf13/cobra"
)

func NewWatchCommand(ctx context.Context) *cobra.Command {
	var flags watch.Flags

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

			flags.AssigneeChanged = cmd.Flags().Changed("assignee")

			if flags.IssueMode == "" {
				flags.IssueMode = os.Getenv("ISSUE_MODE")
			}
			if flags.IssueMode == "" {
				flags.IssueMode = "enabled"
			}

			if flags.PRMode == "" {
				flags.PRMode = os.Getenv("PR_MODE")
			}
			if flags.PRMode == "" {
				flags.PRMode = "enabled"
			}

			if flags.ChoresMode == "" {
				flags.ChoresMode = os.Getenv("CHORES_MODE")
			}
			cfg, _ := config.LoadConfig()
			if cfg != nil && cfg.Chores.Mode == "disabled" {
				flags.ChoresMode = "disabled"
			}
			if flags.ChoresMode == "" {
				flags.ChoresMode = "enabled"
			}

			watcher := watch.NewWatcher(rootFlags, flags)
			return watcher.Run(ctx)
		},
	}

	cmd.Flags().Var(&flags.Repo, "repo", "GitHub repository (e.g. owner/repo)")
	_ = cmd.MarkFlagRequired("repo")
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
	cmd.Flags().StringVar(&flags.SandboxEvictionAge, "sandbox-eviction-age", "7d", "Age threshold for idle sandbox eviction (e.g. '7d', '24h')")
	cmd.Flags().DurationVar(&flags.SandboxIdleTimeout, "sandbox-idle-timeout", common.GetEnvDuration("SANDBOX_IDLE_TIMEOUT", 0), "Idle timeout after which a sandbox that has not run any task is suspended by setting replicas to 0 (e.g. '30m', '1h')")
	cmd.Flags().DurationVar(&flags.PRInactivityTimeout, "pr-inactivity-timeout", common.GetEnvDuration("PR_INACTIVITY_TIMEOUT", 0), "Time of inactivity with no human comments before pausing automated processing on a PR (e.g. '24h', '168h')")

	return cmd
}
