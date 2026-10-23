package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
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
		Example: `  # Watch issues and PRs assigned to factory-bot, and those with the trigger label
  factory watch --repo owner/repo --assignee "factory-bot"

  # Report what would be done with PRs without doing it
  factory watch --repo owner/repo --pr-mode dryrun --once`,
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
			for _, m := range []struct{ name, value string }{{"issue-mode", flags.IssueMode}, {"pr-mode", flags.PRMode}, {"chores-mode", flags.ChoresMode}} {
				if !validMode(m.value) {
					return fmt.Errorf("--%s %q: want enabled, disabled or dryrun", m.name, m.value)
				}
			}

			watcher := watch.NewWatcher(rootFlags, flags)
			watcher.ProposeFanout = proposeFanout
			watcher.WarmWorkspace = warmWorkspace
			watcher.RepoRules = repoWatchRules(flags.Repo.Owner, flags.Repo.Repo)
			watcher.RepoRecipeAs = repoRecipeAs(flags.Repo.Owner, flags.Repo.Repo)
			return watcher.Run(ctx)
		},
	}

	cmd.Flags().Var(&flags.Repo, "repo", "GitHub repository (e.g. owner/repo)")
	_ = cmd.MarkFlagRequired("repo")
	cmd.Flags().StringVar(&flags.Assignee, "assignee", "factory-bot", "GitHub username to watch for assigned issues (use empty string for unassigned issues)")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")
	cmd.Flags().IntVar(&flags.MaxActions, "max-actions", 40, "Maximum number of actions to take in a single watch loop")
	cmd.Flags().IntVar(&flags.MaxPending, "max-pending", 40, "Maximum number of pending/running sandboxes allowed before skipping actions")
	cmd.Flags().StringVar(&flags.Mode, "mode", "all", "Watch mode: all (scan & run), scan (only scan & queue), run (only process queue)")
	cmd.Flags().StringVar(&flags.QueueDir, "queue-dir", "/workspaces/queues", "Directory path for the task queues")
	cmd.Flags().BoolVar(&flags.Once, "once", false, "Run watch once and exit (waits for active tasks to complete)")
	cmd.Flags().StringVar(&flags.IssueMode, "issue-mode", "", "Issue mode: enabled, disabled, or dryrun (scan and log, change nothing) (defaults to ISSUE_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.PRMode, "pr-mode", "", "PR mode: enabled, disabled, or dryrun (scan and log, change nothing) (defaults to PR_MODE env or enabled)")
	cmd.Flags().StringVar(&flags.ChoresMode, "chores-mode", "", "Chores mode: enabled, disabled, or dryrun (log what is due, queue nothing) (defaults to CHORES_MODE env or enabled)")
	cmd.Flags().IntVar(&flags.ScanLimit, "scan-limit", 100, "Maximum number of issues/PRs to fetch from GitHub API in a scan cycle")
	cmd.Flags().DurationVar(&flags.TaskTimeout, "task-timeout", common.GetEnvDuration("TASK_TIMEOUT", 24*time.Hour), "Timeout for each task execution, after which the task is failed and its sandbox deleted (e.g. '3h', '24h')")
	cmd.Flags().StringVar(&flags.SandboxEvictionAge, "sandbox-eviction-age", "7d", "Age threshold for idle sandbox eviction (e.g. '7d', '24h')")
	cmd.Flags().DurationVar(&flags.SandboxIdleTimeout, "sandbox-idle-timeout", common.GetEnvDuration("SANDBOX_IDLE_TIMEOUT", 0), "Idle timeout after which a sandbox that has not run any task is suspended by setting replicas to 0 (e.g. '30m', '1h')")
	cmd.Flags().StringVar(&flags.RulesDisabled, "rules-disabled", os.Getenv("WATCH_RULES_DISABLED"), "Rules the watch does not act on at all, whose queued tasks are dropped before they start: review,ci (the emergency switch) (defaults to the WATCH_RULES_DISABLED env)")
	cmd.Flags().StringVar(&flags.RulesRecord, "rules-record", os.Getenv("WATCH_RULES_RECORD"), "File the watch records each decision in, with the facts it was made on, for `factory watch rules replay` and `explain --record`; empty records nothing (defaults to the WATCH_RULES_RECORD env)")
	cmd.Flags().StringVar(&flags.ScanState, "scan-state", os.Getenv("WATCH_SCAN_STATE"), "File the shared scan's snapshot persists in (defaults to the WATCH_SCAN_STATE env, else scan_state.json in the queue directory)")
	cmd.Flags().BoolVar(&flags.SharedScan, "shared-scan", envBool("WATCH_SHARED_SCAN"), "Have the issue and PR scanners read one shared set of GitHub listings, refreshed incrementally (since the last pass) and kept in the queue directory across restarts, instead of each listing for itself; what they decide is unchanged (defaults to the WATCH_SHARED_SCAN env)")
	cmd.Flags().BoolVar(&flags.GraphQLPRDetails, "graphql-pr-details", envBool("WATCH_GRAPHQL_PR_DETAILS"), "Have the PR scanner read the pull requests it evaluates (the PR, its merge queue entry, commits, comments, reviews and inline comments) in one GraphQL query per batch rather than six REST calls each; CI stays REST (defaults to the WATCH_GRAPHQL_PR_DETAILS env)")
	cmd.Flags().DurationVar(&flags.PRInactivityTimeout, "pr-inactivity-timeout", common.GetEnvDuration("PR_INACTIVITY_TIMEOUT", 0), "Time of inactivity with no human comments before pausing automated processing on a PR (e.g. '24h', '168h')")

	cmd.AddCommand(newWatchFanoutCommand(ctx))
	cmd.AddCommand(newWatchRulesCommand())

	return cmd
}

// validMode reports whether m is a watch loop mode.
func validMode(m string) bool {
	return m == "enabled" || m == "disabled" || m == "dryrun"
}

// envBool is the environment variable key as a boolean; unset or not a
// boolean is false.
func envBool(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}
