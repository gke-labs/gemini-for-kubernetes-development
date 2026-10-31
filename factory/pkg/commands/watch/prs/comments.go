package prs

import (
	"context"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

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
		TrustedLogins:   s.cfg.TrustedLogins,
		TriggerLabel:    s.cfg.TriggerLabel,
	}
}
