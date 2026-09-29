package prs

import (
	"context"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// prCommentAnalysis is the verdict on a pull request's outstanding feedback.
//
// The oldest unaddressed comment is singled out, not the newest: it is the one
// that has been waiting longest, and quoting it in the task gives the agent the
// start of the conversation rather than its tail.
type prCommentAnalysis struct {
	hasNewComments      bool
	unackCommentIDs     []int64
	unackPRCommentIDs   []int64
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
// address-comments task.
//
// Reactions are the second gate. What the emoji on a comment mean, and which
// of them outrank the others, is conventions.CommentState's business; this
// function only asks whether the comment still needs attention.
func (s *Scanner) evaluateComments(
	ctx context.Context,
	pr *githubv39.PullRequest,
	history *prHistory,
	lastCommitTime, lastCommentAddressedTime time.Time,
) prCommentAnalysis {
	var analysis prCommentAnalysis

	comments := history.comments
	reviews := history.reviews
	revCommentsMap := history.revCommentsMap

	updateOldestComment := func(t time.Time, author string, cType string, id int64) {
		if !t.IsZero() && (analysis.oldestCommentTime.IsZero() || t.Before(analysis.oldestCommentTime)) {
			analysis.oldestCommentTime = t
			analysis.oldestCommentAuthor = author
			analysis.oldestCommentType = cType
			analysis.oldestCommentID = id
		}
	}

	for _, c := range comments {
		if s.ignoreFeedback(pr, feedback{
			user: c.GetUser(),
			body: c.GetBody(),
			at:   c.GetCreatedAt(),
		}, lastCommitTime, lastCommentAddressedTime) {
			continue
		}
		if !s.reactions.CommentState(ctx, c.GetID()).NeedsAttention() {
			continue
		}
		analysis.hasNewComments = true
		analysis.unackCommentIDs = append(analysis.unackCommentIDs, c.GetID())
		updateOldestComment(c.GetCreatedAt(), c.GetUser().GetLogin(), "comment", c.GetID())
	}

	for _, r := range reviews {
		// The review body and its inline comments are judged independently:
		// an empty or approving review body can still carry inline feedback.
		if !s.ignoreFeedback(pr, feedback{
			user:        r.GetUser(),
			body:        r.GetBody(),
			reviewState: r.GetState(),
			at:          r.GetSubmittedAt(),
		}, lastCommitTime, lastCommentAddressedTime) {
			analysis.hasNewComments = true
			updateOldestComment(r.GetSubmittedAt(), r.GetUser().GetLogin(), "review", r.GetID())
		}

		for _, rc := range revCommentsMap[r.GetID()] {
			if s.ignoreFeedback(pr, feedback{
				user: rc.GetUser(),
				body: rc.GetBody(),
				at:   rc.GetCreatedAt(),
			}, lastCommitTime, lastCommentAddressedTime) {
				continue
			}
			analysis.hasNewComments = true
			analysis.unackPRCommentIDs = append(analysis.unackPRCommentIDs, rc.GetID())
			updateOldestComment(rc.GetCreatedAt(), rc.GetUser().GetLogin(), "inline review comment", rc.GetID())
		}
	}

	return analysis
}

// feedback is the common shape of an issue comment, a review, or an inline
// review comment, as far as deciding whether it needs addressing goes.
type feedback struct {
	user *githubv39.User
	body string
	// reviewState is the review's state (e.g. APPROVED); empty for comments.
	reviewState string
	at          time.Time
}

// approvalCommands are Prow-style commands that signal approval rather than
// requesting changes.
var approvalCommands = []string{"/lgtm", "/approve"}

// ignoreFeedback reports whether a piece of feedback should not count as
// outstanding. It is ignored when:
//   - it comes from an ignored user (reviewer bots are never ignored),
//   - it comes from the pull request's own author,
//   - it predates the last commit or the last address-comments task,
//   - its body opts out via the ignore prefix,
//   - its body is empty,
//   - it is an approving review, or
//   - a line of its body starts with an approval command such as /lgtm.
func (s *Scanner) ignoreFeedback(pr *githubv39.PullRequest, f feedback, lastCommitTime, lastCommentAddressedTime time.Time) bool {
	isReviewer := conventions.IsReviewerBot(f.user, s.cfg.ReviewerLogins)
	if !isReviewer && conventions.ShouldIgnoreUser(f.user, s.cfg.GitHubLogin, s.cfg.AllowlistedBots) {
		return true
	}
	// The pull request's own author talking to itself is not feedback.
	if strings.EqualFold(f.user.GetLogin(), pr.GetUser().GetLogin()) {
		return true
	}
	if !f.at.After(lastCommitTime) || !f.at.After(lastCommentAddressedTime) {
		return true
	}
	if conventions.HasIgnorePrefix(f.body, s.cfg.TriggerLabel) {
		return true
	}
	body := strings.TrimSpace(f.body)
	if body == "" {
		return true
	}
	if strings.EqualFold(f.reviewState, "APPROVED") {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, cmd := range approvalCommands {
			if strings.EqualFold(fields[0], cmd) {
				return true
			}
		}
	}
	return false
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
