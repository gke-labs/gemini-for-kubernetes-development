package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func (w *Watcher) queueIssueTasks(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, cfg *config.FactoryConfig, issues []*githubv39.Issue, processedIssues map[int]time.Time, refIssues map[int]bool, targetAssignee string, allBotUsers []string, incomingDir, processingDir, processedDir string, triggerLabel string) {
	klog.Infof("queueIssueTasks called with %d issues", len(issues))
	for _, issue := range issues {
		num := issue.GetNumber()
		if cfg != nil && cfg.MinNumber > 0 && num < cfg.MinNumber {
			continue
		}
		if hasStopLabel(issue.Labels, triggerLabel) {
			klog.Infof("Skipping issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, triggerLabel)
			removePendingTasksForNumber(incomingDir, num)
			continue
		}
		if refIssues[num] {
			klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
			continue
		}

		// Check if the issue specifies a workflow path in its description
		workflowPath := common.FindWorkflowPath(issue.GetBody())
		workflowName := ""
		if workflowPath != "" {
			if common.IsWorkflowDefinition(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, workflowPath) {
				filenameOnly := filepath.Base(workflowPath)
				ext := filepath.Ext(filenameOnly)
				workflowName = strings.TrimSuffix(filenameOnly, ext)
			} else {
				// It was just a standard skill/agent prompt mentioned, not a workflow.
				// Fallback to standard issue-fix
				workflowPath = ""
			}
		}

		filename := fmt.Sprintf("task-issue-%d.yaml", num)
		if workflowName != "" {
			filename = fmt.Sprintf("task-workflow-%s-issue-%d.yaml", common.Slugify(workflowName), num)
		}

		if taskExists(incomingDir, processingDir, filename) {
			continue
		}

		// Check if the workflow session already completed recently
		processedPath := filepath.Join(processedDir, filename)
		if info, err := os.Stat(processedPath); err == nil {
			lastRunTime := info.ModTime()
			if data, err := os.ReadFile(processedPath); err == nil {
				var t QueueTask
				if err := yaml.Unmarshal(data, &t); err == nil && !t.CompletedAt.IsZero() {
					lastRunTime = t.CompletedAt
				}
			}
			cooldown := common.GetWorkflowCooldown(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, workflowPath)
			if time.Since(lastRunTime) < cooldown {
				continue
			}
		}

		lastProcessed, ok := processedIssues[num]
		if !ok || issue.GetUpdatedAt().After(lastProcessed) || workflowName != "" {
			// Skip KRM check for workflow triggers since they don't necessarily have linked code PRs
			if workflowName == "" {
				linked, err := hasLinkedPR(ctx, ghClient, w.Repo.Owner, w.Repo.Repo, num)
				if err != nil {
					klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
					continue
				} else if linked {
					klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
					continue
				}
			}

			sandboxName := fmt.Sprintf("fix-%s-%d", w.Repo.Repo, num)
			if workflowName != "" {
				sandboxName = fmt.Sprintf("wf-issue-%d", num)
			}

			running, err := isSandboxTaskRunning(ctx, kubeClient, w.Namespace, sandboxName)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
				continue
			} else if running {
				klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
				continue
			}

			hasTriggerLabel := false
			for _, label := range issue.Labels {
				if strings.EqualFold(label.GetName(), triggerLabel) {
					hasTriggerLabel = true
					break
				}
			}
			if !hasTriggerLabel {
				if w.DryRun {
					fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d\n", triggerLabel, num)
				} else {
					klog.Infof("Adding '%s' label to issue #%d", triggerLabel, num)
					if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{triggerLabel}); err != nil {
						klog.Errorf("Failed to add label '%s' to issue #%d: %v", triggerLabel, num, err)
					}
				}
			}

			taskType := "issue-fix"
			if workflowName != "" {
				taskType = "agent-chore"
			}

			taskAssignee, err := w.selectUserForTask(ctx, ghClient, kubeClient, cfg, taskType, num)
			if err != nil {
				klog.Errorf("Failed to select user for issue #%d: %v", num, err)
				taskAssignee = targetAssignee
			}
			if taskAssignee == "" {
				taskAssignee = targetAssignee
			}

			issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", w.Repo.Owner, w.Repo.Repo, num)
			var task *QueueTask
			if workflowName != "" {
				task = &QueueTask{
					Type:       "agent-chore",
					URL:        issueURL,
					Number:     num,
					Priority:   getIssuePriority(issue),
					Phase:      4,
					CreatedAt:  issue.GetCreatedAt(),
					EnqueuedAt: time.Now(),
					Assignee:   taskAssignee,
					Status:     "Pending",
					AgentFile:  workflowPath,
					SessionID:  fmt.Sprintf("issue-%d", num),
				}
			} else {
				task = &QueueTask{
					Type:       "issue-fix",
					URL:        issueURL,
					Number:     num,
					Priority:   getIssuePriority(issue),
					Phase:      3,
					CreatedAt:  issue.GetCreatedAt(),
					EnqueuedAt: time.Now(),
					Assignee:   taskAssignee,
					Status:     "Pending",
				}
			}

			if w.DryRun {
				if workflowName != "" {
					fmt.Printf("[DRYRUN] Would queue workflow task %s for issue #%d: %s\n", workflowName, num, issueURL)
				} else {
					fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, issueURL)
				}
			} else {
				if workflowName != "" {
					fmt.Printf("Queueing workflow task %s for issue #%d...\n", workflowName, num)
				} else {
					fmt.Printf("Queueing fix task for issue #%d...\n", num)
				}
				processedIssues[num] = time.Now()
				if err := writeTaskAtomically(incomingDir, filename, task); err != nil {
					klog.Errorf("Failed to queue task for issue #%d: %v", num, err)
				} else {
					writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
				}
			}
		}
	}
}

