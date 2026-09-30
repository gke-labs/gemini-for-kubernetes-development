package conventions

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// HasIgnorePrefix reports whether a comment opts itself out of being acted on.
//
// The prefix is "/" + triggerLabel + "-ignore" on any line of the body. The
// '/overseer-ignore' spelling is always accepted, so the directive keeps
// working across deployments that renamed their trigger label.
func HasIgnorePrefix(body string, triggerLabel string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(trimmed, "/"+defaultPrefix+"-ignore") {
			return true
		}
		if triggerLabel != "" && !strings.EqualFold(triggerLabel, defaultPrefix) {
			prefix := "/" + strings.ToLower(triggerLabel) + "-ignore"
			if strings.HasPrefix(trimmed, prefix) {
				return true
			}
		}
	}
	return false
}

// CommentResolverClient is the GitHub access ResolveCommentReactions needs: for
// each of the three places feedback can be left on a pull request -
// conversation comments, review bodies, and inline review comments - a way to
// list them, read the reactions already on them (via ReactionLister), and add
// one more.
//
// They are three separate sets of methods because GitHub models them as three
// separate resources with their own ID namespaces (and, for review bodies, a
// different API altogether).
type CommentResolverClient interface {
	ReactionLister
	// ListIssueComments returns every comment on an issue or pull request.
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	// AddIssueCommentReaction records a reaction on a conversation comment.
	AddIssueCommentReaction(ctx context.Context, commentID int64, content string) error

	// ListReviews returns every review submitted on a pull request.
	ListReviews(ctx context.Context, number int) ([]*githubv39.PullRequestReview, error)
	// AddReviewReaction records a reaction on a review's body, addressed by
	// the review's node ID.
	AddReviewReaction(ctx context.Context, reviewNodeID string, content string) error

	// ListAllReviewComments returns every inline review comment on a pull request.
	ListAllReviewComments(ctx context.Context, number int) ([]*githubv39.PullRequestComment, error)
	// AddPullRequestCommentReaction records a reaction on an inline review comment.
	AddPullRequestCommentReaction(ctx context.Context, commentID int64, content string) error
}

// ResolveOptions describes the task whose outcome ResolveCommentReactions
// records, and whose feedback counts.
type ResolveOptions struct {
	// PRNumber is the pull request the task worked on.
	PRNumber int
	// Resolution is the outcome reaction to record.
	Resolution Reaction
	// SelfLogin is the watcher's own account.
	SelfLogin string
	// AllowlistedBots are the automated accounts whose feedback is acted on.
	AllowlistedBots []string
	// ReviewerLogins are the review bots, whose feedback is acted on even
	// though they are not allowlisted.
	ReviewerLogins []string
	// Since skips feedback created before it. The scanner only picks up
	// feedback at or after the oldest outstanding item, which it records as
	// the task's trigger event time, so anything earlier was never this task's
	// and need not cost a request to rule out. Zero considers everything.
	Since time.Time
}

