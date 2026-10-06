package prs

import (
	"context"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
)

// prCommentAnalysis is the verdict on a pull request's outstanding feedback.
//
// The oldest unaddressed comment is singled out, not the newest: it is the one
// that has been waiting longest, and quoting it in the task gives the agent the
// start of the conversation rather than its tail.
type prCommentAnalysis struct {
	hasNewComments    bool
	unackCommentIDs   []int64
	unackPRCommentIDs []int64
	// unackReviewNodeIDs are the reviews whose bodies count as feedback. They
	// are held by node ID because reactions on a review body can only be
	// reached through GraphQL.
	unackReviewNodeIDs  []string
	oldestCommentTime   time.Time
	oldestCommentAuthor string
	oldestCommentType   string
	oldestCommentID     int64
}

// evaluateComments decides whether a pull request has new feedback from a human
// or reviewbot since the last commit and the last address-comments task.
//
// A comment counts as outstanding only if it post-dates both the last commit
// (a push is taken as the answer to everything said before it) and the last
// address-comments task, and its reactions say it still needs attention
// (feedback.Pending).
func (s *Scanner) evaluateComments(
	ctx context.Context,
	pr *githubv39.PullRequest,
	history *prHistory,
	lastCommitTime, lastCommentAddressedTime time.Time,
) prCommentAnalysis {
	var analysis prCommentAnalysis

	updateOldestComment := func(t time.Time, author string, cType string, id int64) {
		if !t.IsZero() && (analysis.oldestCommentTime.IsZero() || t.Before(analysis.oldestCommentTime)) {
			analysis.oldestCommentTime = t
			analysis.oldestCommentAuthor = author
			analysis.oldestCommentType = cType
			analysis.oldestCommentID = id
		}
	}

	h := &feedback.History{
		Comments:       history.comments,
		Reviews:        history.reviews,
		ReviewComments: history.revCommentsMap,
		LastCommitTime: history.lastCommitTime,
	}
	for _, it := range feedback.Pending(ctx, pr, h, s.feedbackPolicy(), s.reactions, lastCommitTime, lastCommentAddressedTime) {
		analysis.hasNewComments = true
		switch it.Kind {
		case feedback.KindComment:
			analysis.unackCommentIDs = append(analysis.unackCommentIDs, it.ID)
			updateOldestComment(it.At, it.Author, "comment", it.ID)
		case feedback.KindReview:
			if it.NodeID != "" {
				analysis.unackReviewNodeIDs = append(analysis.unackReviewNodeIDs, it.NodeID)
			}
			updateOldestComment(it.At, it.Author, "review", it.ID)
		case feedback.KindReviewComment:
			analysis.unackPRCommentIDs = append(analysis.unackPRCommentIDs, it.ID)
			updateOldestComment(it.At, it.Author, "inline review comment", it.ID)
		}
	}

	return analysis
}

// feedbackPolicy is whose feedback the scanner acts on: anyone but its own
// account and unlisted bots, never the pull request's (bot) author.
func (s *Scanner) feedbackPolicy() feedback.Policy {
	return feedback.Policy{
		SelfLogin:       s.cfg.GitHubLogin,
		AllowlistedBots: s.cfg.AllowlistedBots,
		ReviewerLogins:  s.cfg.ReviewerLogins,
		TriggerLabel:    s.cfg.TriggerLabel,
	}
}

// hasBotReviewAfterLastCommit reports whether the current head has already been
// reviewed by a bot, which is what stops a second review being queued for the
// same revision.
func hasBotReviewAfterLastCommit(reviews []*githubv39.PullRequestReview, lastCommitTime time.Time, headSHA, githubLogin string, bots []string) bool {
	for _, r := range reviews {
		if conventions.IsBotReply(r.GetUser(), githubLogin, bots) && (r.GetSubmittedAt().After(lastCommitTime) || r.GetCommitID() == headSHA) {
			return true
		}
	}
	return false
}

// getInvestigationCount counts how many times the watcher has investigated CI
// failures since the last thing that ought to reset its patience.
//
// The counter resets on a new commit or on any human comment, because either
// one changes the situation the previous attempts failed against. Without the
// reset a pull request a human has just given a hint on would stay stuck at the
// retry limit.
func getInvestigationCount(comments []*githubv39.IssueComment, lastCommitTime time.Time, allBotUsers []string, githubLogin string, bots []string, triggerLabel string) int {
	lastResetTime := lastCommitTime
	for _, c := range comments {
		isPoolBot := false
		for _, bot := range allBotUsers {
			if strings.EqualFold(c.GetUser().GetLogin(), bot) {
				isPoolBot = true
				break
			}
		}
		isHuman := !isPoolBot && !conventions.ShouldIgnoreUser(c.GetUser(), githubLogin, bots)
		if isHuman && conventions.HasIgnorePrefix(c.GetBody(), triggerLabel) {
			isHuman = false
		}
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

// hasCommentPauseAfter reports whether the watcher has posted a comment pausing
// automated feedback addressing since the given timestamp.
func hasCommentPauseAfter(comments []*githubv39.IssueComment, after time.Time) bool {
	for _, c := range comments {
		if strings.Contains(c.GetBody(), "pausing automated feedback addressing") && c.GetCreatedAt().After(after) {
			return true
		}
	}
	return false
}
