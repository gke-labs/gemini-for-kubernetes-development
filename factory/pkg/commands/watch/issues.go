package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

func queueIssueTasks(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, cfg *config.FactoryConfig, owner, repo string, issues []*githubv39.Issue, processedIssues map[int]time.Time, refIssues map[int]bool, targetAssignee string, allBotUsers []string, incomingDir, processingDir, processedDir, queueDir string, dryRun bool, triggerLabel string, namespace string) {
	klog.Infof("queueIssueTasks called with %d issues", len(issues))
	for _, issue := range issues {
		num := issue.GetNumber()
		if cfg != nil && cfg.MinNumber > 0 && num < cfg.MinNumber {
			continue
		}
		if refIssues[num] {
			klog.Infof("Skipping issue #%d because there is already a PR referencing it.", num)
			continue
		}

		// Check if the issue specifies a workflow path in its description
		workflowPath := FindWorkflowPath(issue.GetBody())
		workflowName := ""
		if workflowPath != "" {
			if IsWorkflowDefinition(ctx, ghClient, owner, repo, workflowPath) {
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
			filename = fmt.Sprintf("task-workflow-%s-issue-%d.yaml", Slugify(workflowName), num)
		}

		if taskExists(incomingDir, processingDir, filename) {
			continue
		}

		// Check if the workflow session already completed recently
		processedPath := filepath.Join(processedDir, filename)
		if info, err := os.Stat(processedPath); err == nil {
			cooldown := workflowCooldown(ctx, ghClient, owner, repo, workflowPath)
			if time.Since(info.ModTime()) < cooldown {
				continue
			}
		}

		lastProcessed, ok := processedIssues[num]
		if !ok || issue.GetUpdatedAt().After(lastProcessed) || workflowName != "" {
			// Skip KRM check for workflow triggers since they don't necessarily have linked code PRs
			if workflowName == "" {
				linked, err := hasLinkedPR(ctx, ghClient, owner, repo, num)
				if err != nil {
					klog.Errorf("Failed to check linked PR for issue #%d: %v", num, err)
					continue
				} else if linked {
					klog.Infof("Skipping issue #%d because it has a linked PR according to the Timeline API.", num)
					continue
				}
			}

			sandboxName := fmt.Sprintf("fix-%s-%d", repo, num)
			if workflowName != "" {
				sandboxName = fmt.Sprintf("wf-issue-%d", num)
			}

			running, err := isSandboxTaskRunning(ctx, kubeClient, namespace, sandboxName)
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
				if dryRun {
					fmt.Printf("[DRYRUN] Would add label '%s' to issue #%d\n", triggerLabel, num)
				} else {
					klog.Infof("Adding '%s' label to issue #%d", triggerLabel, num)
					if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, num, []string{triggerLabel}); err != nil {
						klog.Errorf("Failed to add label '%s' to issue #%d: %v", triggerLabel, num, err)
					}
				}
			}

			taskType := "issue-fix"
			if workflowName != "" {
				taskType = "agent-chore"
			}

			taskAssignee, err := selectUserForTask(ctx, ghClient, kubeClient, cfg, taskType, num, owner, repo, namespace)
			if err != nil {
				klog.Errorf("Failed to select user for issue #%d: %v", num, err)
				taskAssignee = targetAssignee
			}
			if taskAssignee == "" {
				taskAssignee = targetAssignee
			}

			issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)
			var task *QueueTask
			if workflowName != "" {
				task = &QueueTask{
					Type:      "agent-chore",
					URL:       issueURL,
					Number:    num,
					Priority:  issuePriority(issue),
					Phase:     4,
					CreatedAt: issue.GetCreatedAt(),
					Assignee:  taskAssignee,
					Status:    "Pending",
					AgentFile: workflowPath,
					SessionID: fmt.Sprintf("issue-%d", num),
				}
			} else {
				task = &QueueTask{
					Type:      "issue-fix",
					URL:       issueURL,
					Number:    num,
					Priority:  issuePriority(issue),
					Phase:     3,
					CreatedAt: issue.GetCreatedAt(),
					Assignee:  taskAssignee,
					Status:    "Pending",
				}
			}

			if dryRun {
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
					writeTaskJournalEvent(queueDir, filename, task, "Created", 0)
				}
			}
		}
	}
}