func (w *Watcher) scanSlowIssues(ctx context.Context, ghClient *githubv39.Client, triggerLabel string) ([]*githubv39.Issue, error) {
	var slowIssues []*githubv39.Issue
	opts := &githubv39.IssueListByRepoOptions{
		Labels:      []string{triggerLabel},
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		pageIssues, resp, err := ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts)
		if err != nil {
			return slowIssues, err
		}
		for _, item := range pageIssues {
			if item.PullRequestLinks == nil {
				slowIssues = append(slowIssues, item)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return slowIssues, nil
}

func (w *Watcher) scanFastIssues(ctx context.Context, ghClient *githubv39.Client, allBotUsers []string, githubLogin, triggerLabel, targetAssignee string) ([]*githubv39.Issue, []*githubv39.Issue, error) {
	var allItems []*githubv39.Issue
	limit := w.ScanLimit
	if limit <= 0 {
		limit = 30
	}

	for _, botUser := range allBotUsers {
		opts1 := &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: limit},
		}
		issues1, _, err := ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts1)
		if err != nil {
			klog.Errorf("Failed to list issues for assignee %s: %v", botUser, err)
		} else {
			klog.Infof("Fetched %d issues assigned to %s from GitHub API", len(issues1), botUser)
			allItems = append(allItems, issues1...)
		}
	}

	if githubLogin != "" {
		optsCreator := &githubv39.IssueListByRepoOptions{
			Creator:     githubLogin,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: limit},
		}
		issuesCreator, _, err := ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, optsCreator)
		if err != nil {
			klog.Errorf("Failed to list issues created by %s: %v", githubLogin, err)
		} else {
			klog.Infof("Fetched %d issues created by %s from GitHub API", len(issuesCreator), githubLogin)
			for _, issue := range issuesCreator {
				if issue.PullRequestLinks != nil {
					continue
				}
				if hasStopLabel(issue.Labels, triggerLabel) {
					klog.Infof("Skipping auto labeling/assigning issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", issue.GetNumber(), triggerLabel)
					continue
				}

				hasTriggerLabel := false
				for _, l := range issue.Labels {
					if strings.EqualFold(l.GetName(), triggerLabel) {
						hasTriggerLabel = true
						break
					}
				}

				hasAssignee := false
				for _, u := range issue.Assignees {
					for _, bot := range allBotUsers {
						if strings.EqualFold(u.GetLogin(), bot) {
							hasAssignee = true
							break
						}
					}
					if hasAssignee {
						break
					}
				}

				if !hasTriggerLabel || !hasAssignee {
					if w.DryRun {
						fmt.Printf("[DRYRUN] Would label issue #%d created by %s with '%s' and assign to %s\n", issue.GetNumber(), githubLogin, triggerLabel, targetAssignee)
					} else {
						fmt.Printf("Labelling issue #%d created by %s with '%s' and assigning to %s...\n", issue.GetNumber(), githubLogin, triggerLabel, targetAssignee)
						if !hasTriggerLabel {
							if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, issue.GetNumber(), []string{triggerLabel}); err != nil {
								klog.Errorf("Failed to add label '%s' to issue #%d: %v", triggerLabel, issue.GetNumber(), err)
							} else {
								issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(triggerLabel)})
							}
						}
						if !hasAssignee && targetAssignee != "" {
							if _, _, err := ghClient.Issues.AddAssignees(ctx, w.Repo.Owner, w.Repo.Repo, issue.GetNumber(), []string{targetAssignee}); err != nil {
								klog.Errorf("Failed to assign %s to issue #%d: %v", targetAssignee, issue.GetNumber(), err)
							} else {
								issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(targetAssignee)})
							}
						}
					}
				}
				allItems = append(allItems, issue)
			}
		}
	}

	uniqueIssues := make(map[int]*githubv39.Issue)
	for _, item := range allItems {
		uniqueIssues[item.GetNumber()] = item
	}

	var issues []*githubv39.Issue
	var fastPRIssues []*githubv39.Issue
	for _, item := range uniqueIssues {
		if item.PullRequestLinks == nil {
			issues = append(issues, item)
		} else {
			fastPRIssues = append(fastPRIssues, item)
		}
	}

	return issues, fastPRIssues, nil
}
