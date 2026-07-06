package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func (w *watchContext) processPRs(ctx context.Context, prIssues []*githubv39.Issue) {
	if w.opts.PRMode == "disabled" {
		return
	}
	unassignedPRs := make(map[int]bool)

	for _, prIssue := range prIssues {
		num := prIssue.GetNumber()
		if w.cfg != nil && w.cfg.MinNumber > 0 && num < w.cfg.MinNumber {
			continue
		}
		pr, _, err := w.ghClient.PullRequests.Get(ctx, w.opts.Owner, w.opts.Repo, num)
		if err != nil {
			klog.Errorf("Failed to fetch full PR #%d: %v", num, err)
			continue
		}

		// Verify PR Author: Only process PRs created by any bot in the pool
		author := pr.GetUser().GetLogin()
		isBotPR := false
		for _, bot := range w.allBotUsers {
			if strings.EqualFold(author, bot) {
				isBotPR = true
				break
			}
		}
		if !isBotPR {
			klog.Infof("Skipping PR #%d because it was created by %s (not in our bot pool). We do not have permission to push to external forks.", num, author)
			continue
		}

		// Sync labels from referenced parent issues to the PR
		syncReferencedIssueLabels(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, pr, prIssue)

		headSHA := pr.GetHead().GetSHA()

		// Fetch PR commits to find the last commit timestamp
		prCommits, err := github.ListAllCommits(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, num)
		var lastCommitTime time.Time
		if err == nil {
			for _, c := range prCommits {
				if c.GetCommit().GetCommitter().GetDate().After(lastCommitTime) {
					lastCommitTime = c.GetCommit().GetCommitter().GetDate()
				}
			}
		}

		// Fetch all PR comments (handling pagination)
		comments, listCommentsErr := github.ListAllIssueComments(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, num)

		// Check Phase 1: Rebase/Conflicts
		isConflicting := pr.Mergeable != nil && !*pr.Mergeable

		if isConflicting {
			filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
			if !taskExists(w.incomingDir, w.processingDir, filename) {
				sandboxName := resolveSandboxName(ctx, w.kubeClient, w.ghClient, "pr-iterate", num, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
				running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.opts.Namespace, sandboxName)
				if err != nil {
					klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
					continue
				} else if running {
					klog.Infof("Skipping PR #%d rebase because there is an in-flight sandbox %s.", num, sandboxName)
				} else {
					assignedBot := assignedBotUser(prIssue, w.allBotUsers)

					taskAssignee := assignedBot
					if taskAssignee == "" {
						taskAssignee = author
					}

					prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.opts.Owner, w.opts.Repo, num)
					task := &QueueTask{
						Type:      "pr-iterate",
						URL:       prURL,
						Number:    num,
						Priority:  prPriority(prIssue),
						Phase:     1,
						CreatedAt: pr.GetCreatedAt(),
						Assignee:  taskAssignee,
						Status:    "Pending",
						CommitSHA: headSHA,
					}

					if w.opts.DryRun {
						fmt.Printf("[DRYRUN] Would queue rebase task for PR #%d: %s\n", num, prURL)
					} else {
						fmt.Printf("Queueing rebase task for PR #%d...\n", num)
						if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
							klog.Errorf("Failed to queue rebase task for PR #%d: %v", num, err)
						} else {
							writeTaskJournalEvent(w.opts.QueueDir, filename, task, "Created", 0)
						}
					}
				}
			}
			// If conflicting, we prioritize rebase and skip other PR checks for this PR in this loop
			continue
		}

		// Check CI Check Failures
		hasFailure := false
		checkRuns, err := listAllCheckRuns(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, headSHA)
		if err == nil {
			for _, run := range checkRuns {
				c := run.GetConclusion()
				if c == "failure" || c == "timed_out" || c == "cancelled" {
					hasFailure = true
					break
				}
			}
		}

		statuses, _, err := w.ghClient.Repositories.ListStatuses(ctx, w.opts.Owner, w.opts.Repo, headSHA, nil)
		if err == nil {
			for _, status := range statuses {
				if status.GetState() == "failure" || status.GetState() == "error" {
					hasFailure = true
					break
				}
			}
		}

		state := w.processedPRs[num]

		assignedBot := assignedBotUser(prIssue, w.allBotUsers)
		isExplicitlyAssigned := assignedBot != "" && !unassignedPRs[num]

		if state.lastSHA != "" && state.lastSHA != headSHA {
			if assignedBot != "" && !unassignedPRs[num] {
				if w.opts.DryRun {
					fmt.Printf("[DRYRUN] Would unassign stale bot %s from PR #%d due to new commit %s\n", assignedBot, num, headSHA)
				} else {
					fmt.Printf("Unassigning stale bot %s from PR #%d due to new commit %s...\n", assignedBot, num, headSHA)
					if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.opts.Owner, w.opts.Repo, num, []string{assignedBot}); err != nil {
						klog.Errorf("Failed to unassign stale bot %s from PR #%d: %v", assignedBot, num, err)
					}
					unassignedPRs[num] = true
					isExplicitlyAssigned = false
					assignedBot = ""
				}
			}
			// Remove the giving up label if present
			hasGivingUpLabel := false
			for _, l := range prIssue.Labels {
				if l.GetName() == "overseer/giving-up" {
					hasGivingUpLabel = true
					break
				}
			}
			if hasGivingUpLabel {
				if w.opts.DryRun {
					fmt.Printf("[DRYRUN] Would remove giving up label from PR #%d due to new commit %s\n", num, headSHA)
				} else {
					fmt.Printf("Removing giving up label from PR #%d due to new commit %s...\n", num, headSHA)
					if _, err := w.ghClient.Issues.RemoveLabelForIssue(ctx, w.opts.Owner, w.opts.Repo, num, "overseer/giving-up"); err != nil {
						klog.Errorf("Failed to remove giving up label from PR #%d: %v", num, err)
					}
				}
			}
		}

		if state.lastSHA != headSHA {
			state.lastSHA = headSHA
			w.processedPRs[num] = state
		}

		if hasFailure {
			filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
			if !taskExists(w.incomingDir, w.processingDir, filename) {
				// Count investigations since last commit
				investigationCount := 0
				if listCommentsErr == nil {
					for _, c := range comments {
						isPoolBot := false
						for _, bot := range w.allBotUsers {
							if strings.EqualFold(c.GetUser().GetLogin(), bot) {
								isPoolBot = true
								break
							}
						}
						if isPoolBot &&
							strings.Contains(c.GetBody(), "started investigating CI check failures") &&
							c.GetCreatedAt().After(lastCommitTime) {
							investigationCount++
						}
					}
				}

				// Post giving up comment if we haven't already posted it since the last commit
				hasPostedGivingUp := false
				if listCommentsErr == nil {
					for _, c := range comments {
						isPoolBot := false
						for _, bot := range w.allBotUsers {
							if strings.EqualFold(c.GetUser().GetLogin(), bot) {
								isPoolBot = true
								break
							}
						}
						if isPoolBot &&
							strings.Contains(c.GetBody(), "giving up. Human assistance is required") &&
							c.GetCreatedAt().After(lastCommitTime) {
							hasPostedGivingUp = true
							break
						}
					}
				}

				if hasPostedGivingUp {
					klog.Infof("Skipping PR #%d investigate because the bot has already given up on the current commit.", num)
				} else if investigationCount >= 3 {
					if !w.opts.DryRun {
						_ = addGitHubComment(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, num, "🤖 AI Factory has attempted to fix CI failures for this PR 3 times since the last commit and is giving up. Human assistance is required.")
						if assignedBot != "" && !unassignedPRs[num] {
							fmt.Printf("Unassigning bot %s from PR #%d because it has given up...\n", assignedBot, num)
							if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.opts.Owner, w.opts.Repo, num, []string{assignedBot}); err != nil {
								klog.Errorf("Failed to unassign bot %s from PR #%d: %v", assignedBot, num, err)
							}
							unassignedPRs[num] = true
						}
						if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.opts.Owner, w.opts.Repo, num, []string{"overseer/giving-up"}); err != nil {
							klog.Errorf("Failed to add giving up label to PR #%d: %v", num, err)
						}
					}
					klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (3).", num)
				} else {
					prevFailed := false
					processedPath := filepath.Join(w.processedDir, filename)
					if data, err := os.ReadFile(processedPath); err == nil {
						var t QueueTask
						if err := yaml.Unmarshal(data, &t); err == nil {
							if t.Status == "Failed" {
								prevFailed = true
							}
						}
					}

					if state.lastSHA != headSHA || prevFailed || isExplicitlyAssigned || time.Since(state.lastInvestigatedTime) > 6*time.Hour {
						sandboxName := resolveSandboxName(ctx, w.kubeClient, w.ghClient, "pr-investigate", num, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
						running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.opts.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
						} else if running {
							klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
						} else {
							assignedBot := assignedBotUser(prIssue, w.allBotUsers)

							taskAssignee := assignedBot
							if taskAssignee == "" {
								taskAssignee = author
							}

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.opts.Owner, w.opts.Repo, num)
							task := &QueueTask{
								Type:      "pr-investigate",
								URL:       prURL,
								Number:    num,
								Priority:  prPriority(prIssue),
								Phase:     3,
								CreatedAt: pr.GetCreatedAt(),
								Assignee:  taskAssignee,
								Status:    "Pending",
								CommitSHA: headSHA,
							}

							if w.opts.DryRun {
								fmt.Printf("[DRYRUN] Would queue investigate task for PR #%d: %s\n", num, prURL)
							} else {
								fmt.Printf("Queueing investigate task for PR #%d...\n", num)
								if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
									klog.Errorf("Failed to queue investigate task for PR #%d: %v", num, err)
								} else {
									state.lastInvestigatedTime = time.Now()
									w.processedPRs[num] = state
									writeTaskJournalEvent(w.opts.QueueDir, filename, task, "Created", 0)
								}
							}
						}
					}
				}
			}
			// If CI checks fail, we prioritize investigate/fix and skip address comments
			continue
		}

		// Check review comments and approvals
		var reviews []*githubv39.PullRequestReview
		if listReviews, _, err := w.ghClient.PullRequests.ListReviews(ctx, w.opts.Owner, w.opts.Repo, num, nil); err == nil {
			reviews = listReviews
		}

		isApproved := isPRApprovedOrLGTM(pr, prIssue, reviews)

		if listCommentsErr == nil {
			hasNewComments := false

			var bots []string
			if w.cfg != nil {
				bots = w.cfg.AllowlistedBots
			}

			for _, c := range comments {
				if shouldIgnoreUser(c.GetUser(), w.githubLogin, bots) {
					continue
				}
				if strings.EqualFold(c.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
					continue
				}
				if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(state.lastCommentAddressedTime) {
					hasNewComments = true
					break
				}
			}

			// Also check inline PR review comments directly
			if !hasNewComments {
				for _, r := range reviews {
					if shouldIgnoreUser(r.GetUser(), w.githubLogin, bots) {
						continue
					}
					if strings.EqualFold(r.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
						continue
					}
					if r.GetSubmittedAt().After(lastCommitTime) && r.GetSubmittedAt().After(state.lastCommentAddressedTime) {
						hasNewComments = true
						break
					}

					revComments, _, err := w.ghClient.PullRequests.ListReviewComments(ctx, w.opts.Owner, w.opts.Repo, num, r.GetID(), nil)
					if err == nil {
						for _, rc := range revComments {
							if shouldIgnoreUser(rc.GetUser(), w.githubLogin, bots) {
								continue
							}
							if strings.EqualFold(rc.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
								continue
							}
							if rc.GetCreatedAt().After(lastCommitTime) && rc.GetCreatedAt().After(state.lastCommentAddressedTime) {
								hasNewComments = true
								break
							}
						}
					}
					if hasNewComments {
						break
					}
				}
			}

			if isApproved {
				if hasNewComments {
					klog.Infof("PR #%d is approved / LGTM'd. Ignoring new comments/feedback.", num)

					// Post ignore comment if we haven't already posted it since the last commit
					hasPostedIgnore := false
					ignorePrefix := "🤖 AI Factory is ignoring new comments/feedback because this PR is already approved"
					for _, c := range comments {
						isPoolBot := false
						for _, bot := range w.allBotUsers {
							if strings.EqualFold(c.GetUser().GetLogin(), bot) {
								isPoolBot = true
								break
							}
						}
						if isPoolBot &&
							strings.HasPrefix(c.GetBody(), ignorePrefix) &&
							c.GetCreatedAt().After(lastCommitTime) {
							hasPostedIgnore = true
							break
						}
					}

					if !hasPostedIgnore && !w.opts.DryRun {
						_ = addGitHubComment(ctx, w.ghClient, w.opts.Owner, w.opts.Repo, num, ignorePrefix+" / LGTM'd.")
					}

					state.lastCommentAddressedTime = time.Now()
					w.processedPRs[num] = state
				}
				// Skip queueing comment task since it's approved
				continue
			}

			if hasNewComments {
				if os.Getenv("DRY_RUN") == "true" {
					continue
				}
				filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
				if !taskExists(w.incomingDir, w.processingDir, filename) {
					sandboxName := resolveSandboxName(ctx, w.kubeClient, w.ghClient, "pr-comments", num, w.opts.Owner, w.opts.Repo, w.opts.Namespace)
					running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.opts.Namespace, sandboxName)
					if err != nil {
						klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
						continue
					} else if running {
						klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
					} else {
						taskAssignee := assignedBot
						if taskAssignee == "" {
							taskAssignee = author
						}

						prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.opts.Owner, w.opts.Repo, num)
						task := &QueueTask{
							Type:      "pr-comments",
							URL:       prURL,
							Number:    num,
							Priority:  prPriority(prIssue),
							Phase:     2,
							CreatedAt: pr.GetCreatedAt(),
							Assignee:  taskAssignee,
							Status:    "Pending",
							CommitSHA: headSHA,
						}

						if w.opts.DryRun {
							fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, prURL)
						} else {
							fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
							state.lastCommentAddressedTime = time.Now()
							w.processedPRs[num] = state
							if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
								klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
							} else {
								writeTaskJournalEvent(w.opts.QueueDir, filename, task, "Created", 0)
							}
						}
					}
				}
			}
		}
	}
}
