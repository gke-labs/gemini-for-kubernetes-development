package commands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
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
			return runWatch(ctx, parts[0], parts[1], flags.PollInterval, flags.Assignee, cmd.Flags().Changed("assignee"), flags.Labels, flags.DryRun, flags.WatchTimeout, flags.MaxActions, flags.MaxPending, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.Repo, "repo", "", "GitHub repository (e.g. owner/repo)")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval")
	cmd.Flags().StringVar(&flags.Assignee, "assignee", "factory-bot", "GitHub username to watch for assigned issues (use empty string for unassigned issues)")
	cmd.Flags().StringSliceVar(&flags.Labels, "labels", nil, "Comma-separated list of labels to filter issues by")
	cmd.Flags().BoolVar(&flags.DryRun, "dryrun", false, "Print actions without creating sandboxes or executing tasks")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching (default forever)")
	cmd.Flags().IntVar(&flags.MaxActions, "max-actions", 10, "Maximum number of actions to take in a single watch loop")
	cmd.Flags().IntVar(&flags.MaxPending, "max-pending", 40, "Maximum number of pending/running sandboxes allowed before skipping actions")

	return cmd
}

func isAssigned(issue *githubv39.Issue, assignee string) bool {
	if assignee == "" {
		return false
	}
	for _, u := range issue.Assignees {
		if strings.EqualFold(u.GetLogin(), assignee) {
			return true
		}
	}
	return false
}

func isSandboxTaskRunning(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) (bool, error) {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		return true, nil
	}

	state := annotations["sandbox.gemini.google.com/last-task-state"]
	if state == "" || strings.EqualFold(state, "Running") {
		return true, nil
	}

	return false, nil
}

func countRunningSandboxTasks(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) (int, error) {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}

	count := 0
	for _, item := range list.Items {
		annotations := item.GetAnnotations()
		if annotations == nil {
			count++
			continue
		}

		state := annotations["sandbox.gemini.google.com/last-task-state"]
		if state == "" || strings.EqualFold(state, "Running") {
			count++
		}
	}

	return count, nil
}

