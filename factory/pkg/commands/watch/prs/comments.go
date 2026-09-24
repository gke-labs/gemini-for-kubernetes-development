package prs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// prCommentAnalysis is the verdict on a pull request's outstanding feedback.
//
// The oldest unaddressed comment is singled out, not the newest: it is the one
// that has been waiting longest, and quoting it in the task gives the agent the
// start of the conversation rather than its tail.
type prCommentAnalysis struct {
	hasNewComments         bool
	unackCommentIDs        []int64
	unackPRCommentIDs      []int64
	oldestCommentTime      time.Time
	oldestCommentAuthor    string
	oldestCommentType      string
	oldestCommentID        int64
	commentsAttemptCount   int
	lastCommentsTaskFailed bool
}

// evaluateComments decides whether a pull request has feedback still waiting on
// the agent.
//
// A comment counts as outstanding only if it post-dates all three of: the last
// commit (a push is taken as the answer to everything said before it), the last
// address-comments task (whose work is not on GitHub yet), and the last bot
// reply (which already answered it in the thread). Any one of those being newer
// means the feedback has been dealt with.
//
// Reactions are the second gate. What the emoji on a comment mean, and which
// of them outrank the others, is conventions.CommentState's business; this
// function only asks whether the comment still needs attention.
//
// Human feedback always wins. Bot review feedback is held back when an
// address-comments task already ran against this exact commit, because the
// agent looking at the same review and producing no commit means it judged
// there was nothing to change - running it again would loop forever.
func (s *Scanner) evaluateComments(
	ctx context.Context,
	num int,
	pr *githubv39.PullRequest,
	history *prHistory,
	lastCommitTime time.Time,
	headSHA string,
) prCommentAnalysis {
	filename := fmt.Sprintf("task-pr-%d-comments.yaml", num)
	state := s.state.get(num)

	// O(1) sync of the successful task state from the queue's finished tasks
	if s.queue != nil {
		if lastCompleted := s.queue.GetProcessedTask(filename); lastCompleted != nil && lastCompleted.Status == api.StatusCompleted {
			if lastCompleted.CompletedAt.After(state.lastSuccessfulCommentAddressedTime) {
				state.lastSuccessfulCommentAddressedTime = lastCompleted.CompletedAt
				if lastCompleted.CommitSHA != "" {
					state.lastSuccessfulCommentAddressedSHA = lastCompleted.CommitSHA
				}
				if lastCompleted.CompletedAt.After(state.lastCommentAddressedTime) {
					state.lastCommentAddressedTime = lastCompleted.CompletedAt
					if lastCompleted.CommitSHA != "" {
						state.lastCommentAddressedSHA = lastCompleted.CommitSHA
					}
				}
				s.state.set(num, state)
			} else if lastCompleted.CompletedAt.Equal(state.lastSuccessfulCommentAddressedTime) {
				// Handle clock resolution equality: ensure SHAs are fully synchronized.
				updated := false
				if lastCompleted.CommitSHA != "" && state.lastSuccessfulCommentAddressedSHA != lastCompleted.CommitSHA {
					klog.V(4).Infof("PR %d: Clock resolution match at %v, but successful SHA differs (completed: %s, state: %s). Syncing SHA.",
						num, lastCompleted.CompletedAt, lastCompleted.CommitSHA, state.lastSuccessfulCommentAddressedSHA)
					state.lastSuccessfulCommentAddressedSHA = lastCompleted.CommitSHA
					updated = true
				}
				if lastCompleted.CompletedAt.After(state.lastCommentAddressedTime) {
					state.lastCommentAddressedTime = lastCompleted.CompletedAt
					if lastCompleted.CommitSHA != "" {
						state.lastCommentAddressedSHA = lastCompleted.CommitSHA
					}
					updated = true
				} else if lastCompleted.CompletedAt.Equal(state.lastCommentAddressedTime) {
					if lastCompleted.CommitSHA != "" && state.lastCommentAddressedSHA != lastCompleted.CommitSHA {
						klog.V(4).Infof("PR %d: Clock resolution match at %v, but addressed SHA differs (completed: %s, state: %s). Syncing SHA.",
							num, lastCompleted.CompletedAt, lastCompleted.CommitSHA, state.lastCommentAddressedSHA)
						state.lastCommentAddressedSHA = lastCompleted.CommitSHA
						updated = true
					}
				}
				if updated {
					s.state.set(num, state)
				}
			} else {
				// Log discrepancy where the completed task time is older than the existing state time.
				klog.V(4).Infof("PR %d: Completed task time %v is older than existing state time %v, bypassing update to ensure strict monotonic progression.",
					num, lastCompleted.CompletedAt, state.lastSuccessfulCommentAddressedTime)
			}
		}
	}

	lastCommentsTaskFailed := s.lastCommentsTaskFailed(filename, headSHA)
	comments := history.comments
	reviews := history.reviews
	revCommentsMap := history.revCommentsMap
	bots := s.cfg.AllowlistedBots

	commentsAttemptCount := getCommentsAttemptCount(comments, lastCommitTime, s.botMap, s.cfg.GitHubLogin, bots, s.cfg.TriggerLabel)

	var analysis prCommentAnalysis
	analysis.lastCommentsTaskFailed = lastCommentsTaskFailed
	analysis.commentsAttemptCount = commentsAttemptCount

	retryFailedComments := lastCommentsTaskFailed
	commentAddressedTime := state.lastCommentAddressedTime
	if retryFailedComments {
		commentAddressedTime = state.lastSuccessfulCommentAddressedTime
	}

	// Find the latest timestamp of any reply made by an allowlisted bot user
	// (excluding reviewer bots, whose reviews are feedback rather than replies).
	var latestBotReplyTime time.Time
	for _, c := range comments {
		if c == nil {
			continue
		}
		isAnnouncement := conventions.IsBotAnnouncement(c.GetBody())

		if !isAnnouncement && !conventions.IsReviewerBot(c.GetUser(), s.cfg.ReviewerLogins) && conventions.IsBotReply(c.GetUser(), s.cfg.GitHubLogin, bots) && c.GetCreatedAt().After(latestBotReplyTime) {
			latestBotReplyTime = c.GetCreatedAt()
		}
	}
	for _, r := range reviews {
		if r == nil {
			continue
		}
		if !conventions.IsReviewerBot(r.GetUser(), s.cfg.ReviewerLogins) && conventions.IsBotReply(r.GetUser(), s.cfg.GitHubLogin, bots) && r.GetSubmittedAt().After(latestBotReplyTime) {
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

	// Pre-filter comments that need attention to avoid duplicate filter logic and count reaction lookup requirements.
	var targetComments []*githubv39.IssueComment
	for _, c := range comments {
		if c == nil {
			continue
		}
		isReviewer := conventions.IsReviewerBot(c.GetUser(), s.cfg.ReviewerLogins)
		if !isReviewer && conventions.ShouldIgnoreUser(c.GetUser(), s.cfg.GitHubLogin, bots) {
			continue
		}
		if c.GetUser() != nil && pr.GetUser() != nil && strings.EqualFold(c.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
			continue
		}
		if conventions.HasIgnorePrefix(c.GetBody(), s.cfg.TriggerLabel) {
			continue
		}
		if c.GetCreatedAt().After(lastCommitTime) && c.GetCreatedAt().After(commentAddressedTime) && c.GetCreatedAt().After(latestBotReplyTime) {
			targetComments = append(targetComments, c)
		}
	}

	if len(targetComments) > 10 {
		klog.V(4).Infof("PR #%d has a large number of comments (%d) requiring reaction lookups. This may consume many GitHub API calls and risk rate-limiting.", num, len(targetComments))
	}

	for _, c := range targetComments {
		isReviewer := conventions.IsReviewerBot(c.GetUser(), s.cfg.ReviewerLogins)
		commentState := s.reactions.CommentState(ctx, c.GetID())
		if commentState.Resolved {
			continue
		}
		if !retryFailedComments && !commentState.NeedsAttention() {
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

	// Also check inline PR review comments directly
	for _, r := range reviews {
		if r == nil {
			continue
		}
		isReviewer := conventions.IsReviewerBot(r.GetUser(), s.cfg.ReviewerLogins)
		if !isReviewer && conventions.ShouldIgnoreUser(r.GetUser(), s.cfg.GitHubLogin, bots) {
			if r.GetSubmittedAt().After(latestBotReplyTime) {
				latestBotReplyTime = r.GetSubmittedAt()
			}
			continue
		}
		if r.GetUser() != nil && pr.GetUser() != nil && strings.EqualFold(r.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
			continue
		}
		if r.GetSubmittedAt().After(lastCommitTime) && r.GetSubmittedAt().After(commentAddressedTime) && r.GetSubmittedAt().After(latestBotReplyTime) {
			if conventions.HasIgnorePrefix(r.GetBody(), s.cfg.TriggerLabel) {
				continue
			}
			if isReviewer {
				hasNewBotReviews = true
			} else {
				hasNewHumanComments = true
			}
			// A review with an empty body carries no instruction of its own -
			// its inline comments below are the feedback.
			if strings.TrimSpace(r.GetBody()) != "" {
				author := ""
				if r.GetUser() != nil {
					author = r.GetUser().GetLogin()
				}
				updateOldestComment(r.GetSubmittedAt(), author, "review", r.GetID())
			}
		}

		revComments := revCommentsMap[r.GetID()]
		for _, rc := range revComments {
			if rc == nil {
				continue
			}
			isInlineReviewer := conventions.IsReviewerBot(rc.GetUser(), s.cfg.ReviewerLogins)
			if !isInlineReviewer && conventions.ShouldIgnoreUser(rc.GetUser(), s.cfg.GitHubLogin, bots) {
				if rc.GetCreatedAt().After(latestBotReplyTime) {
					latestBotReplyTime = rc.GetCreatedAt()
				}
				continue
			}
			if rc.GetUser() != nil && pr.GetUser() != nil && strings.EqualFold(rc.GetUser().GetLogin(), pr.GetUser().GetLogin()) {
				continue
			}
			if rc.GetCreatedAt().After(lastCommitTime) && rc.GetCreatedAt().After(commentAddressedTime) && rc.GetCreatedAt().After(latestBotReplyTime) {
				if conventions.HasIgnorePrefix(rc.GetBody(), s.cfg.TriggerLabel) {
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
		if !lastCommentsTaskFailed && state.lastCommentAddressedSHA != "" && state.lastCommentAddressedSHA == headSHA {
			klog.Infof("Skipping bot review feedback on PR #%d because an address-comments task already ran against SHA %s without resulting in a commit.", num, headSHA)
		} else {
			analysis.hasNewComments = true
		}
	}

	return analysis
}

// hasBotReviewAfterLastCommit reports whether the current head has already been
// reviewed by a bot, which is what stops a second review being queued for the
// same revision.
func hasBotReviewAfterLastCommit(reviews []*githubv39.PullRequestReview, lastCommitTime time.Time, headSHA, githubLogin string, bots []string) bool {
	for _, r := range reviews {
		if r == nil {
			continue
		}
		if conventions.IsBotReply(r.GetUser(), githubLogin, bots) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			return true
		}
	}
	return false
}

// CommentCategory represents the classification of a comment's author.
type CommentCategory int

const (
	CategoryOther CommentCategory = iota
	CategoryHuman
	CategoryPoolBot
)

// classifyComment determines if a comment is from a human, a pool bot, or should be ignored.
func classifyComment(
	c *githubv39.IssueComment,
	botMap map[string]struct{},
	githubLogin string,
	bots []string,
	triggerLabel string,
) CommentCategory {
	if c == nil {
		return CategoryOther
	}
	if c.GetUser() == nil {
		return CategoryOther
	}
	login := c.GetUser().GetLogin()
	if login == "" {
		return CategoryOther
	}
	if strings.EqualFold(login, githubLogin) {
		return CategoryPoolBot
	}
	if _, ok := botMap[strings.ToLower(login)]; ok {
		return CategoryPoolBot
	}
	if conventions.ShouldIgnoreUser(c.GetUser(), githubLogin, bots) {
		return CategoryOther
	}
	if conventions.HasIgnorePrefix(c.GetBody(), triggerLabel) {
		return CategoryOther
	}
	return CategoryHuman
}

// getAttemptCount generalizes the count of bot task attempts since the last reset.
//
// The sequential iteration here assumes that the incoming comments slice is
// already sorted chronologically (ascending), which aligns with the GitHub API's
// natural ordering.
//
// To be fully robust against unsorted input, this function explicitly checks
// sorting and, if needed, sorts the slice chronologically first.
//
// The attempt counter resets on a new commit (handled via lastCommitTime)
// or on any human comment, or when a pause comment is posted.
// To avoid redundant O(N) classifications, we scan backwards to locate the latest
// reset point first, and then count onwards from that point.
func getAttemptCount(
	comments []*githubv39.IssueComment,
	lastCommitTime time.Time,
	botMap map[string]struct{},
	githubLogin string,
	bots []string,
	triggerLabel string,
	pauseSubstring string,
	startSubstring string,
) int {
	// Filter out nil comments to avoid nil checks in subsequent loops.
	var cleanComments []*githubv39.IssueComment
	for _, c := range comments {
		if c != nil {
			cleanComments = append(cleanComments, c)
		}
	}

	// Ensure cleanComments is sorted chronologically (ascending) by CreatedAt.
	isSorted := true
	for i := 1; i < len(cleanComments); i++ {
		if cleanComments[i].GetCreatedAt().Before(cleanComments[i-1].GetCreatedAt()) {
			isSorted = false
			break
		}
	}
	if !isSorted {
		sort.SliceStable(cleanComments, func(i, j int) bool {
			return cleanComments[i].GetCreatedAt().Before(cleanComments[j].GetCreatedAt())
		})
	}

	classified := make([]CommentCategory, len(cleanComments))
	for i := range classified {
		classified[i] = -1 // unclassified
	}

	getClassified := func(idx int) CommentCategory {
		if classified[idx] != -1 {
			return classified[idx]
		}
		classified[idx] = classifyComment(cleanComments[idx], botMap, githubLogin, bots, triggerLabel)
		return classified[idx]
	}

	// Locate the latest reset point by scanning backwards.
	// Since comments are sorted chronologically, once we find a comment whose CreatedAt
	// is not after lastCommitTime, we can stop scanning because all preceding comments
	// are also before or equal to lastCommitTime.
	resetIdx := -1
	firstAfterCommitIdx := 0
	for i := len(cleanComments) - 1; i >= 0; i-- {
		c := cleanComments[i]
		if !c.GetCreatedAt().After(lastCommitTime) {
			firstAfterCommitIdx = i + 1
			break
		}
		category := getClassified(i)
		isHuman := category == CategoryHuman
		isPause := strings.Contains(strings.ToLower(c.GetBody()), strings.ToLower(pauseSubstring))
		if isHuman || isPause {
			resetIdx = i
			break
		}
	}

	startIdx := firstAfterCommitIdx
	if resetIdx != -1 {
		startIdx = resetIdx + 1
	}

	// Count bot task attempts after the latest reset point
	attemptCount := 0
	for i := startIdx; i < len(cleanComments); i++ {
		c := cleanComments[i]
		category := getClassified(i)
		if category == CategoryPoolBot &&
			strings.Contains(strings.ToLower(c.GetBody()), strings.ToLower(startSubstring)) {
			attemptCount++
		}
	}
	return attemptCount
}

// getInvestigationCount counts how many times the watcher has investigated CI
// failures since the last thing that ought to reset its patience.
//
// The counter resets on a new commit or on any human comment, because either
// one changes the situation the previous attempts failed against. Without the
// reset a pull request a human has just given a hint on would stay stuck at the
// retry limit.
func getInvestigationCount(comments []*githubv39.IssueComment, lastCommitTime time.Time, botMap map[string]struct{}, githubLogin string, bots []string, triggerLabel string) int {
	return getAttemptCount(
		comments,
		lastCommitTime,
		botMap,
		githubLogin,
		bots,
		triggerLabel,
		conventions.AnnouncementPausingInvestigation,
		conventions.AnnouncementStartedInvestigatingCheck,
	)
}

// getCommentsAttemptCount counts how many times the watcher has attempted to address comments
// since the last thing that ought to reset its patience.
//
// The counter resets on a new commit or on any human comment, because either
// one changes the situation the previous attempts failed against.
func getCommentsAttemptCount(comments []*githubv39.IssueComment, lastCommitTime time.Time, botMap map[string]struct{}, githubLogin string, bots []string, triggerLabel string) int {
	return getAttemptCount(
		comments,
		lastCommitTime,
		botMap,
		githubLogin,
		bots,
		triggerLabel,
		conventions.AnnouncementPausingProcessing,
		conventions.AnnouncementStartedAddressingReview,
	)
}
