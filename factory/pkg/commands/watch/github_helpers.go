package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func assignedBotUser(issue *githubv39.Issue, botUsers []string) string {
	for _, u := range issue.Assignees {
		for _, bot := range botUsers {
			if strings.EqualFold(u.GetLogin(), bot) {
				return u.GetLogin()
			}
		}
	}
	return ""
}

func addGitHubComment(ctx context.Context, client *githubv39.Client, owner, repo string, number int, body string) {
	comment := &githubv39.IssueComment{
		Body: githubv39.String(body),
	}
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, comment)
	if err != nil {
		klog.Errorf("Failed to create GitHub comment on #%d: %v", number, err)
	}
}

func listAllOpenPRs(ctx context.Context, ghClient *githubv39.Client, owner, repo string) ([]*githubv39.PullRequest, error) {
	var allPRs []*githubv39.PullRequest
	opts := &githubv39.PullRequestListOptions{
		State:       "open",
		ListOptions: githubv39.ListOptions{PerPage: 100},
	}
	for {
		prs, resp, err := ghClient.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}
		allPRs = append(allPRs, prs...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allPRs, nil
}

func syncReferencedIssueLabels(ctx context.Context, ghClient *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, prIssue *githubv39.Issue) {
	var refIssues []*githubv39.Issue
	for refIssueNum := range common.GetReferencedIssues(pr) {
		refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
		if err != nil {
			klog.Warningf("Failed to fetch referenced parent issue #%d for PR #%d: %v", refIssueNum, pr.GetNumber(), err)
			continue
		}
		refIssues = append(refIssues, refIssue)
	}

	allMissingLabels := getMissingLabelsForPR(prIssue.Labels, refIssues)

	if len(allMissingLabels) > 0 {
		klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, pr.GetNumber())
		if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), allMissingLabels); err != nil {
			klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, pr.GetNumber(), err)
		}
	}
}

func getMissingLabelsForPR(prLabels []*githubv39.Label, refIssues []*githubv39.Issue) []string {
	prLabelsSet := make(map[string]bool)
	for _, label := range prLabels {
		if label.GetName() != "" {
			prLabelsSet[label.GetName()] = true
		}
	}

	var allMissingLabels []string
	missingLabelsSet := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}

		for _, label := range refIssue.Labels {
			labelName := label.GetName()
			if labelName != "" && !prLabelsSet[labelName] && !missingLabelsSet[labelName] {
				missingLabelsSet[labelName] = true
				allMissingLabels = append(allMissingLabels, labelName)
			}
		}
	}

	return allMissingLabels
}

func hasLinkedPR(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int) (bool, error) {
	// 1. Try timeline check (quick and standard)
	timeline, _, err := client.Issues.ListIssueTimeline(ctx, owner, repo, issueNum, nil)
	if err == nil {
		for _, event := range timeline {
			if event.GetEvent() == "cross-referenced" && event.Source != nil {
				if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
					if event.Source.Issue.GetState() == "open" {
						return true, nil
					}
				}
			}
		}
	} else {
		klog.Warningf("Failed to list issue timeline for #%d: %v. Falling back to search API.", issueNum, err)
	}

	// 2. Fallback to Search API: search for open PRs referencing the issue number
	query := fmt.Sprintf("repo:%s/%s type:pr state:open \"%d\"", owner, repo, issueNum)
	opts := &githubv39.SearchOptions{
		ListOptions: githubv39.ListOptions{PerPage: 10},
	}
	result, _, err := client.Search.Issues(ctx, query, opts)
	if err != nil {
		return false, fmt.Errorf("failed to search PRs for issue #%d: %w", issueNum, err)
	}

	if result.GetTotal() > 0 {
		return true, nil
	}

	return false, nil
}

func hasLinkedPRWithTimeline(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int, timeline []*githubv39.Timeline) (bool, error) {
	if timeline != nil {
		for _, event := range timeline {
			if event.GetEvent() == "cross-referenced" && event.Source != nil {
				if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
					if event.Source.Issue.GetState() == "open" {
						return true, nil
					}
				}
			}
		}
		return false, nil
	}
	return hasLinkedPR(ctx, client, owner, repo, issueNum)
}

