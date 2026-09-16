package prs

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// prHistory is everything said and done on a pull request, fetched once per
// evaluation.
//
// The conversation is read up front rather than on demand because nearly every
// decision that follows needs some part of it, and each part costs a paginated
// round trip. Fetching it once also means every phase reasons about the same
// snapshot, instead of one phase seeing a comment that another did not.
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

// fetchHistory reads the commits, comments and reviews of a pull request.
//
// A failure to list commits is tolerated - a zero lastCommitTime simply makes
// every comment look new, which errs towards doing the work again rather than
// towards silently skipping it. A failure to list comments or reviews is not:
// without them the scanner cannot tell whether feedback is outstanding, and
// acting on that blank picture would queue the wrong task.
func (s *Scanner) fetchHistory(ctx context.Context, num int) (*prHistory, error) {
	h := &prHistory{revCommentsMap: make(map[int64][]*githubv39.PullRequestComment)}

	commits, err := s.gh.ListCommits(ctx, num)
	if err == nil {
		for _, c := range commits {
			if c.GetCommit().GetCommitter().GetDate().After(h.lastCommitTime) {
				h.lastCommitTime = c.GetCommit().GetCommitter().GetDate()
			}
		}
	}

	h.comments, err = s.gh.ListIssueComments(ctx, num)
	if err != nil {
		return nil, fmt.Errorf("listing issue comments: %w", err)
	}

	h.reviews, err = s.gh.ListReviews(ctx, num)
	if err != nil {
		return nil, fmt.Errorf("listing reviews: %w", err)
	}

	for _, r := range h.reviews {
		if rc, err := s.gh.ListReviewComments(ctx, num, r.GetID()); err == nil {
			h.revCommentsMap[r.GetID()] = rc
		}
	}

	return h, nil
}

// pauseIfInactive stops the watcher working a pull request that no human has
// engaged with for the configured timeout, and reports whether it did.
//
// A pull request nobody has looked at is usually one that has been abandoned or
// superseded, and continuing to rebase and re-investigate it burns agent time
// on a change that will never merge. The stop label is applied rather than the
// pull request being silently dropped, so that the pause is visible and a human
// can undo it by removing the label.
func (s *Scanner) pauseIfInactive(ctx context.Context, pr *githubv39.PullRequest, prIssue *githubv39.Issue, history *prHistory, headSHA string) bool {
	if s.cfg.InactivityTimeout <= 0 {
		return false
	}
	num := prIssue.GetNumber()
	lastActivity := getLastPRActivityTime(pr, history.comments, history.reviews, history.revCommentsMap, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TriggerLabel)
	if time.Since(lastActivity) <= s.cfg.InactivityTimeout {
		return false
	}

	stopLabel := conventions.StopLabel(s.cfg.TriggerLabel)
	s.reconcileReadyForHumanLabel(ctx, num, prIssue, false, headSHA)
	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would pause automated processing on PR #%d and apply label '%s' due to inactivity since %v\n", num, stopLabel, lastActivity)
		return true
	}

	klog.Infof("Pausing automated processing on PR #%d and applying label '%s' due to inactivity since %v", num, stopLabel, lastActivity)
	// The explanatory comment is only posted once: it is itself bot activity,
	// so re-posting it every cycle would spam a thread nobody is reading.
	if !hasInactivityComment(history.comments, lastActivity) {
		s.comment(ctx, num, fmt.Sprintf("🤖 AI Factory has paused automated processing on this pull request due to a period of inactivity with no human comments (inactive for %s). I have applied the `%s` label.\n\nTo resume automated processing, please remove the `%s` label from this pull request and add a new comment/review.", s.cfg.InactivityTimeout, stopLabel, stopLabel))
	}
	if err := s.gh.AddLabels(ctx, num, []string{stopLabel}); err != nil {
		klog.Errorf("Failed to add stop label '%s' to PR #%d: %v", stopLabel, num, err)
	}
	_ = s.queue.RemovePendingTasksForNumber(num)
	return true
}

// getLastPRActivityTime returns when a human last engaged with the pull
// request, falling back to when it was opened.
//
// Only human activity counts. The watcher's own comments, allowlisted bots and
// anything opted out with the ignore prefix are all skipped, because otherwise
// the daemon's own chatter would keep a dead pull request looking alive
// forever.
func getLastPRActivityTime(pr *githubv39.PullRequest, comments []*githubv39.IssueComment, reviews []*githubv39.PullRequestReview, revComments map[int64][]*githubv39.PullRequestComment, githubLogin string, bots []string, triggerLabel string) time.Time {
	lastActivity := pr.GetCreatedAt()

	// 1. Check issue comments
	for _, c := range comments {
		isBot := conventions.IsBotReply(c.GetUser(), githubLogin, bots)
		if !isBot {
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
		if !conventions.IsBotReply(r.GetUser(), githubLogin, bots) {
			if conventions.HasIgnorePrefix(r.GetBody(), triggerLabel) {
				continue
			}
			if r.GetSubmittedAt().After(lastActivity) {
				lastActivity = r.GetSubmittedAt()
			}
		}

		if rcList, ok := revComments[r.GetID()]; ok {
			for _, rc := range rcList {
				if !conventions.IsBotReply(rc.GetUser(), githubLogin, bots) {
					if conventions.HasIgnorePrefix(rc.GetBody(), triggerLabel) {
						continue
					}
					if rc.GetCreatedAt().After(lastActivity) {
						lastActivity = rc.GetCreatedAt()
					}
				}
			}
		}
	}

	return lastActivity
}

// hasInactivityComment reports whether the inactivity notice has already been
// posted since the last activity.
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
