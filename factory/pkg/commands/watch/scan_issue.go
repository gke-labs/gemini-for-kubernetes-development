package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func (w *Watcher) queueIssueTasks(ctx context.Context, issues []*githubv39.Issue, refIssues map[int]bool) {
	klog.Infof("queueIssueTasks called with %d issues", len(issues))
	for _, issue := range issues {
		num := issue.GetNumber()
		if w.cfg != nil && w.cfg.MinNumber > 0 && num < w.cfg.MinNumber {
			continue
		}
		if hasStopLabel(issue.Labels, w.triggerLabel) {
			klog.Infof("Skipping issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, w.triggerLabel)
			_ = w.queueMgr.RemovePendingTasksForNumber(num)
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
			if common.IsWorkflowDefinition(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, workflowPath) {
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

		if w.queueMgr.TaskExists(filename) {
			continue
		}

		// Check if the workflow session already completed recently
		processedPath := filepath.Join(w.processedDir, filename)
		if info, err := os.Stat(processedPath); err == nil {
			lastRunTime := info.ModTime()
			if data, err := os.ReadFile(processedPath); err == nil {
				var t api.QueueTask
				if err := yaml.Unmarshal(data, &t); err == nil && !t.CompletedAt.IsZero() {
					lastRunTime = t.CompletedAt
				}
			}
			cooldown := common.GetWorkflowCooldown(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, workflowPath)
			if time.Since(lastRunTime) < cooldown {
				continue
			}
		}

		lastProcessed, ok := w.processedIssues[num]
		if !ok || issue.GetUpdatedAt().After(lastProcessed) || workflowName != "" {
			var timeline []*githubv39.Timeline
			if w.ghClient != nil {
				tl, _, err := w.ghClient.Issues.ListIssueTimeline(ctx, w.Repo.Owner, w.Repo.Repo, num, nil)
				if err == nil {
					timeline = tl
				}
			}

			// Skip KRM check for workflow triggers since they don't necessarily have linked code PRs
			if workflowName == "" {
				linked, err := hasLinkedPRWithTimeline(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, timeline)
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

			running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
				continue
			} else if running {
				klog.Infof("Skipping issue #%d because there is an in-flight sandbox %s.", num, sandboxName)
				continue
			}

			hasTriggerLabel := false
			for _, label := range issue.Labels {
				if strings.EqualFold(label.GetName(), w.triggerLabel) {
					hasTriggerLabel = true
					break
				}
			}
			wasAutoLabeled := false
			if !hasTriggerLabel {
				wasAutoLabeled = true
				if w.DryRun {
					fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d\n", w.triggerLabel, num)
				} else {
					klog.Infof("Adding '%s' label to issue #%d", w.triggerLabel, num)
					if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{w.triggerLabel}); err != nil {
						klog.Errorf("Failed to add label '%s' to issue #%d: %v", w.triggerLabel, num, err)
					}
				}
			}

			triggerEventTime, triggerReason, triggerNotes := getIssueTriggerInfo(issue, timeline, w.triggerLabel, wasAutoLabeled)

			taskType := api.TypeIssueFix
			if workflowName != "" {
				taskType = api.TypeAgentChore
			}

			taskAssignee, err := w.selectUserForTask(ctx, taskType, num)
			if err != nil {
				klog.Errorf("Failed to select user for issue #%d: %v", num, err)
				taskAssignee = w.targetAssignee
			}
			if taskAssignee == "" {
				taskAssignee = w.targetAssignee
			}

			var task *api.QueueTask
			if workflowName != "" {
				task = w.newIssueQueueTask(IssueTaskOptions{
					Type:             api.TypeAgentChore,
					Issue:            issue,
					Phase:            api.PhaseChores,
					Assignee:         taskAssignee,
					TriggerEventTime: triggerEventTime,
					TriggerReason:    triggerReason,
					TriggerNotes:     triggerNotes,
					AgentFile:        workflowPath,
					SessionID:        fmt.Sprintf("issue-%d", num),
				})
			} else {
				task = w.newIssueQueueTask(IssueTaskOptions{
					Type:             api.TypeIssueFix,
					Issue:            issue,
					Phase:            api.PhaseInvestigate,
					Assignee:         taskAssignee,
					TriggerEventTime: triggerEventTime,
					TriggerReason:    triggerReason,
					TriggerNotes:     triggerNotes,
				})
			}

			if w.DryRun {
				if workflowName != "" {
					fmt.Printf("[DRYRUN] Would queue workflow task %s for issue #%d: %s\n", workflowName, num, task.URL)
				} else {
					fmt.Printf("[DRYRUN] Would queue fix task for issue #%d: %s\n", num, task.URL)
				}
			} else {
				if workflowName != "" {
					fmt.Printf("Queueing workflow task %s for issue #%d...\n", workflowName, num)
				} else {
					fmt.Printf("Queueing fix task for issue #%d...\n", num)
				}
				w.processedIssues[num] = time.Now()
				if err := w.queueMgr.Enqueue(filename, task); err != nil {
					klog.Errorf("Failed to queue task for issue #%d: %v", num, err)
				}
			}
		}
	}
}

func (w *Watcher) scanSlowIssues(ctx context.Context) ([]*githubv39.Issue, error) {
	var slowIssues []*githubv39.Issue
	opts := &githubv39.IssueListByRepoOptions{
		Labels:      []string{w.triggerLabel},
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		pageIssues, resp, err := w.ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts)
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

func (w *Watcher) scanFastIssues(ctx context.Context) ([]*githubv39.Issue, []*githubv39.Issue, error) {
	var allItems []*githubv39.Issue
	limit := w.ScanLimit
	if limit <= 0 {
		limit = 30
	}

	for _, botUser := range w.allBotUsers {
		opts1 := &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: limit},
		}
		issues1, _, err := w.ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts1)
		if err != nil {
			klog.Errorf("Failed to list issues for assignee %s: %v", botUser, err)
		} else {
			klog.Infof("Fetched %d issues assigned to %s from GitHub API", len(issues1), botUser)
			allItems = append(allItems, issues1...)
		}
	}

	if w.githubLogin != "" {
		optsCreator := &githubv39.IssueListByRepoOptions{
			Creator:     w.githubLogin,
			State:       "open",
			Sort:        "updated",
			Direction:   "desc",
			ListOptions: githubv39.ListOptions{PerPage: limit},
		}
		issuesCreator, _, err := w.ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, optsCreator)
		if err != nil {
			klog.Errorf("Failed to list issues created by %s: %v", w.githubLogin, err)
		} else {
			klog.Infof("Fetched %d issues created by %s from GitHub API", len(issuesCreator), w.githubLogin)
			for _, issue := range issuesCreator {
				if issue.PullRequestLinks != nil {
					continue
				}
				if hasStopLabel(issue.Labels, w.triggerLabel) {
					klog.Infof("Skipping auto labeling/assigning issue #%d because it has the stop label ('overseer/stop' or '%s/stop')", issue.GetNumber(), w.triggerLabel)
					continue
				}

				hasTriggerLabel := false
				for _, l := range issue.Labels {
					if strings.EqualFold(l.GetName(), w.triggerLabel) {
						hasTriggerLabel = true
						break
					}
				}

				hasAssignee := false
				for _, u := range issue.Assignees {
					for _, bot := range w.allBotUsers {
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
						fmt.Printf("[DRYRUN] Would label issue #%d created by %s with '%s' and assign to %s\n", issue.GetNumber(), w.githubLogin, w.triggerLabel, w.targetAssignee)
					} else {
						fmt.Printf("Labelling issue #%d created by %s with '%s' and assigning to %s...\n", issue.GetNumber(), w.githubLogin, w.triggerLabel, w.targetAssignee)
						if !hasTriggerLabel {
							if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, issue.GetNumber(), []string{w.triggerLabel}); err != nil {
								klog.Errorf("Failed to add label '%s' to issue #%d: %v", w.triggerLabel, issue.GetNumber(), err)
							} else {
								issue.Labels = append(issue.Labels, &githubv39.Label{Name: githubv39.String(w.triggerLabel)})
							}
						}
						if !hasAssignee && w.targetAssignee != "" {
							if _, _, err := w.ghClient.Issues.AddAssignees(ctx, w.Repo.Owner, w.Repo.Repo, issue.GetNumber(), []string{w.targetAssignee}); err != nil {
								klog.Errorf("Failed to assign %s to issue #%d: %v", w.targetAssignee, issue.GetNumber(), err)
							} else {
								issue.Assignees = append(issue.Assignees, &githubv39.User{Login: githubv39.String(w.targetAssignee)})
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