func getIssueTriggerInfo(issue *githubv39.Issue, timeline []*githubv39.Timeline, triggerLabel string, wasAutoLabeled bool) (time.Time, string, string) {
	createTime := issue.GetCreatedAt()
	num := issue.GetNumber()

	if wasAutoLabeled {
		user := ""
		if issue.GetUser() != nil {
			user = issue.GetUser().GetLogin()
		}
		reason := fmt.Sprintf("Issue #%d created", num)
		if user != "" {
			reason = fmt.Sprintf("Issue #%d created by %s", num, user)
		}
		notes := fmt.Sprintf("Issue #%d created at %s; trigger label '%s' auto-applied by watcher", num, createTime.Format(time.RFC3339), triggerLabel)
		return createTime, reason, notes
	}

	var latestLabelTime time.Time
	var labelActor string
	for _, event := range timeline {
		if event.GetEvent() == "labeled" && event.GetLabel() != nil {
			if strings.EqualFold(event.GetLabel().GetName(), triggerLabel) {
				t := event.GetCreatedAt()
				if !t.IsZero() && t.After(latestLabelTime) {
					latestLabelTime = t
					if event.GetActor() != nil {
						labelActor = event.GetActor().GetLogin()
					}
				}
			}
		}
	}

	if !latestLabelTime.IsZero() && latestLabelTime.After(createTime) {
		actorStr := ""
		if labelActor != "" {
			actorStr = fmt.Sprintf(" by %s", labelActor)
		}
		reason := fmt.Sprintf("Trigger label '%s' added to issue #%d%s", triggerLabel, num, actorStr)
		notes := fmt.Sprintf("Issue #%d created at %s; trigger label '%s' added%s at %s", num, createTime.Format(time.RFC3339), triggerLabel, actorStr, latestLabelTime.Format(time.RFC3339))
		return latestLabelTime, reason, notes
	}

	reason := fmt.Sprintf("Issue #%d created with label '%s'", num, triggerLabel)
	notes := fmt.Sprintf("Issue #%d created at %s with trigger label '%s'", num, createTime.Format(time.RFC3339), triggerLabel)
	return createTime, reason, notes
}

func isPRApprovedOrLGTM(pr *githubv39.PullRequest, prIssue *githubv39.Issue, reviews []*githubv39.PullRequestReview) bool {
	// 1. Check labels
	for _, label := range prIssue.Labels {
		if strings.EqualFold(label.GetName(), "lgtm") || strings.EqualFold(label.GetName(), "approved") {
			return true
		}
	}

	// 2. Check reviews
	hasApproved := false
	hasChangesRequested := false
	latestReviews := make(map[string]string)
	for _, r := range reviews {
		if r.GetUser() != nil && r.GetState() != "" {
			latestReviews[r.GetUser().GetLogin()] = r.GetState()
		}
	}
	for _, state := range latestReviews {
		if state == "APPROVED" {
			hasApproved = true
		} else if state == "CHANGES_REQUESTED" {
			hasChangesRequested = true
		}
	}

	return hasApproved && !hasChangesRequested
}

func isReviewerBot(user *githubv39.User, cfg *config.FactoryConfig) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if cfg != nil {
		if reviewerRole, ok := cfg.Roles["reviewer"]; ok {
			for _, u := range reviewerRole.Users {
				if strings.EqualFold(login, u) {
					return true
				}
			}
		}
	}
	return strings.Contains(strings.ToLower(login), "reviewbot")
}

func isBotReply(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true
	}
	for _, b := range allowlistedBots {
		if strings.EqualFold(login, b) {
			return true
		}
	}
	return shouldIgnoreUser(user, githubLogin, nil)
}

func shouldIgnoreUser(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true // always ignore our own bot
	}

	loginLower := strings.ToLower(login)
	isBotUser := strings.EqualFold(user.GetType(), "Bot") ||
		strings.HasSuffix(loginLower, "[bot]") ||
		strings.HasSuffix(loginLower, "-bot") ||
		strings.HasSuffix(loginLower, "-robot") ||
		strings.Contains(loginLower, "prow")

	if isBotUser {
		// Check if it's in the allowlist
		for _, b := range allowlistedBots {
			if strings.EqualFold(login, b) {
				return false // DO NOT ignore (it is allowlisted)
			}
		}
		return true // ignore since it is not allowlisted
	}

	return false
}

