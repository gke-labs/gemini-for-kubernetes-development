package prs

import (
	"fmt"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// commentRetryCooldown is how long after a failed address-feedback task the
// same feedback becomes worth another attempt.
//
// The fast pass runs every minute, so without a delay the whole budget would be
// spent within a few minutes of the first failure - against the same revision,
// the same sandbox image and, usually, the same transient cause. Ten minutes is
// long enough for a flaky sandbox or a GitHub blip to clear, and short enough
// that a reviewer waiting on the fix does not notice the gap.
const commentRetryCooldown = 10 * time.Minute

// commentRetry is what the last attempt at a pull request's review feedback
// left behind: how many consecutive times it has failed against the current
// head, and whether the watcher still owes it another try.
//
// A retry is not a new decision about the feedback but a repetition of one
// already made, which is why it is derived once per evaluation and threaded
// through the phase rather than re-derived where it is needed.
type commentRetry struct {
	// attempts is the number of consecutive failures against the current head.
	attempts int
	// active reports that the last attempt failed and the budget is not yet
	// spent, so the feedback that attempt was queued for is still outstanding
	// however the comments themselves are marked.
	active bool
	// dueAt is when an active retry may be queued. Zero when none is owed.
	dueAt time.Time
}

// attempt returns the 1-based number to record on the task about to be queued.
//
// Anything that is not a retry is a first attempt, whatever came before it: the
// count only exists to bound a run of consecutive failures, and a pull request
// whose feedback is being picked up again - by a new commit, a new comment or a
// human's redo - is starting a new run, not continuing the old one.
func (r commentRetry) attempt() int {
	if !r.active {
		return 1
	}
	return r.attempts + 1
}

// commentRetryState reads what the pull request's last address-feedback task
// left behind, and decides whether another attempt is owed.
//
// The count comes from the queue's record of that task rather than from the
// scanner's own memory because the record is what survives a restart. A
// scanner-held counter would reset on every restart, which is the one moment a
// pull request stuck in a failing loop is most likely to be handed a fresh
// budget it has not earned.
//
// Three things end a sequence of retries, all of them changes to the situation
// the attempts were failing against: a successful attempt, a new commit, and a
// human saying something since the last failure. Everything else - the failure
// reactions, the addressed-at timestamps - is bookkeeping left by the attempt
// being retried, and cannot be allowed to speak for the retry.
func (s *Scanner) commentRetryState(num int, headSHA string, pr *githubv39.PullRequest, history *prHistory) commentRetry {
	last := s.queue.GetProcessedTask(commentTaskFilename(num))
	if last == nil || last.Status != api.StatusFailed {
		return commentRetry{}
	}

	// A task recorded before attempts were counted carries no number. Reading
	// it as a first failure would hand every pull request parked before the
	// upgrade a retry budget at once, so it keeps the behaviour it was parked
	// under and waits for a human.
	if last.Attempt == 0 {
		return commentRetry{}
	}

	// A failure against a different revision says nothing about this one: the
	// push that produced the current head is itself an answer to the feedback
	// that attempt was working from.
	if last.CommitSHA != headSHA {
		return commentRetry{}
	}

	lastHumanActivity := getLastPRActivityTime(pr, history.comments, history.reviews, history.revCommentsMap, s.cfg.GitHubLogin, s.cfg.AllowlistedBots, s.cfg.TriggerLabel)
	if lastHumanActivity.After(last.CompletedAt) {
		return commentRetry{}
	}

	if last.Attempt >= api.MaxPRCommentAttempts {
		// The budget is spent, so no retry is owed - but this is deliberately
		// not a veto on ever queueing the feedback again. What parks it is the
		// 'confused' mark the final failure left on the comments and the
		// addressed-at stamp, both of which a human can get past with a redo
		// reaction. A hard block here would outrank them and make the give-up
		// permanent for this revision, which is the one thing the redo exists
		// to prevent.
		klog.V(2).Infof("PR #%d has spent its address-comments retry budget (%d attempts against %s).", num, last.Attempt, headSHA)
		return commentRetry{}
	}

	return commentRetry{
		attempts: last.Attempt,
		active:   true,
		dueAt:    last.CompletedAt.Add(commentRetryCooldown),
	}
}

// commentTaskFilename returns the queue file name of a pull request's
// address-feedback task. The name is the queue's identity for that work, so the
// gating, the retry count and the enqueue all have to agree on it.
func commentTaskFilename(num int) string {
	return fmt.Sprintf("task-pr-%d-comments.yaml", num)
}