func runWatch(ctx context.Context, owner, repo string, interval time.Duration, assignee string, assigneeChanged bool, labels []string, dryRun bool, watchTimeout time.Duration, maxActions int, maxPending int, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])

	targetAssignee := assignee
	if !assigneeChanged {
		targetAssignee = githubLogin
	}

	fmt.Printf("Starting watch for repository %s/%s (poll interval: %s, assignee: '%s', labels: %v, dryRun: %v, watchTimeout: %s)...\n", owner, repo, interval, targetAssignee, labels, dryRun, watchTimeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var timeoutChan <-chan time.Time
	if watchTimeout > 0 {
		timeoutChan = time.After(watchTimeout)
	}

	type prWatchState struct {
		lastSHA                  string
		lastInvestigatedTime     time.Time
		lastCommentAddressedTime time.Time
	}

	processedIssues := make(map[int]time.Time)
	processedPRs := make(map[int]prWatchState)

	checkRepo := func() {
		actionsTaken := 0
		unassignedPRs := make(map[int]bool)

		runningCount, err := countRunningSandboxTasks(ctx, kubeClient, rootFlags.Namespace)
		if err != nil {
			klog.Errorf("Failed to count running sandbox tasks: %v", err)
		} else {
			fmt.Printf("Current running sandbox tasks: %d / %d\n", runningCount, maxPending)
			if runningCount >= maxPending {
				fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping this cycle.\n", maxPending)
				return
			}
		}

		// 1. Fetch first 100 open Pull Requests to check for referenced issues
		prOpts := &githubv39.PullRequestListOptions{
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		prs, _, err := ghClient.PullRequests.List(ctx, owner, repo, prOpts)
		referencedIssues := make(map[int]bool)
		if err == nil {
			for _, pr := range prs {
				for num := range getReferencedIssues(pr) {
					referencedIssues[num] = true
				}
			}
		} else {
			klog.Errorf("Failed to list open PRs for referenced issue detection: %v", err)
		}

		// 2. Fetch Issues/PRs matching assignee or labelled "overseer" (using Issues API to fetch both)
		var allItems []*githubv39.Issue
		if targetAssignee != "" {
			opts1 := &githubv39.IssueListByRepoOptions{
				Assignee:    targetAssignee,
				State:       "open",
				ListOptions: githubv39.ListOptions{PerPage: 100},
			}
			issues1, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts1)
			if err != nil {
				klog.Errorf("Failed to list issues for assignee %s: %v", targetAssignee, err)
			} else {
				allItems = append(allItems, issues1...)
			}
		}

		opts2 := &githubv39.IssueListByRepoOptions{
			Labels:      []string{"overseer"},
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		issues2, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, opts2)
		if err != nil {
			klog.Errorf("Failed to list issues for label overseer: %v", err)
		} else {
			allItems = append(allItems, issues2...)
		}

		// Deduplicate and group into issues and PRs
		uniqueIssues := make(map[int]*githubv39.Issue)
		for _, item := range allItems {
			uniqueIssues[item.GetNumber()] = item
		}

		var issues []*githubv39.Issue
		var prIssues []*githubv39.Issue
		for _, item := range uniqueIssues {
			if item.PullRequestLinks != nil {
				prIssues = append(prIssues, item)
			} else {
				issues = append(issues, item)
			}
		}

		// 3. Process Issues
		for _, issue := range issues {
			if actionsTaken >= maxActions {
				fmt.Printf("Reached maximum actions limit (%d) for this cycle. Skipping remaining issues.\n", maxActions)
				break
			}
			if runningCount >= maxPending {
				fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining issues.\n", maxPending)
				break
			}

			num := issue.GetNumber()
			if referencedIssues[num] {
				klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
				continue
			}
			if lastProcessed, ok := processedIssues[num]; !ok || time.Since(lastProcessed) > 24*time.Hour {
				linked, err := hasLinkedPR(ctx, ghClient, owner, repo, num)
				if err != nil {
					klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
				} else if linked {
					klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
					continue
				}

				sandboxName := fmt.Sprintf("fix-%s-%d", repo, num)
				running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
				if err != nil {
					klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
				} else if running {
					klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
					continue
				}

				if isAssigned(issue, targetAssignee) {
					if dryRun {
						fmt.Printf("[DRYRUN] Would unassign %s from issue #%d\n", targetAssignee, num)
					} else {
						fmt.Printf("Unassigning %s from issue #%d...\n", targetAssignee, num)
						if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
							klog.Errorf("Failed to unassign %s from issue #%d: %v", targetAssignee, num, err)
						}
					}
				}

				issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
				if dryRun {
					fmt.Printf("[DRYRUN] Would trigger fix for issue #%d (%s): %s\n", num, issue.GetTitle(), issueURL)
					actionsTaken++
					runningCount++
				} else {
					fmt.Printf("\nFound assigned issue #%d (%s). Triggering fix...\n", num, issue.GetTitle())
					processedIssues[num] = time.Now()
					actionsTaken++
					runningCount++
					if err := runFix(ctx, issueURL, "Fix this issue", "", false, false, 0, watchTimeout, ephemeralStorage, secrets); err != nil {
						klog.Errorf("Fix for issue #%d failed: %v", num, err)
					}
				}
			}
		}

		// 4. Process Pull Requests
		for _, prIssue := range prIssues {
			if actionsTaken >= maxActions {
				fmt.Printf("Reached maximum actions limit (%d) for this cycle. Skipping remaining PRs.\n", maxActions)
				break
			}
			if runningCount >= maxPending {
				fmt.Printf("Reached maximum pending sandboxes limit (%d). Skipping remaining PRs.\n", maxPending)
				break
			}

			num := prIssue.GetNumber()

			// Fetch full Pull Request to get HEAD SHA and other details
			pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
			if err != nil {
				klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
				continue
			}

			headSHA := pr.GetHead().GetSHA()

			// Check 3.1: Check CI check runs and commit statuses for failures
			hasFailure := false
			checkRuns, err := listAllCheckRuns(ctx, ghClient, owner, repo, headSHA)
			if err == nil {
				for _, run := range checkRuns {
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

			state := processedPRs[num]

			if hasFailure {
				if state.lastSHA != headSHA || time.Since(state.lastInvestigatedTime) > 6*time.Hour {
					sandboxName := fmt.Sprintf("factory-pr-%d", num)
					running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
					if err != nil {
						klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
					} else if running {
						klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
					} else {
						if isAssigned(prIssue, targetAssignee) && !unassignedPRs[num] {
							if dryRun {
								fmt.Printf("[DRYRUN] Would unassign %s from PR #%d\n", targetAssignee, num)
							} else {
								fmt.Printf("Unassigning %s from PR #%d...\n", targetAssignee, num)
								if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
									klog.Errorf("Failed to unassign %s from PR #%d: %v", targetAssignee, num, err)
								}
								unassignedPRs[num] = true
							}
						}

						prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
						if dryRun {
							fmt.Printf("[DRYRUN] Would trigger investigate for PR #%d (%s) (SHA: %s): %s\n", num, pr.GetTitle(), headSHA[:7], prURL)
							actionsTaken++
							runningCount++
						} else {
							fmt.Printf("\nFound failing PR #%d (%s) (SHA: %s). Triggering investigate...\n", num, pr.GetTitle(), headSHA[:7])
							state.lastSHA = headSHA
							state.lastInvestigatedTime = time.Now()
							processedPRs[num] = state
							actionsTaken++
							runningCount++
							if err := runInvestigate(ctx, prURL, "Investigate check failures for this PR", false, ephemeralStorage, secrets); err != nil {
								klog.Errorf("Investigate for PR #%d failed: %v", num, err)
							}
						}
					}
				}
			}

			if actionsTaken >= maxActions {
				continue
			}
			if runningCount >= maxPending {
				continue
			}

			// Check 3.2: Check new review comments/feedback after latest commit
			prCommits, _, err := ghClient.PullRequests.ListCommits(ctx, owner, repo, num, nil)
			if err == nil {
				var lastCommitTime time.Time
				for _, c := range prCommits {
					if c.GetCommit().GetAuthor().GetDate().After(lastCommitTime) {
						lastCommitTime = c.GetCommit().GetAuthor().GetDate()
					}
				}

				comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, num, nil)
				if err == nil {
					hasNewComments := false
					for _, c := range comments {
						// Ignore comments from bot
						if strings.Contains(strings.ToLower(c.GetUser().GetLogin()), "bot") {
							continue
						}
						if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(state.lastCommentAddressedTime) {
							hasNewComments = true
							break
						}
					}

					if hasNewComments {
						sandboxName := fmt.Sprintf("factory-pr-%d", num)
						running, err := isSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
						} else if running {
							klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
						} else {
							if isAssigned(prIssue, targetAssignee) && !unassignedPRs[num] {
								if dryRun {
									fmt.Printf("[DRYRUN] Would unassign %s from PR #%d\n", targetAssignee, num)
								} else {
									fmt.Printf("Unassigning %s from PR #%d...\n", targetAssignee, num)
									if _, _, err := ghClient.Issues.RemoveAssignees(ctx, owner, repo, num, []string{targetAssignee}); err != nil {
										klog.Errorf("Failed to unassign %s from PR #%d: %v", targetAssignee, num, err)
									}
									unassignedPRs[num] = true
								}
							}

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)
							if dryRun {
								fmt.Printf("[DRYRUN] Would trigger address-comments for PR #%d (%s): %s\n", num, pr.GetTitle(), prURL)
								actionsTaken++
								runningCount++
							} else {
								fmt.Printf("\nFound new review comments for PR #%d (%s). Triggering address-comments...\n", num, pr.GetTitle())
								state.lastCommentAddressedTime = time.Now()
								processedPRs[num] = state
								actionsTaken++
								runningCount++
								if err := runAddressComments(ctx, prURL, "Address review feedback for this PR", false, ephemeralStorage, secrets); err != nil {
									klog.Errorf("Address-comments for PR #%d failed: %v", num, err)
								}
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
		case <-timeoutChan:
			fmt.Printf("\nWatch timeout of %s expired. Stopping watch.\n", watchTimeout)
			return nil
		case <-ticker.C:
			checkRepo()
		}
	}
}

func getReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\b\d+\b`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil {
				referenced[num] = true
			}
		}
	}

	// Check title and body
	re := regexp.MustCompile(`#(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

func hasLinkedPR(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int) (bool, error) {
	timeline, _, err := client.Issues.ListIssueTimeline(ctx, owner, repo, issueNum, nil)
	if err != nil {
		return false, err
	}
	for _, event := range timeline {
		if event.GetEvent() == "cross-referenced" && event.Source != nil {
			if event.Source.Issue != nil {
				if event.GetSource().GetType() == "issue" {
					continue
				}
				if event.Source.Issue.PullRequestLinks != nil {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
