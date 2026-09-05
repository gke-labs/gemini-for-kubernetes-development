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
		if listCommentsErr != nil {
			klog.Errorf("Failed to list issue comments for PR #%d: %v", num, listCommentsErr)
			continue
		}

		reviews, listReviewsErr := github.ListAllReviews(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num)
		if listReviewsErr != nil {
			klog.Errorf("Failed to list reviews for PR #%d: %v", num, listReviewsErr)
			continue
		}

		revCommentsMap := make(map[int64][]*githubv39.PullRequestComment)
		for _, r := range reviews {
			if rc, err := github.ListAllReviewComments(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, r.GetID()); err == nil {
				revCommentsMap[r.GetID()] = rc
			}
		}

		state := w.processedPRs[num]

		var bots []string
		if w.cfg != nil {
			bots = w.cfg.AllowlistedBots
		}

		// PR Inactivity check
		if w.PRInactivityTimeout > 0 {
			lastActivity := getLastPRActivityTime(pr, comments, reviews, revCommentsMap, w.githubLogin, bots, w.triggerLabel)
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

		var checkAnalysis prCheckAnalysis
		var commentAnalysis prCommentAnalysis
		var canReview bool

		if !isConflicting {
			checkAnalysis = w.evaluatePRChecks(ctx, headSHA)
			commentAnalysis = w.evaluatePRComments(ctx, num, pr, comments, reviews, revCommentsMap, lastCommitTime, state.lastCommentAddressedTime, state.lastCommentAddressedSHA, headSHA, bots)
			isApproved := isPRApprovedOrLGTM(pr, prIssue, reviews)
			if isApproved {
				klog.V(2).Infof("PR #%d is approved / LGTM'd", num)
			}
			if !checkAnalysis.hasFailure && !checkAnalysis.hasPending && !isApproved && state.lastReviewedSHA != headSHA && shouldAutoReviewPR(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, pr, prIssue, w.triggerLabel) {
				canReview = !hasBotReviewAfterLastCommit(reviews, lastCommitTime, headSHA, w.githubLogin, bots)
			}
		}

		assignedBot := assignedBotUser(prIssue, w.allBotUsers)
		isExplicitlyAssigned := assignedBot != ""

		taskAssignee := assignedBot
		if taskAssignee == "" {
			taskAssignee = author
		}

		canInvestigate := !isConflicting && checkAnalysis.hasFailure && w.canInvestigatePR(num, headSHA, isExplicitlyAssigned, state, comments, lastCommitTime, bots)

		prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", w.Repo.Owner, w.Repo.Repo, num)
		shortSHA := headSHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}

		pc := &prContext{
			pr:                   pr,
			prIssue:              prIssue,
			headSHA:              headSHA,
			shortSHA:             shortSHA,
			lastCommitTime:       lastCommitTime,
			taskAssignee:         taskAssignee,
			isExplicitlyAssigned: isExplicitlyAssigned,
			prURL:                prURL,
		}

		// Top level case statement for handling each type of PR task
		switch {
		case isConflicting:
			w.handlePRIterate(ctx, pc)
			continue

		case commentAnalysis.hasNewComments:
			w.handlePRComments(ctx, pc, commentAnalysis)

		case canInvestigate:
			w.handlePRInvestigate(ctx, pc, checkAnalysis, comments, bots)

		case canReview:
			w.handlePRReview(ctx, pc, checkAnalysis.checkRuns)
		}

		// Check and reconcile ready-for-human label
		isReviewRequired := shouldAutoReviewPR(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, pr, prIssue, w.triggerLabel)
		hasBotReviewOnHead := hasCompletedBotReviewOnHead(reviews, revCommentsMap, headSHA, lastCommitTime, w.cfg)
		reviewSatisfied := !isReviewRequired || hasBotReviewOnHead

		hasActiveTask := hasActivePRTask(w.incomingDir, w.processingDir, num)

		isReadyForHuman := !isConflicting &&
			!checkAnalysis.hasFailure &&
			!checkAnalysis.hasPending &&
			!commentAnalysis.hasNewComments &&
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