func hasStopLabel(labels []*githubv39.Label, triggerLabel string) bool {
	stopLabels := []string{"overseer/stop"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		stopLabels = append(stopLabels, triggerLabel+"/stop")
	}
	for _, label := range labels {
		for _, stop := range stopLabels {
			if strings.EqualFold(label.GetName(), stop) {
				return true
			}
		}
	}
	return false
}

func getInvestigationCount(comments []*githubv39.IssueComment, lastCommitTime time.Time, allBotUsers []string, githubLogin string, bots []string) int {
	lastResetTime := lastCommitTime
	for _, c := range comments {
		isPoolBot := false
		for _, bot := range allBotUsers {
			if strings.EqualFold(c.GetUser().GetLogin(), bot) {
				isPoolBot = true
				break
			}
		}
		isHuman := !isPoolBot && !shouldIgnoreUser(c.GetUser(), githubLogin, bots)
		if (isHuman || strings.Contains(c.GetBody(), "pausing automated investigation")) && c.GetCreatedAt().After(lastResetTime) {
			lastResetTime = c.GetCreatedAt()
		}
	}

	investigationCount := 0
	for _, c := range comments {
		isPoolBot := false
		for _, bot := range allBotUsers {
			if strings.EqualFold(c.GetUser().GetLogin(), bot) {
				isPoolBot = true
				break
			}
		}
		if isPoolBot &&
			strings.Contains(c.GetBody(), "started investigating CI check failures") &&
			c.GetCreatedAt().After(lastResetTime) {
			investigationCount++
		}
	}
	return investigationCount
}

func getStopLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		return triggerLabel + "/stop"
	}
	return "overseer/stop"
}

func hasReviewLabel(labels []*githubv39.Label, triggerLabel string) bool {
	reviewLabels := []string{"overseer/review"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		reviewLabels = append(reviewLabels, triggerLabel+"/review")
	}
	for _, label := range labels {
		for _, rev := range reviewLabels {
			if strings.EqualFold(label.GetName(), rev) {
				return true
			}
		}
	}
	return false
}

func shouldAutoReviewPR(ctx context.Context, ghClient *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, prIssue *githubv39.Issue, triggerLabel string) bool {
	if hasReviewLabel(prIssue.Labels, triggerLabel) {
		return true
	}
	for refIssueNum := range common.GetReferencedIssues(pr) {
		refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
		if err == nil && hasReviewLabel(refIssue.Labels, triggerLabel) {
			return true
		}
	}
	return false
}

func getReadyForHumanLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		return triggerLabel + "/ready-for-human"
	}
	return "overseer/ready-for-human"
}

func hasReadyForHumanLabel(labels []*githubv39.Label, triggerLabel string) bool {
	readyLabels := []string{"overseer/ready-for-human"}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, "overseer") {
		readyLabels = append(readyLabels, triggerLabel+"/ready-for-human")
	}
	for _, label := range labels {
		for _, ready := range readyLabels {
			if strings.EqualFold(label.GetName(), ready) {
				return true
			}
		}
	}
	return false
}

func hasCompletedBotReviewOnHead(reviews []*githubv39.PullRequestReview, headSHA string, lastCommitTime time.Time, cfg *config.FactoryConfig) bool {
	var latestReview *githubv39.PullRequestReview
	for _, r := range reviews {
		if isReviewerBot(r.GetUser(), cfg) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			if latestReview == nil || r.GetSubmittedAt().After(latestReview.GetSubmittedAt()) {
				latestReview = r
			}
		}
	}
	if latestReview == nil {
		return false
	}
	return latestReview.GetState() != "CHANGES_REQUESTED"
}

func (w *Watcher) reconcileReadyForHumanLabel(ctx context.Context, num int, prIssue *githubv39.Issue, isReady bool, headSHA string) {
	if w.ghClient == nil || prIssue == nil {
		return
	}
	readyLabel := getReadyForHumanLabel(w.triggerLabel)
	hasLabel := hasReadyForHumanLabel(prIssue.Labels, w.triggerLabel)

	if isReady && !hasLabel {
		if w.DryRun {
			fmt.Printf("[DRYRUN] Would add label '%s' to PR #%d (passed review on SHA %s)\n", readyLabel, num, headSHA)
		} else {
			klog.Infof("PR #%d passed automated review on SHA %s. Adding label '%s'.", num, headSHA, readyLabel)
			if _, _, err := w.ghClient.Issues.AddLabelsToIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, []string{readyLabel}); err != nil {
				klog.Errorf("Failed to add label '%s' to PR #%d: %v", readyLabel, num, err)
			}
		}
	} else if !isReady && hasLabel {
		if w.DryRun {
			fmt.Printf("[DRYRUN] Would remove label '%s' from PR #%d\n", readyLabel, num)
		} else {
			klog.Infof("PR #%d is no longer ready for human review. Removing label '%s'.", num, readyLabel)
			if _, err := w.ghClient.Issues.RemoveLabelForIssue(ctx, w.Repo.Owner, w.Repo.Repo, num, readyLabel); err != nil {
				klog.Errorf("Failed to remove label '%s' from PR #%d: %v", readyLabel, num, err)
			}
		}
	}
}