// ResolveCommentReactions closes out the acknowledgements on a pull request:
// every conversation comment, review, and inline review comment the watcher
// marked as picked up gets the outcome reaction once the task has finished.
//
// It is called from the task lifecycle rather than from a scan, which is why it
// lives here rather than with the scanner that applied the acknowledgement.
//
// Only feedback awaiting an outcome is touched (see
// CommentState.AwaitingOutcome). A comment that already carries an outcome
// belongs to an earlier task, and one the watcher never marked was never this
// task's to answer.
//
// The three kinds of feedback are resolved independently: failing to list one
// of them is logged and does not stop the others from being resolved.
func ResolveCommentReactions(ctx context.Context, client CommentResolverClient, opts ResolveOptions) {
	interpreter := NewReactionInterpreter(client, opts.SelfLogin, opts.AllowlistedBots)
	content := string(opts.Resolution)
	prNum := opts.PRNumber
	// candidate reports whether a piece of feedback could have been picked up
	// by the task: the author is one the scanner listens to, and it is not
	// older than the task's trigger.
	candidate := func(u *githubv39.User, at time.Time) bool {
		if !opts.Since.IsZero() && at.Before(opts.Since) {
			return false
		}
		return IsFeedbackAuthor(u, opts.SelfLogin, opts.AllowlistedBots, opts.ReviewerLogins)
	}

	if comments, err := client.ListIssueComments(ctx, prNum); err != nil {
		klog.Warningf("Failed to list comments on PR #%d to resolve reactions: %v", prNum, err)
	} else {
		for _, c := range comments {
			if !candidate(c.GetUser(), c.GetCreatedAt()) {
				continue
			}
			id := c.GetID()
			resolveIfAwaiting(interpreter, fmt.Sprintf("comment %d", id),
				func() ([]*githubv39.Reaction, error) { return client.IssueCommentReactions(ctx, id) },
				func() error { return client.AddIssueCommentReaction(ctx, id, content) },
			)
		}
	}

	// reviewSubmittedAt times each inline comment by the review that carried
	// it, as the scanner does (see ReviewCommentTime). If the reviews cannot
	// be listed it stays empty and inline comments fall back to their own
	// creation time.
	reviewSubmittedAt := make(map[int64]time.Time)
	if reviews, err := client.ListReviews(ctx, prNum); err != nil {
		klog.Warningf("Failed to list reviews on PR #%d to resolve reactions: %v", prNum, err)
	} else {
		for _, r := range reviews {
			reviewSubmittedAt[r.GetID()] = r.GetSubmittedAt()
			// Reviews are only reachable for reactions by node ID; a review
			// without one cannot have been acknowledged in the first place.
			nodeID := r.GetNodeID()
			if nodeID == "" || !candidate(r.GetUser(), r.GetSubmittedAt()) {
				continue
			}
			resolveIfAwaiting(interpreter, fmt.Sprintf("review %d", r.GetID()),
				func() ([]*githubv39.Reaction, error) { return client.ReviewReactions(ctx, nodeID) },
				func() error { return client.AddReviewReaction(ctx, nodeID, content) },
			)
		}
	}

	if revComments, err := client.ListAllReviewComments(ctx, prNum); err != nil {
		klog.Warningf("Failed to list review comments on PR #%d to resolve reactions: %v", prNum, err)
	} else {
		for _, rc := range revComments {
			if !candidate(rc.GetUser(), ReviewCommentTime(rc, reviewSubmittedAt[rc.GetPullRequestReviewID()])) {
				continue
			}
			id := rc.GetID()
			resolveIfAwaiting(interpreter, fmt.Sprintf("review comment %d", id),
				func() ([]*githubv39.Reaction, error) { return client.PullRequestCommentReactions(ctx, id) },
				func() error { return client.AddPullRequestCommentReaction(ctx, id, content) },
			)
		}
	}
}

// ReviewCommentTime is when an inline review comment became feedback: the
// later of when it was written and when the review carrying it was submitted.
//
// An inline comment left as part of a review is created when it is drafted,
// but nobody else can see it until the review is submitted, which can be much
// later. Timed by its draft, it would fall before a commit or task that
// happened while the reviewer was still writing, and be dropped as already
// answered even though nothing could have answered it. Taking the later of the
// two times means this can only ever make a comment newer, never older.
//
// The scanner deciding what to pick up and the resolver deciding what to stamp
// must time inline comments identically, and consistently with the review they
// belong to. The resolver's Since cut-off can be a review's submission time;
// timed by their drafts, that review's own inline comments fell before it and
// never received an outcome.
//
// reviewSubmittedAt may be zero when the review is not known, in which case
// the comment's own creation time is used.
func ReviewCommentTime(rc *githubv39.PullRequestComment, reviewSubmittedAt time.Time) time.Time {
	created := rc.GetCreatedAt()
	if reviewSubmittedAt.After(created) {
		return reviewSubmittedAt
	}
	return created
}

// resolveIfAwaiting adds the outcome reaction to a single piece of feedback
// if it is awaiting one.
//
// A failed read leaves the feedback alone. The outcome reaction is a signal
// rather than the work itself, and stamping one on feedback the watcher cannot
// show it picked up would misreport what this task addressed.
func resolveIfAwaiting(interpreter *ReactionInterpreter, desc string, list func() ([]*githubv39.Reaction, error), add func() error) {
	reactions, err := list()
	if err != nil {
		klog.Warningf("Failed to read reactions on %s: %v", desc, err)
		return
	}
	if !interpreter.Interpret(reactions).AwaitingOutcome() {
		return
	}
	if err := add(); err != nil {
		klog.Warningf("Failed to resolve reaction on %s: %v", desc, err)
	}
}