type prCheckAnalysis struct {
	hasFailure                bool
	hasPending                bool
	earliestFailureTime       time.Time
	earliestFailureName       string
	earliestFailureConclusion string
	failedCount               int
	checkRuns                 []*githubv39.CheckRun
}

func (w *Watcher) evaluatePRChecks(ctx context.Context, headSHA string) prCheckAnalysis {
	var analysis prCheckAnalysis

	checkRuns, err := common.ListAllCheckRuns(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, headSHA)
	if err == nil {
		analysis.checkRuns = checkRuns
		for _, run := range checkRuns {
			if run.GetStatus() != "completed" {
				analysis.hasPending = true
			}
			c := run.GetConclusion()
			if c == "failure" || c == "timed_out" || c == "cancelled" || c == "action_required" || c == "stale" {
				analysis.hasFailure = true
				analysis.failedCount++
				t := run.GetCompletedAt().Time
				if t.IsZero() {
					t = run.GetStartedAt().Time
				}
				if !t.IsZero() {
					if analysis.earliestFailureTime.IsZero() || t.Before(analysis.earliestFailureTime) {
						analysis.earliestFailureTime = t
						analysis.earliestFailureName = run.GetName()
						analysis.earliestFailureConclusion = c
					}
				} else if analysis.earliestFailureName == "" {
					analysis.earliestFailureName = run.GetName()
					analysis.earliestFailureConclusion = c
				}
			}
		}
	}

	statuses, err := common.ListAllStatuses(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, headSHA)
	if err == nil {
		for _, status := range statuses {
			if status.GetState() == "pending" {
				analysis.hasPending = true
			}
			if status.GetState() == "failure" || status.GetState() == "error" {
				analysis.hasFailure = true
				analysis.failedCount++
				t := status.GetUpdatedAt()
				if t.IsZero() {
					t = status.GetCreatedAt()
				}
				if !t.IsZero() {
					if analysis.earliestFailureTime.IsZero() || t.Before(analysis.earliestFailureTime) {
						analysis.earliestFailureTime = t
						analysis.earliestFailureName = status.GetContext()
						analysis.earliestFailureConclusion = status.GetState()
					}
				} else if analysis.earliestFailureName == "" {
					analysis.earliestFailureName = status.GetContext()
					analysis.earliestFailureConclusion = status.GetState()
				}
			}
		}
	}

	return analysis
}

type prCommentAnalysis struct {
	hasNewComments      bool
	unackCommentIDs     []int64
	unackPRCommentIDs   []int64
	oldestCommentTime   time.Time
	oldestCommentAuthor string
	oldestCommentType   string
	oldestCommentID     int64
}

