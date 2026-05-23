package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type WatchFlags struct {
	Repo         string
	PollInterval time.Duration
	Assignee     string
	Labels       []string
	DryRun       bool
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
		RunE: func(_ *cobra.Command, _ []string) error {
			if flags.Repo == "" {
				return fmt.Errorf("--repo is required (e.g. owner/repo)")
			}
			parts := strings.Split(flags.Repo, "/")
			if len(parts) != 2 {
				return fmt.Errorf("invalid repo format, expected owner/repo, got %s", flags.Repo)
			}
			return runWatch(ctx, parts[0], parts[1], flags.PollInterval, flags.Assignee, flags.Labels, flags.DryRun)
		},
	}

	cmd.Flags().StringVar(&flags.Repo, "repo", "", "GitHub repository (e.g. owner/repo)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().StringVar(&flags.Assignee, "assignee", "factory-bot", "GitHub username to watch for assigned issues (use empty string for unassigned issues)")
	cmd.Flags().StringSliceVar(&flags.Labels, "labels", nil, "Comma-separated list of labels to filter issues by")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")

	return cmd
}

func runWatch(ctx context.Context, owner, repo string, interval time.Duration, assignee string, labels []string, dryRun bool) error {
	fmt.Printf("Starting watch for repository %s/%s (poll interval: %s, assignee: '%s', labels: %v, dryRun: %v)...\n", owner, repo, interval, assignee, labels, dryRun)

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	type prWatchState struct {
		lastSHA          string
		lastInvestigated time.Time
	}

	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)

	checkRepo := func() {
		assigneeFilter := assignee
		if assigneeFilter == "" {
			assigneeFilter = "none"
		}
		opts := &githubv39.IssueListByRepoOptions{
			Assignee:    assigneeFilter,
			State:       "open",
			Labels:      labels,
			ListOptions: githubv39.ListOptions{PerPage: 50},
		}
		issues, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			klog.Errorf("Failed to list issues: %v", err)
		} else {
			for _, issue := range issues {
				if issue.PullRequestLinks != nil {
					continue
				}
				num := issue.GetNumber()
				if lastProcessed, ok := processedIssues[num]; !ok || time.Since(lastProcessed) > 24*time.Hour {
					fmt.Printf("\nFound assigned issue #%d (%s). Triggering fix...\n", num, issue.GetTitle())
					processedIssues[num] = time.Now()
					issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
					if dryRun {
						fmt.Printf("[DRYRUN] Would trigger fix for issue #%d: %s\n", num, issueURL)
					} else {
						if err := runFix(ctx, issueURL, "Fix this issue", "", false); err != nil {
							klog.Errorf("Fix for issue #%d failed: %v", num, err)
						}
					}
				}
			}
		}

		prOpts := &githubv39.PullRequestListOptions{
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 50},
		}
		prs, _, err := ghClient.PullRequests.List(ctx, owner, repo, prOpts)
		if err != nil {
			klog.Errorf("Failed to list PRs: %v", err)
		} else {
			for _, pr := range prs {
				num := pr.GetNumber()
				headSHA := pr.GetHead().GetSHA()

				hasFailure := false
				checks, _, err := ghClient.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, nil)
				if err == nil {
					for _, run := range checks.CheckRuns {
						if run.GetConclusion() == "failure" {
							hasFailure = true
							break
						}
					}
				}

				statuses, _, err := ghClient.Repositories.ListStatuses(ctx, owner, repo, headSHA, nil)
				if err == nil {
					for _, status := range statuses {
						if status.GetState() == "failure" || status.GetState() == "error" {
							hasFailure = true
							break
						}
					}
				}

				if hasFailure {
					state, ok := processedPRs[num]
					if !ok || headSHA != state.lastSHA || time.Since(state.lastInvestigated) > 6*time.Hour {
						fmt.Printf("\nFound failing PR #%d (%s) (SHA: %s). Triggering investigate & review...\n", num, pr.GetTitle(), headSHA[:7])
						processedPRs[num] = prWatchState{
							lastSHA:          headSHA,
							lastInvestigated: time.Now(),
						}
						prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
						if dryRun {
							fmt.Printf("[DRYRUN] Would trigger investigate for PR #%d: %s\n", num, prURL)
						} else {
							if err := runInvestigate(ctx, prURL, "Investigate check failures for this PR"); err != nil {
								klog.Errorf("Investigate for PR #%d failed: %v", num, err)
							}
						}
					}
				}
			}
		}
	}

	// Run first check immediately
	checkRepo()

	for {
		fmt.Printf("Sleeping for %s...\n", interval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			checkRepo()
		}
	}
}