func (w *Watcher) selectUserForTask(ctx context.Context, taskType string, prNum int) (string, error) {
	if w.cfg == nil || len(w.cfg.Roles) == 0 {
		return "", nil // default fallback to factory-user
	}

	// 1. Determine role for task type
	role := ""
	for roleName, rCfg := range w.cfg.Roles {
		for _, t := range rCfg.Tasks {
			if strings.EqualFold(t, taskType) {
				role = roleName
				break
			}
		}
		if role != "" {
			break
		}
	}

	if role == "" {
		switch {
		case taskType == "agent-chore":
			if rCfg, ok := w.cfg.Roles["agent"]; ok && len(rCfg.Users) > 0 {
				role = "agent"
			} else {
				role = "coder"
			}
		case isPRTask(taskType):
			if prNum > 0 {
				pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
				if err == nil {
					author := pr.GetUser().GetLogin()
					if author != "" {
						inAgentPool := false
						if agentCfg, ok := w.cfg.Roles["agent"]; ok {
							for _, u := range agentCfg.Users {
								if strings.EqualFold(u, author) {
									inAgentPool = true
									break
								}
							}
						}
						if inAgentPool {
							role = "agent"
						} else {
							role = "coder"
						}
					}
				}
			}
			if role == "" {
				role = "coder"
			}
		case taskType == "issue-fix":
			role = "coder"
		case taskType == "pr-review":
			role = "reviewer"
		default:
			return "", nil // default fallback
		}
	}

	roleCfg, exists := w.cfg.Roles[role]
	if !exists || len(roleCfg.Users) == 0 {
		if role == "agent" {
			role = "coder"
			roleCfg, exists = w.cfg.Roles[role]
		}
		if !exists || len(roleCfg.Users) == 0 {
			return "", nil // default fallback
		}
	}

	// 2. Select bot based on new vs existing PR/Issue
	isIssueTask := taskType == "issue-fix" || taskType == "agent-chore"
	if isIssueTask {
		if prNum > 0 {
			// A. First check if a Sandbox already exists for this task on the cluster
			// and has been pinned to a specific user.
			var sandboxName string
			if taskType == "issue-fix" {
				sandboxName = fmt.Sprintf("fix-%s-%d", w.Repo.Repo, prNum)
			} else if taskType == "agent-chore" {
				sandboxName = fmt.Sprintf("wf-issue-%d", prNum)
			}

			if sandboxName != "" && w.kubeClient != nil {
				sb, err := w.kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(w.Namespace).Get(ctx, sandboxName, metav1.GetOptions{})
				if err == nil {
					labels := sb.GetLabels()
					if user, ok := labels["factory.gemini.google.com/user"]; ok && user != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, user) {
								inPool = true
								break
							}
						}
						if inPool {
							klog.Infof("Pinned user '%s' for issue #%d from existing sandbox '%s'", user, prNum, sandboxName)
							return user, nil
						}
					}
				}
			}

			// B. Fallback to GitHub issue assignee check
			issue, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
			if err == nil {
				for _, a := range issue.Assignees {
					assignee := a.GetLogin()
					if assignee != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, assignee) {
								inPool = true
								break
							}
						}
						if inPool {
							return assignee, nil
						}
					}
				}
			} else {
				klog.Warningf("Failed to fetch issue details for task %s #%d: %v", taskType, prNum, err)
			}
		}

		idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
		return roleCfg.Users[idx], nil
	}

	if prNum > 0 {
		pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
		if err != nil {
			return "", fmt.Errorf("fetching PR details: %w", err)
		}
		author := pr.GetUser().GetLogin()
		if author == "" {
			return "", fmt.Errorf("empty author login for PR %d", prNum)
		}

		if taskType == "pr-review" {
			reviewerRoleCfg, ok := w.cfg.Roles["reviewer"]
			if ok && len(reviewerRoleCfg.Users) > 0 {
				idx := time.Now().UnixNano() % int64(len(reviewerRoleCfg.Users))
				return reviewerRoleCfg.Users[idx], nil
			}
			idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
			return roleCfg.Users[idx], nil
		}

		inPool := false
		for _, u := range roleCfg.Users {
			if strings.EqualFold(u, author) {
				inPool = true
				break
			}
		}
		if !inPool {
			return "", fmt.Errorf("PR author '%s' is not in the configured bot pool for role '%s'", author, role)
		}
		return author, nil
	}

	return "", nil
}

