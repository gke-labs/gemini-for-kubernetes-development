package prs

import (
	"context"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// prHistory is everything said and done on a pull request, fetched once per
// evaluation (feedback.History, under the names the scanner uses).
type prHistory struct {
	// comments are the pull request's top-level comments.
	comments []*githubv39.IssueComment
	// reviews are the submitted reviews.
	reviews []*githubv39.PullRequestReview
	// revCommentsMap holds the inline comments of each review, keyed by review ID.
	revCommentsMap map[int64][]*githubv39.PullRequestComment
	// lastCommitTime is the committer date of the most recent commit, which is
	// the line that separates feedback already answered by a push from
	// feedback still outstanding.
	lastCommitTime time.Time
}

// fetchHistory reads the commits, comments and reviews of a pull request
// (feedback.Fetch).
func (s *Scanner) fetchHistory(ctx context.Context, num int) (*prHistory, error) {
	h, err := feedback.Fetch(ctx, s.gh, num)
	if err != nil {
		return nil, err
	}
	return &prHistory{
		comments:       h.Comments,
		reviews:        h.Reviews,
		revCommentsMap: h.ReviewComments,
		lastCommitTime: h.LastCommitTime,
	}, nil
}

// historyFromDetails is the history fetchHistory reads, from what the GraphQL
// details query read instead: the same comments, reviews and inline comments,
// the inline comments grouped by review the same way.
func historyFromDetails(d *github.PRDetails) *prHistory {
	h := &prHistory{
		comments:       d.Comments,
		reviews:        d.Reviews,
		revCommentsMap: make(map[int64][]*githubv39.PullRequestComment),
		lastCommitTime: d.LastCommitTime,
	}
	for _, rc := range d.ReviewComments {
		if id := rc.GetPullRequestReviewID(); id != 0 {
			h.revCommentsMap[id] = append(h.revCommentsMap[id], rc)
		}
	}
	return h
}

// getLastPRActivityTime returns when a human last engaged with the pull
// request, falling back to when it was opened.
//
// Only trusted human activity counts. The watcher's own comments, allowlisted
// bots and anything opted out with the ignore prefix are all skipped, because
// otherwise the daemon's own chatter would keep a dead pull request looking
// alive forever. Untrusted authors (see conventions.IsTrustedAuthor) are
// skipped too.
func getLastPRActivityTime(pr *githubv39.PullRequest, comments []*githubv39.IssueComment, reviews []*githubv39.PullRequestReview, revComments map[int64][]*githubv39.PullRequestComment, githubLogin string, bots, trustedLogins []string, triggerLabel string) time.Time {
	lastActivity := pr.GetCreatedAt()

	// 1. Check issue comments
	for _, c := range comments {
		isBot := conventions.IsBotReply(c.GetUser(), githubLogin, bots)
		if !isBot && conventions.IsTrustedAuthor(c.GetUser(), c.GetAuthorAssociation(), trustedLogins) {
			if conventions.HasIgnorePrefix(c.GetBody(), triggerLabel) {
				continue
			}
			if c.GetCreatedAt().After(lastActivity) {
				lastActivity = c.GetCreatedAt()
			}
		}
	}

	// 2. Check reviews and review comments
	for _, r := range reviews {
		if !conventions.IsBotReply(r.GetUser(), githubLogin, bots) && conventions.IsTrustedAuthor(r.GetUser(), r.GetAuthorAssociation(), trustedLogins) {
			if conventions.HasIgnorePrefix(r.GetBody(), triggerLabel) {
				continue
			}
			if r.GetSubmittedAt().After(lastActivity) {
				lastActivity = r.GetSubmittedAt()
			}
		}

		if rcList, ok := revComments[r.GetID()]; ok {
			for _, rc := range rcList {
				if !conventions.IsBotReply(rc.GetUser(), githubLogin, bots) && conventions.IsTrustedAuthor(rc.GetUser(), rc.GetAuthorAssociation(), trustedLogins) {
					if conventions.HasIgnorePrefix(rc.GetBody(), triggerLabel) {
						continue
					}
					if at := conventions.ReviewCommentTime(rc, r.GetSubmittedAt()); at.After(lastActivity) {
						lastActivity = at
					}
				}
			}
		}
	}

	return lastActivity
}