func (w *Watcher) evaluatePRComments(
	ctx context.Context,
	num int,
	pr *githubv39.PullRequest,
	comments []*githubv39.IssueComment,
	reviews []*githubv39.PullRequestReview,
	revCommentsMap map[int64][]*githubv39.PullRequestComment,
	lastCommitTime, lastCommentAddressedTime time.Time,
	lastCommentAddressedSHA, headSHA string,
	bots []string,
) prCommentAnalysis {
	var analysis prCommentAnalysis

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

	hasNewHumanComments := false
	hasNewBotReviews := false

	updateOldestComment := func(t time.Time, author string, cType string, id int64) {
		if !t.IsZero() && (analysis.oldestCommentTime.IsZero() || t.Before(analysis.oldestCommentTime)) {
			analysis.oldestCommentTime = t
			analysis.oldestCommentAuthor = author
			analysis.oldestCommentType = cType
			analysis.oldestCommentID = id
		}
	}

	for _, c := range comments {
		isReviewer := isReviewerBot(c.GetUser(), w.cfg)
		if !isReviewer && shouldIgnoreUser(c.GetUser(), w.githubLogin, bots) {
			continue
		}
		if strings.EqualFold(c.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
			continue
		}
		if hasIgnorePrefix(c.GetBody(), w.triggerLabel) {
			continue
		}
		if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(lastCommentAddressedTime) && c.GetCreatedAt().After(latestBotReplyTime) {
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
			analysis.unackCommentIDs = append(analysis.unackCommentIDs, c.GetID())
			author := ""
			if c.GetUser() != nil {
				author = c.GetUser().GetLogin()
			}
			updateOldestComment(c.GetCreatedAt(), author, "comment", c.GetID())
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
		if r.GetSubmittedAt().After(lastCommitTime) && r.GetSubmittedAt().After(lastCommentAddressedTime) && r.GetSubmittedAt().After(latestBotReplyTime) {
			if hasIgnorePrefix(r.GetBody(), w.triggerLabel) {
				continue
			}
			if isReviewer {
				if r.GetState() == "CHANGES_REQUESTED" || len(revCommentsMap[r.GetID()]) > 0 {
					hasNewBotReviews = true
				}
			} else {
				hasNewHumanComments = true
			}
			if strings.TrimSpace(r.GetBody()) != "" {
				if !isReviewer || r.GetState() == "CHANGES_REQUESTED" || len(revCommentsMap[r.GetID()]) > 0 {
					author := ""
					if r.GetUser() != nil {
						author = r.GetUser().GetLogin()
					}
					updateOldestComment(r.GetSubmittedAt(), author, "review", r.GetID())
				}
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
			if rc.GetCreatedAt().After(lastCommitTime) && rc.GetCreatedAt().After(lastCommentAddressedTime) && rc.GetCreatedAt().After(latestBotReplyTime) {
				if hasIgnorePrefix(rc.GetBody(), w.triggerLabel) {
					continue
				}
				if isInlineReviewer {
					hasNewBotReviews = true
				} else {
					hasNewHumanComments = true
				}
				analysis.unackPRCommentIDs = append(analysis.unackPRCommentIDs, rc.GetID())
				author := ""
				if rc.GetUser() != nil {
					author = rc.GetUser().GetLogin()
				}
				updateOldestComment(rc.GetCreatedAt(), author, "inline review comment", rc.GetID())
			}
		}
	}

	if hasNewHumanComments {
		analysis.hasNewComments = true
	} else if hasNewBotReviews {
		if lastCommentAddressedSHA != "" && lastCommentAddressedSHA == headSHA {
			hasReviewAfterAddressed := false
			for _, r := range reviews {
				if isReviewerBot(r.GetUser(), w.cfg) && r.GetSubmittedAt().After(lastCommentAddressedTime) {
					if r.GetState() == "CHANGES_REQUESTED" || len(revCommentsMap[r.GetID()]) > 0 {
						hasReviewAfterAddressed = true
						break
					}
				}
				revComments := revCommentsMap[r.GetID()]
				for _, rc := range revComments {
					if isReviewerBot(rc.GetUser(), w.cfg) && rc.GetCreatedAt().After(lastCommentAddressedTime) {
						hasReviewAfterAddressed = true
						break
					}
				}
				if hasReviewAfterAddressed {
					break
				}
			}

			if !hasReviewAfterAddressed {
				klog.Infof("Skipping bot review feedback on PR #%d because an address-comments task already ran against SHA %s without resulting in a commit.", num, headSHA)
			} else {
				analysis.hasNewComments = true
			}
		} else {
			analysis.hasNewComments = true
		}
	}

	return analysis
}

func hasBotReviewAfterLastCommit(reviews []*githubv39.PullRequestReview, lastCommitTime time.Time, headSHA, githubLogin string, bots []string) bool {
	for _, r := range reviews {
		if isBotReply(r.GetUser(), githubLogin, bots) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			return true
		}
	}
	return false
}

type prContext struct {
	pr                   *githubv39.PullRequest
	prIssue              *githubv39.Issue
	headSHA              string
	shortSHA             string
	lastCommitTime       time.Time
	taskAssignee         string
	isExplicitlyAssigned bool
	prURL                string
}

func (w *Watcher) handlePRIterate(ctx context.Context, pc *prContext) {
	num := pc.prIssue.GetNumber()
	state := w.processedPRs[num]

	w.reconcileReadyForHumanLabel(ctx, num, pc.prIssue, false, pc.headSHA)
	if state.lastIteratedSHA != "" && state.lastIteratedSHA == pc.headSHA {
		klog.Infof("Skipping PR #%d rebase/conflict resolution because an iterate task was already processed for head SHA %s.", num, pc.headSHA)
		return
	}

	filename := fmt.Sprintf("task-pr-%d-iterate.yaml", num)
	if !taskExists(w.incomingDir, w.processingDir, filename) {
		sandboxName := w.resolveSandboxName(ctx, api.TypePRIterate, num)
		running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
			return
		} else if running {
			klog.Infof("Skipping PR #%d rebase because there is an in-flight sandbox %s.", num, sandboxName)
			return
		}

		baseRef := ""
		if pc.pr.GetBase() != nil {
			baseRef = pc.pr.GetBase().GetRef()
		}
		notes := fmt.Sprintf("PR #%d has merge conflicts with base branch '%s'; head commit %s committer date %s, PR updated at %s", num, baseRef, pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339), pc.pr.GetUpdatedAt().Format(time.RFC3339))

		task := w.newPRQueueTask(PRTaskOptions{
			Type:             api.TypePRIterate,
			PR:               pc.pr,
			PRIssue:          pc.prIssue,
			Phase:            api.PhaseRebase,
			Assignee:         pc.taskAssignee,
			CommitSHA:        pc.headSHA,
			TriggerEventTime: pc.lastCommitTime,
			TriggerReason:    api.TriggerReasonPRMergeConflict,
			TriggerNotes:     notes,
		})

		if w.DryRun {
			fmt.Printf("[DRYRUN] Would queue rebase task for PR #%d: %s\n", num, pc.prURL)
		} else {
			fmt.Printf("Queueing rebase task for PR #%d...\n", num)
			state.lastIteratedSHA = pc.headSHA
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

func (w *Watcher) canInvestigatePR(
	num int,
	headSHA string,
	isExplicitlyAssigned bool,
	state prWatchState,
	comments []*githubv39.IssueComment,
	lastCommitTime time.Time,
	bots []string,
) bool {
	filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)
	if taskExists(w.incomingDir, w.processingDir, filename) {
		return false
	}
	if getInvestigationCount(comments, lastCommitTime, w.allBotUsers, w.githubLogin, bots, w.triggerLabel) >= 3 {
		return true
	}
	prevFailed := false
	processedPath := filepath.Join(w.processedDir, filename)
	if data, err := os.ReadFile(processedPath); err == nil {
		var t api.QueueTask
		if err := yaml.Unmarshal(data, &t); err == nil {
			if t.Status == "Failed" {
				prevFailed = true
			}
		}
	}
	return state.lastInvestigatedSHA != headSHA || prevFailed || isExplicitlyAssigned || time.Since(state.lastInvestigatedTime) > 2*time.Hour
}

func (w *Watcher) handlePRInvestigate(
	ctx context.Context,
	pc *prContext,
	checkAnalysis prCheckAnalysis,
	comments []*githubv39.IssueComment,
	bots []string,
) {
	num := pc.prIssue.GetNumber()
	state := w.processedPRs[num]
	filename := fmt.Sprintf("task-pr-%d-investigate.yaml", num)

	if !taskExists(w.incomingDir, w.processingDir, filename) {
		investigationCount := getInvestigationCount(comments, pc.lastCommitTime, w.allBotUsers, w.githubLogin, bots, w.triggerLabel)

		if investigationCount >= 3 {
			stopLabel := getStopLabel(w.triggerLabel)
			if !w.DryRun {
				addGitHubComment(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, num, fmt.Sprintf("🤖 AI Factory has attempted to investigate/fix CI check failures for this pull request 3 times since the last commit or update without success. To prevent infinite loops, I am pausing automated investigation and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment).", stopLabel, stopLabel))
				if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{stopLabel}); err != nil {
					klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
				}
			}
			klog.Infof("Skipping PR #%d investigate because it has reached the maximum retry limit (3 attempts since last update) and applying stop label '%s'.", num, stopLabel)
			return
		}

		prevFailed := false
		processedPath := filepath.Join(w.processedDir, filename)
		if data, err := os.ReadFile(processedPath); err == nil {
			var t api.QueueTask
			if err := yaml.Unmarshal(data, &t); err == nil {
				if t.Status == "Failed" {
					prevFailed = true
				}
			}
		}

		if state.lastInvestigatedSHA != pc.headSHA || prevFailed || pc.isExplicitlyAssigned || time.Since(state.lastInvestigatedTime) > 2*time.Hour {
			sandboxName := w.resolveSandboxName(ctx, api.TypePRInvestigate, num)
			running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
				return
			} else if running {
				klog.Infof("Skipping PR #%d investigate because there is an in-flight sandbox %s.", num, sandboxName)
				return
			}

			eventTime := checkAnalysis.earliestFailureTime
			if eventTime.IsZero() {
				eventTime = pc.lastCommitTime
			}
			failName := checkAnalysis.earliestFailureName
			if failName == "" {
				failName = "unknown check"
			}
			failConclusion := checkAnalysis.earliestFailureConclusion
			if failConclusion == "" {
				failConclusion = "failed"
			}
			notes := fmt.Sprintf("Earliest CI failure in '%s' (%s) at %s; total %d failed check(s) on commit %s", failName, failConclusion, eventTime.Format(time.RFC3339), checkAnalysis.failedCount, pc.shortSHA)

			task := w.newPRQueueTask(PRTaskOptions{
				Type:             api.TypePRInvestigate,
				PR:               pc.pr,
				PRIssue:          pc.prIssue,
				Phase:            api.PhaseInvestigate,
				Assignee:         pc.taskAssignee,
				CommitSHA:        pc.headSHA,
				TriggerEventTime: eventTime,
				TriggerReason:    api.TriggerReasonPRCheckFailed,
				TriggerNotes:     notes,
			})

			if w.DryRun {
				fmt.Printf("[DRYRUN] Would queue investigate task for PR #%d: %s\n", num, pc.prURL)
			} else {
				fmt.Printf("Queueing investigate task for PR #%d...\n", num)
				state.lastInvestigatedSHA = pc.headSHA
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

func (w *Watcher) handlePRComments(ctx context.Context, pc *prContext, commentAnalysis prCommentAnalysis) {
	if os.Getenv("DRY_RUN") == "true" {
		return
	}
	num := pc.prIssue.GetNumber()
	state := w.processedPRs[num]
	filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)

	if !taskExists(w.incomingDir, w.processingDir, filename) {
		sandboxName := w.resolveSandboxName(ctx, api.TypePRComments, num)
		running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
			return
		} else if running {
			klog.Infof("Skipping PR #%d address-comments because there is an in-flight sandbox %s.", num, sandboxName)
			return
		}

		commitInfo := ""
		if !pc.lastCommitTime.IsZero() {
			commitInfo = fmt.Sprintf(" since last commit %s (committer date %s)", pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339))
		}
		authorStr := ""
		if commentAnalysis.oldestCommentAuthor != "" {
			authorStr = fmt.Sprintf(" by %s", commentAnalysis.oldestCommentAuthor)
		}
		cType := commentAnalysis.oldestCommentType
		if cType == "" {
			cType = "comment"
		}
		notes := fmt.Sprintf("Oldest unaddressed %s%s added at %s (ID %d)%s", cType, authorStr, commentAnalysis.oldestCommentTime.Format(time.RFC3339), commentAnalysis.oldestCommentID, commitInfo)

		task := w.newPRQueueTask(PRTaskOptions{
			Type:             api.TypePRComments,
			PR:               pc.pr,
			PRIssue:          pc.prIssue,
			Phase:            api.PhaseComments,
			Assignee:         pc.taskAssignee,
			CommitSHA:        pc.headSHA,
			TriggerEventTime: commentAnalysis.oldestCommentTime,
			TriggerReason:    api.TriggerReasonPRCommentsAdded,
			TriggerNotes:     notes,
		})

		if w.DryRun {
			fmt.Printf("[DRYRUN] Would queue address-comments task for PR #%d: %s\n", num, pc.prURL)
		} else {
			fmt.Printf("Queueing address-comments task for PR #%d...\n", num)
			for _, cid := range commentAnalysis.unackCommentIDs {
				addIssueCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, cid, "eyes")
			}
			for _, cid := range commentAnalysis.unackPRCommentIDs {
				addPullRequestCommentReaction(ctx, w.ghClient, w.Repo.Owner, w.Repo.Repo, cid, "eyes")
			}
			state.lastCommentAddressedTime = time.Now()
			state.lastCommentAddressedSHA = pc.headSHA
			w.processedPRs[num] = state
			if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
				klog.Errorf("Failed to queue address-comments task for PR #%d: %v", num, err)
			} else {
				writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
			}
		}
	}
}

