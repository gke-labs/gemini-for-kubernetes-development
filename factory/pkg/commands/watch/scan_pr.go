package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func (w *Watcher) scanPRIssues(ctx context.Context) ([]*githubv39.Issue, error) {
	var allPRIssues []*githubv39.Issue
	for _, botUser := range w.allBotUsers {
		opts1 := &githubv39.IssueListByRepoOptions{
			Assignee:    botUser,
			State:       "open",
			ListOptions: githubv39.ListOptions{PerPage: 100},
		}
		for {
			iss1, resp, err := w.ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts1)
			if err != nil {
				klog.Errorf("Failed to list PR issues for assignee %s: %v", botUser, err)
				break
			}
			for _, item := range iss1 {
				if item.PullRequestLinks != nil {
					allPRIssues = append(allPRIssues, item)
				}
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts1.Page = resp.NextPage
		}
	}
	opts2PR := &githubv39.IssueListByRepoOptions{
		Labels:      []string{w.triggerLabel},
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		iss2, resp, err := w.ghClient.Issues.ListByRepo(ctx, w.Repo.Owner, w.Repo.Repo, opts2PR)
		if err != nil {
			klog.Errorf("Failed to list PR issues for label %s: %v", w.triggerLabel, err)
			break
		}
		for _, item := range iss2 {
			if item.PullRequestLinks != nil {
				allPRIssues = append(allPRIssues, item)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts2PR.Page = resp.NextPage
	}

	// Deduplicate allPRIssues
	uniquePRIssues := make(map[int]*githubv39.Issue)
	for _, item := range allPRIssues {
		uniquePRIssues[item.GetNumber()] = item
	}
	var prIssues []*githubv39.Issue
	for _, item := range uniquePRIssues {
		prIssues = append(prIssues, item)
	}

	return prIssues, nil
}

func (w *Watcher) processPRs(ctx context.Context, prIssues []*githubv39.Issue) {
	if w.PRMode == "disabled" {
		return
	}
	for _, prIssue := range prIssues {
		num := prIssue.GetNumber()
		if w.cfg != nil && w.cfg.MinNumber > 0 && num < w.cfg.MinNumber {
			continue
		}
		if hasStopLabel(prIssue.Labels, w.triggerLabel) {
			klog.Infof("Skipping PR #%d because it has the stop label ('overseer/stop' or '%s/stop')", num, w.triggerLabel)
			w.reconcileReadyForHumanLabel(ctx, num, prIssue, false, "")
			removePendingTasksForNumber(w.incomingDir, num)
			continue
		}
		pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, num)
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
		syncReferencedIssueLabels(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, pr, prIssue)
		if hasStopLabel(prIssue.Labels, w.triggerLabel) {
			klog.Infof("Skipping PR #%d after label sync because it has the stop label ('overseer/stop' or '%s/stop')", num, w.triggerLabel)
			w.reconcileReadyForHumanLabel(ctx, num, prIssue, false, "")
			removePendingTasksForNumber(w.incomingDir, num)
			continue
		}

		headSHA := pr.GetHead().GetSHA()

		// Fetch PR commits to find the last commit timestamp
		prCommits, err := github.ListAllCommits(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num)
		var lastCommitTime time.Time
		if err == nil {
			for _, c := range prCommits {
				if c.GetCommit().GetCommitter().GetDate().After(lastCommitTime) {
					lastCommitTime = c.GetCommit().GetCommitter().GetDate()
				}
			}
		}

		// Fetch all PR comments (handling pagination)
		comments, listCommentsErr := github.ListAllIssueComments(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num)

		var reviews []*githubv39.PullRequestReview
		var listReviewsErr error
		if listCommentsErr == nil {
			reviews, listReviewsErr = github.ListAllReviews(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num)
		}

		revCommentsMap := make(map[int64][]*githubv39.PullRequestComment)
		if listCommentsErr == nil && listReviewsErr == nil {
			for _, r := range reviews {
				if rc, err := github.ListAllReviewComments(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, r.GetID()); err == nil {
					revCommentsMap[r.GetID()] = rc
				}
			}
		}

		state := w.processedPRs[num]

		// PR Inactivity check
		if w.PRInactivityTimeout > 0 && listCommentsErr == nil && listReviewsErr == nil {
			var bots []string
			if w.cfg != nil {
				bots = w.cfg.AllowlistedBots
			}
			lastActivity := getLastPRActivityTime(pr, comments, reviews, revCommentsMap, w.githubLogin, bots)
			if time.Since(lastActivity) > w.PRInactivityTimeout {
				stopLabel := getStopLabel(w.triggerLabel)
				w.reconcileReadyForHumanLabel(ctx, num, prIssue, false, headSHA)
				if w.DryRun {
					fmt.Printf("[DRYRUN] Would pause automated processing on PR #%d and apply label '%s' due to inactivity since %v\n", num, stopLabel, lastActivity)
				} else {
					klog.Infof("Pausing automated processing on PR #%d and applying label '%s' due to inactivity since %v", num, stopLabel, lastActivity)
					if !hasInactivityComment(comments, lastActivity) {
						addGitHubComment(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, fmt.Sprintf("🤖 AI Factory has paused automated processing on this pull request due to a period of inactivity with no human comments (inactive for %s). I have applied the `%s` label.\n\nTo resume automated processing, please remove the `%s` label from this pull request and add a new comment/review.", w.PRInactivityTimeout, stopLabel, stopLabel))
					}
					if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{stopLabel}); err != nil {
						klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
					}
					removePendingTasksForNumber(w.incomingDir, num)
				}
				continue
			}
		}

		// Check Phase 1: Rebase/Conflicts
		isConflicting := pr.Mergeable != nil && !*pr.Mergeable

		if isConflicting {
			w.reconcileReadyForHumanLabel(ctx, num, prIssue, false, headSHA)
			if state.lastIteratedSHA != "" && state.lastIteratedSHA == headSHA {
				klog.Infof("Skipping PR #%d rebase/conflict resolution because an iterate task was already processed for head SHA %s.", num, headSHA)
				continue
			}

			filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
			if !taskExists(w.incomingDir, w.processingDir, filename) {
				sandboxName := w.resolveSandboxName(ctx, "pr-iterate", num)
				running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
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

					prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num)
					task := &QueueTask{
						Type:       "pr-iterate",
						URL:        prURL,
						Number:     num,
						Priority:   getPRPriority(prIssue),
						Phase:      1,
						CreatedAt:  pr.GetCreatedAt(),
						EnqueuedAt: time.Now(),
						Assignee:   taskAssignee,
						Status:     "Pending",
						CommitSHA:  headSHA,
					}

					if w.DryRun {
						fmt.Printf("[DRYRUN] Would queue rebase task for PR #%d: %s\n", num, prURL)
					} else {
						fmt.Printf("Queueing rebase task for PR #%d...\n", num)
						state.lastIteratedSHA = headSHA
						state.lastIteratedTime = time.Now()
						w.processedPRs[num] = state
						if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
							klog.Errorf("Failed to queue rebase task for PR #%d: %v", num, err)
						} else {
							writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
						}
					}
				}
			}
			// If conflicting, we prioritize rebase and skip other PR checks for this PR in this loop
			continue
		}

		// Check CI Check Failures
		hasFailure := false
		checkRuns, err := common.ListAllCheckRuns(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, headSHA)
		if err == nil {
			for _, run := range checkRuns {
				c := run.GetConclusion()
				if c == "failure" || c == "timed_out" || c == "cancelled" {
					hasFailure = true
					break
				}
			}
		}

		statuses, _, err := w.ghClient.Repositories.ListStatuses(ctx, w.Repo.Owner, w.Repo.Repo, headSHA, nil)
		if err == nil {
			for _, status := range statuses {
				if status.GetState() == "failure" || status.GetState() == "error" {
					hasFailure = true
					break
				}
			}
		}

		state = w.processedPRs[num]

		assignedBot := assignedBotUser(prIssue, w.allBotUsers)
		isExplicitlyAssigned := assignedBot != ""

		if hasFailure {
			filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
			if !taskExists(w.incomingDir, w.processingDir, filename) {
				investigationCount := 0
				if listCommentsErr == nil {
					var bots []string
					if w.cfg != nil {
						bots = w.cfg.AllowlistedBots
					}
					investigationCount = getInvestigationCount(comments, lastCommitTime, w.allBotUsers, w.githubLogin, bots)
				}

				if investigationCount >= 3 {
					stopLabel := getStopLabel(w.triggerLabel)
					if !w.DryRun {
						addGitHubComment(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, fmt.Sprintf("🤖 AI Factory has attempted to investigate/fix CI check failures for this pull request 3 times since the last commit or update without success. To prevent infinite loops, I am pausing automated investigation and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment).", stopLabel, stopLabel))
						if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{stopLabel}); err != nil {
							klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
						}
					}
					klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (3 attempts since last update) and applying stop label '%s'.", num, stopLabel)
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

					if state.lastInvestigatedSHA != headSHA || prevFailed || isExplicitlyAssigned || time.Since(state.lastInvestigatedTime) > 2*time.Hour {
						sandboxName := w.resolveSandboxName(ctx, "pr-investigate", num)
						running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
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

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num)
							task := &QueueTask{
								Type:       "pr-investigate",
								URL:        prURL,
								Number:     num,
								Priority:   getPRPriority(prIssue),
								Phase:      3,
								CreatedAt:  pr.GetCreatedAt(),
								EnqueuedAt: time.Now(),
								Assignee:   taskAssignee,
								Status:     "Pending",
								CommitSHA:  headSHA,
							}

							if w.DryRun {
								fmt.Printf("[DRYRUN] Would queue investigate task for PR #%d: %s\n", num, prURL)
							} else {
								fmt.Printf("Queueing investigate task for PR #%d...\n", num)
								state.lastInvestigatedSHA = headSHA
								state.lastInvestigatedTime = time.Now()
								w.processedPRs[num] = state
								if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
									klog.Errorf("Failed to queue investigate task for PR #%d: %v", num, err)
								} else {
									writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
								}
							}
						}
					}
				}
			}
		}

		// Check review comments and approvals
		isApproved := isPRApprovedOrLGTM(pr, prIssue, reviews)

		if listCommentsErr == nil {
			hasNewComments := false

			var bots []string
			if w.cfg != nil {
				bots = w.cfg.AllowlistedBots
			}

			// Find the latest timestamp of any reply made by an allowlisted bot user (excluding reviewer bots)
			var latestBotReplyTime time.Time
			for _, c := range comments {
				if !isReviewerBot(c.GetUser(), w.cfg) && isBotReply(c.GetUser(), w.githubLogin, bots) && c.GetCreatedAt().After(latestBotReplyTime) {
					latestBotReplyTime = c.GetCreatedAt()
				}
			}
			for _, r := range reviews {
				if !isReviewerBot(r.GetUser(), w.cfg) && isBotReply(r.GetUser(), w.githubLogin, bots) && r.GetSubmittedAt().After(latestBotReplyTime) {
					latestBotReplyTime = r.GetSubmittedAt()
				}
			}

			var unackCommentIDs []int64
			var unackPRCommentIDs []int64
			hasNewHumanComments := false
			hasNewBotReviews := false
			for _, c := range comments {
				isReviewer := isReviewerBot(c.GetUser(), w.cfg)
				if !isReviewer && shouldIgnoreUser(c.GetUser(), w.githubLogin, bots) {
					continue
				}
				if strings.EqualFold(c.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
					continue
				}
				if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(state.lastCommentAddressedTime) && c.GetCreatedAt().After(latestBotReplyTime) {
					if hasIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, c.GetID(), "+1", true, bots, w.githubLogin) {
						continue
					}
					humanRocket := hasIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, c.GetID(), "rocket", false, bots, w.githubLogin)
					if !humanRocket && hasIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, c.GetID(), "eyes", true, bots, w.githubLogin) {
						continue
					}
					if !humanRocket && hasIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, c.GetID(), "confused", true, bots, w.githubLogin) {
						continue
					}
					if isReviewer {
						hasNewBotReviews = true
					} else {
						hasNewHumanComments = true
					}
					unackCommentIDs = append(unackCommentIDs, c.GetID())
				}
			}

			// Also check inline PR review comments directly
			for _, r := range reviews {
				isReviewer := isReviewerBot(r.GetUser(), w.cfg)
				if !isReviewer && shouldIgnoreUser(r.GetUser(), w.githubLogin, bots) {
					if r.GetSubmittedAt().After(latestBotReplyTime) {
						latestBotReplyTime = r.GetSubmittedAt()
					}
					continue
				}
				if strings.EqualFold(r.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
					continue
				}
				if r.GetSubmittedAt().After(lastCommitTime) && r.GetSubmittedAt().After(state.lastCommentAddressedTime) && r.GetSubmittedAt().After(latestBotReplyTime) {
					if isReviewer {
						hasNewBotReviews = true
					} else {
						hasNewHumanComments = true
					}
				}

				revComments := revCommentsMap[r.GetID()]
				for _, rc := range revComments {
					isInlineReviewer := isReviewerBot(rc.GetUser(), w.cfg)
					if !isInlineReviewer && shouldIgnoreUser(rc.GetUser(), w.githubLogin, bots) {
						if rc.GetCreatedAt().After(latestBotReplyTime) {
							latestBotReplyTime = rc.GetCreatedAt()
						}
						continue
					}
					if strings.EqualFold(rc.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
						continue
					}
					if rc.GetCreatedAt().After(lastCommitTime) && rc.GetCreatedAt().After(state.lastCommentAddressedTime) && rc.GetCreatedAt().After(latestBotReplyTime) {
						if isInlineReviewer {
							hasNewBotReviews = true
						} else {
							hasNewHumanComments = true
						}
						unackPRCommentIDs = append(unackPRCommentIDs, rc.GetID())
					}
				}
			}

			if hasNewHumanComments {
				hasNewComments = true
			} else if hasNewBotReviews {
				if state.lastCommentAddressedSHA != "" && state.lastCommentAddressedSHA == headSHA {
					klog.Infof("Skipping bot review feedback on PR #%d because an address-comments task already ran against SHA %s without resulting in a commit.", num, headSHA)
				} else {
					hasNewComments = true
				}
			}

			if isApproved {
				klog.V(2).Infof("PR #%d is approved / LGTM'd", num)
			}

			if hasNewComments {
				if os.Getenv("DRY_RUN") == "true" {
					continue
				}
				filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
				if !taskExists(w.incomingDir, w.processingDir, filename) {
					sandboxName := w.resolveSandboxName(ctx, "pr-comments", num)
					running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
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

						prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num)
						task := &QueueTask{
							Type:       "pr-comments",
							URL:        prURL,
							Number:     num,
							Priority:   getPRPriority(prIssue),
							Phase:      2,
							CreatedAt:  pr.GetCreatedAt(),
							EnqueuedAt: time.Now(),
							Assignee:   taskAssignee,
							Status:     "Pending",
							CommitSHA:  headSHA,
						}

						if w.DryRun {
							fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, prURL)
						} else {
							fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
							for _, cid := range unackCommentIDs {
								addIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, cid, "eyes")
							}
							for _, cid := range unackPRCommentIDs {
								addPullRequestCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, cid, "eyes")
							}
							state.lastCommentAddressedTime = time.Now()
							state.lastCommentAddressedSHA = headSHA
							w.processedPRs[num] = state
							if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
								klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
							} else {
								writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
							}
						}
					}
				}
			} else if !hasFailure && !isApproved && state.lastReviewedSHA != headSHA && shouldAutoReviewPR(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, pr, prIssue, w.triggerLabel) {
				hasBotReviewAfterLastCommit := false
				for _, r := range reviews {
					if isBotReply(r.GetUser(), w.githubLogin, bots) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
						hasBotReviewAfterLastCommit = true
						break
					}
				}

				if !hasBotReviewAfterLastCommit {
					if os.Getenv("DRY_RUN") == "true" {
						continue
					}
					filename := fmt.Sprintf("task-pr-%d-review.yaml", num)
					if !taskExists(w.incomingDir, w.processingDir, filename) {
						sandboxName := w.resolveSandboxName(ctx, "pr-review", num)
						running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
						if err != nil {
							klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
							continue
						} else if running {
							klog.Infof("Skipping PR #%d review because there is an in-flight sandbox %s.", num, sandboxName)
						} else {
							var bodies []string
							if pr.GetBody() != "" {
								bodies = append(bodies, pr.GetBody())
							}
							for refIssueNum := range common.GetReferencedIssues(pr) {
								refIssue, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, refIssueNum)
								if err == nil && refIssue.GetBody() != "" {
									bodies = append(bodies, refIssue.GetBody())
								}
							}
							instructions := common.ExtractReviewInstructions(bodies...)

							prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num)
							task := &QueueTask{
								Type:         "pr-review",
								URL:          prURL,
								Number:       num,
								Priority:     getPRPriority(prIssue),
								Phase:        2,
								CreatedAt:    pr.GetCreatedAt(),
								EnqueuedAt:   time.Now(),
								Status:       "Pending",
								CommitSHA:    headSHA,
								Instructions: instructions,
							}

							if w.DryRun {
								fmt.Printf("[DRYRUN] Would queue review task for PR #%d: %s\n", num, prURL)
							} else {
								fmt.Printf("Queueing review task for PR #%d (Instructions: %d)...\n", num, len(instructions))
								state.lastReviewedSHA = headSHA
								w.processedPRs[num] = state
								if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
									klog.Errorf("Failed to queue review task for PR #%d: %v", num, err)
								} else {
									writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
								}
							}
						}
					}
				}
			}

			// Check and reconcile ready-for-human label
			if listReviewsErr == nil {
				isReviewRequired := shouldAutoReviewPR(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, pr, prIssue, w.triggerLabel)
				hasBotReviewOnHead := hasCompletedBotReviewOnHead(reviews, headSHA, lastCommitTime, w.cfg)
				reviewSatisfied := !isReviewRequired || hasBotReviewOnHead

				hasActiveTask := hasActivePRTask(w.incomingDir, w.processingDir, num)

				isReadyForHuman := !isConflicting &&
					!hasFailure &&
					!hasNewComments &&
					!hasActiveTask &&
					reviewSatisfied &&
					!hasStopLabel(prIssue.Labels, w.triggerLabel) &&
					!pr.GetDraft() &&
					pr.GetState() == "open"

				w.reconcileReadyForHumanLabel(ctx, num, prIssue, isReadyForHuman, headSHA)

				if isReadyForHuman && assignedBot != "" {
					if w.DryRun {
						fmt.Printf("[DRYRUN] Would unassign bot %s from PR #%d (ready for human review)\n", assignedBot, num)
					} else {
						fmt.Printf("Unassigning bot %s from PR #%d (ready for human review)...\n", assignedBot, num)
						if _, _, err := w.ghClient.Issues.RemoveAssignees(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{assignedBot}); err != nil {
							klog.Errorf("Failed to unassign bot %s from PR #%d: %v", assignedBot, num, err)
						}
					}
				}
			}
		}
	}
}