func hasIssueCommentReaction(ctx context.Context, ghClient *githubv39.Client, owner, repo string, commentID int64, content string, filterBot bool, bots []string, selfLogin string) bool {
	if ghClient == nil {
		return false
	}
	reactions, _, err := ghClient.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID, nil)
	if err != nil {
		return false
	}
	for _, r := range reactions {
		if r.GetContent() == content {
			isBot := shouldIgnoreUser(r.GetUser(), selfLogin, bots)
			if filterBot && isBot {
				return true
			} else if !filterBot && !isBot {
				return true
			}
		}
	}
	return false
}

func resolvePRCommentReactions(ctx context.Context, ghClient *githubv39.Client, owner, repo string, prNum int, resolutionContent string, bots []string, selfLogin string) {
	if ghClient == nil {
		return
	}
	comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, prNum, nil)
	if err != nil {
		return
	}
	for _, c := range comments {
		if shouldIgnoreUser(c.GetUser(), selfLogin, bots) {
			continue
		}
		if hasIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), "eyes", true, bots, selfLogin) {
			addIssueCommentReaction(ctx, ghClient, owner, repo, c.GetID(), resolutionContent)
		}
	}
}

func addIssueCommentReaction(ctx context.Context, ghClient *githubv39.Client, owner, repo string, commentID int64, content string) {
	if ghClient == nil {
		return
	}
	_, _, err := ghClient.Reactions.CreateIssueCommentReaction(ctx, owner, repo, commentID, content)
	if err != nil {
		klog.Warningf("Failed to create reaction '%s' on comment %d: %v", content, commentID, err)
	}
}

func addPullRequestCommentReaction(ctx context.Context, ghClient *githubv39.Client, owner, repo string, commentID int64, content string) {
	if ghClient == nil {
		return
	}
	_, _, err := ghClient.Reactions.CreatePullRequestCommentReaction(ctx, owner, repo, commentID, content)
	if err != nil {
		klog.Warningf("Failed to create reaction '%s' on PR review comment %d: %v", content, commentID, err)
	}
}

func getLastPRActivityTime(pr *githubv39.PullRequest, comments []*githubv39.IssueComment, reviews []*githubv39.PullRequestReview, revComments map[int64][]*githubv39.PullRequestComment, githubLogin string, bots []string) time.Time {
	lastActivity := pr.GetCreatedAt()

	// 1. Check issue comments
	for _, c := range comments {
		isBot := isBotReply(c.GetUser(), githubLogin, bots)
		if !isBot {
			if c.GetCreatedAt().After(lastActivity) {
				lastActivity = c.GetCreatedAt()
			}
		}
	}

	// 2. Check reviews and review comments
	for _, r := range reviews {
		if !isBotReply(r.GetUser(), githubLogin, bots) {
			if r.GetSubmittedAt().After(lastActivity) {
				lastActivity = r.GetSubmittedAt()
			}
		}

		if rcList, ok := revComments[r.GetID()]; ok {
			for _, rc := range rcList {
				if !isBotReply(rc.GetUser(), githubLogin, bots) {
					if rc.GetCreatedAt().After(lastActivity) {
						lastActivity = rc.GetCreatedAt()
					}
				}
			}
		}
	}

	return lastActivity
}

func hasInactivityComment(comments []*githubv39.IssueComment, lastActivity time.Time) bool {
	for _, c := range comments {
		if strings.Contains(c.GetBody(), "paused automated processing on this pull request due to a period of inactivity") {
			if c.GetCreatedAt().After(lastActivity) {
				return true
			}
		}
	}
	return false
}