func (w *Watcher) handlePRReview(ctx context.Context, pc *prContext, checkRuns []*githubv39.CheckRun) {
	if os.Getenv("DRY_RUN") == "true" {
		return
	}
	num := pc.prIssue.GetNumber()
	state := w.processedPRs[num]
	filename := fmt.Sprintf("task-pr-%d-review.yaml", num)

	if !taskExists(w.incomingDir, w.processingDir, filename) {
		sandboxName := w.resolveSandboxName(ctx, api.TypePRReview, num)
		running, err := isSandboxTaskRunning(ctx, w.kubeClient, w.Namespace, sandboxName)
		if err != nil {
			klog.Errorf("Failed to check if sandbox %s is running: %v", sandboxName, err)
			return
		} else if running {
			klog.Infof("Skipping PR #%d review because there is an in-flight sandbox %s.", num, sandboxName)
			return
		}

		var bodies []string
		if pc.pr.GetBody() != "" {
			bodies = append(bodies, pc.pr.GetBody())
		}
		for refIssueNum := range common.GetReferencedIssues(pc.pr) {
			refIssue, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, refIssueNum)
			if err == nil && refIssue.GetBody() != "" {
				bodies = append(bodies, refIssue.GetBody())
			}
		}
		instructions := common.ExtractReviewInstructions(bodies...)

		var latestCheckCompletedTime time.Time
		for _, run := range checkRuns {
			t := run.GetCompletedAt().Time
			if t.After(latestCheckCompletedTime) {
				latestCheckCompletedTime = t
			}
		}
		eventTime := pc.lastCommitTime
		var notes string
		if !latestCheckCompletedTime.IsZero() && latestCheckCompletedTime.After(pc.lastCommitTime) {
			eventTime = latestCheckCompletedTime
			notes = fmt.Sprintf("Automated review triggered; all CI checks passed at %s for commit %s (committer date %s)", latestCheckCompletedTime.Format(time.RFC3339), pc.shortSHA, pc.lastCommitTime.Format(time.RFC3339))
		} else {
			notes = fmt.Sprintf("Automated review triggered for commit %s at %s", pc.shortSHA, eventTime.Format(time.RFC3339))
		}

		task := w.newPRQueueTask(PRTaskOptions{
			Type:             api.TypePRReview,
			PR:               pc.pr,
			PRIssue:          pc.prIssue,
			Phase:            api.PhaseComments,
			CommitSHA:        pc.headSHA,
			TriggerEventTime: eventTime,
			TriggerReason:    api.TriggerReasonPRReadyForReview,
			TriggerNotes:     notes,
			Instructions:     instructions,
		})

		if w.DryRun {
			fmt.Printf("[DRYRUN] Would queue review task for PR #%d: %s\n", num, pc.prURL)
		} else {
			fmt.Printf("Queueing review task for PR #%d (Instructions: %d)...\n", num, len(instructions))
			state.lastReviewedSHA = pc.headSHA
			w.processedPRs[num] = state
			if err := writeTaskAtomically(w.incomingDir, filename, task); err != nil {
				klog.Errorf("Failed to queue review task for PR #%d: %v", num, err)
			} else {
				writeTaskJournalEvent(w.QueueDir, filename, task, "Created", 0)
			}
		}
	}
}
