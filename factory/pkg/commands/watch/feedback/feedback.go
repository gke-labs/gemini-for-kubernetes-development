// Package feedback reads what reviewers said on a pull request and decides
// which of it is still waiting to be addressed.
//
// It is one pull request's answer to "is there feedback to act on?", shared
// by the overseer's watch (package prs), which queues an address-comments
// task, and by `factory pr watch` on a fix recipe's PR, which revises the
// fix. Both read the same three kinds of feedback - conversation comments,
// review bodies and inline review comments - and both record what they
// picked up as reactions on GitHub (conventions.Reaction), so the record
// survives a restart and is visible in the thread. What differs between them
// is the Policy: who the watcher is, and whose words count.
package feedback

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// Lister is the read side of the GitHub client that Fetch needs.
type Lister interface {
	ListCommits(ctx context.Context, number int) ([]*githubv39.RepositoryCommit, error)
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	ListReviews(ctx context.Context, number int) ([]*githubv39.PullRequestReview, error)
	ListAllReviewComments(ctx context.Context, number int) ([]*githubv39.PullRequestComment, error)
}

// History is everything said and done on a pull request, fetched once per
// evaluation.
//
// The conversation is read up front rather than on demand because nearly every
// decision that follows needs some part of it, and each part costs a paginated
// round trip. Fetching it once also means every phase reasons about the same
// snapshot, instead of one phase seeing a comment that another did not.
type History struct {
	// Comments are the pull request's top-level comments.
	Comments []*githubv39.IssueComment
	// Reviews are the submitted reviews.
	Reviews []*githubv39.PullRequestReview
	// ReviewComments holds the inline comments of each review, keyed by review ID.
	ReviewComments map[int64][]*githubv39.PullRequestComment
	// LastCommitTime is the committer date of the most recent commit, which is
	// the line that separates feedback already answered by a push from
	// feedback still outstanding.
	LastCommitTime time.Time
}

// Fetch reads the commits, comments and reviews of a pull request.
//
// A failure to list commits is tolerated - a zero LastCommitTime simply makes
// every comment look new, which errs towards doing the work again rather than
// towards silently skipping it. A failure to list comments, reviews or the
// inline comments of those reviews is not: without them the caller cannot tell
// whether feedback is outstanding, and acting on that blank picture would queue
// the wrong task - or, worse, none at all, since a pull request whose only
// outstanding feedback is inline would look clean and be handed back to a human.
//
// The inline comments are read for the whole pull request in one call and
// grouped by review here, rather than fetched per review. Both spellings
// return the same thing, but the per-review one costs a request each, so its
// price rose with every review a pull request had ever received - which on a
// long-lived change was the single largest term in the cost of evaluating it.
func Fetch(ctx context.Context, gh Lister, num int) (*History, error) {
	h := &History{ReviewComments: make(map[int64][]*githubv39.PullRequestComment)}

	commits, err := gh.ListCommits(ctx, num)
	if err == nil {
		for _, c := range commits {
			if c.GetCommit().GetCommitter().GetDate().After(h.LastCommitTime) {
				h.LastCommitTime = c.GetCommit().GetCommitter().GetDate()
			}
		}
	}

	h.Comments, err = gh.ListIssueComments(ctx, num)
	if err != nil {
		return nil, fmt.Errorf("listing issue comments: %w", err)
	}

	h.Reviews, err = gh.ListReviews(ctx, num)
	if err != nil {
		return nil, fmt.Errorf("listing reviews: %w", err)
	}

	revComments, err := gh.ListAllReviewComments(ctx, num)
	if err != nil {
		return nil, fmt.Errorf("listing review comments: %w", err)
	}
	for _, rc := range revComments {
		// A comment with no review behind it is not addressable as review
		// feedback - every consumer of this map looks a review up by ID - so it
		// is dropped rather than collected under the zero key.
		if id := rc.GetPullRequestReviewID(); id != 0 {
			h.ReviewComments[id] = append(h.ReviewComments[id], rc)
		}
	}

	return h, nil
}

// Kind is which of GitHub's three feedback resources an Item is. They have
// separate ID namespaces, and a reaction or a reply reaches each differently.
type Kind string

const (
	// KindComment is a conversation comment on the pull request.
	KindComment Kind = "comment"
	// KindReview is a review's body. It has no thread to reply in, and its
	// reactions are reached by node ID only.
	KindReview Kind = "review"
	// KindReviewComment is an inline comment of a review.
	KindReviewComment Kind = "review-comment"
)

// Item is one piece of feedback: a conversation comment, a review body or an
// inline review comment.
type Item struct {
	Kind Kind `json:"kind"`
	// ID is the comment's or the review's numeric ID: what a reply's
	// inReplyTo names.
	ID int64 `json:"id"`
	// NodeID is a review's GraphQL node ID, the only way to react to it.
	NodeID string `json:"nodeId,omitempty"`
	Author string `json:"author"`
	// At is when the feedback became visible: an inline comment's review's
	// submission, not its draft.
	At   time.Time `json:"createdAt"`
	Path string    `json:"path,omitempty"`
	Line int       `json:"line,omitempty"`
	URL  string    `json:"url,omitempty"`
	Body string    `json:"body"`

	user        *githubv39.User
	reviewState string
}

// Policy is whose feedback counts, and what the watcher is.
type Policy struct {
	// SelfLogin is the watcher's own account, never feedback. Empty when
	// the watcher posts as the person it works for, whose words do count.
	SelfLogin string
	// AllowlistedBots are the automated accounts whose comments are acted on.
	AllowlistedBots []string
	// ReviewerLogins are the review bots, whose feedback always counts.
	ReviewerLogins []string
	// TriggerLabel names the per-deployment ignore prefix (/<label>-ignore).
	TriggerLabel string
	// CountPRAuthor counts the pull request author's own words. The
	// overseer's bots author their PRs, and a bot talking to itself is not
	// feedback; a member's fix PR is authored by the member, who reviews it.
	CountPRAuthor bool
	// Skip drops feedback whose body it matches, such as the replies a
	// watcher posting as the member wrote itself.
	Skip func(body string) bool
}

// approvalCommands are Prow-style commands that signal approval rather than
// requesting changes.
var approvalCommands = []string{"/lgtm", "/approve"}

// Ignore reports whether a piece of feedback should not count as
// outstanding. It is ignored when:
//   - it comes from an ignored user (reviewer bots are never ignored),
//   - it comes from the pull request's own author, unless CountPRAuthor,
//   - it predates the last commit or the last address-comments task,
//   - its body opts out via the ignore prefix, or Skip matches it,
//   - its body is empty,
//   - it is an approving review, or
//   - a line of its body starts with an approval command such as /lgtm.
func (p Policy) Ignore(pr *githubv39.PullRequest, it Item, lastCommitTime, lastAddressedTime time.Time) bool {
	if !conventions.IsFeedbackAuthor(it.user, p.SelfLogin, p.AllowlistedBots, p.ReviewerLogins) {
		return true
	}
	// The pull request's own author talking to itself is not feedback.
	if !p.CountPRAuthor && strings.EqualFold(it.user.GetLogin(), pr.GetUser().GetLogin()) {
		return true
	}
	if !it.At.After(lastCommitTime) || !it.At.After(lastAddressedTime) {
		return true
	}
	if conventions.HasIgnorePrefix(it.Body, p.TriggerLabel) {
		return true
	}
	if p.Skip != nil && p.Skip(it.Body) {
		return true
	}
	body := strings.TrimSpace(it.Body)
	if body == "" {
		return true
	}
	if strings.EqualFold(it.reviewState, "APPROVED") {
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

// Pending is the feedback on a pull request still waiting on the watcher, in
// the order GitHub lists it: the conversation, then each review's body
// followed by its inline comments.
//
// A piece counts only if the policy does not ignore it - in particular, only
// if it post-dates both lastCommitTime and lastAddressedTime (zero for no such
// gate) - and its reactions say it still needs attention. What the emoji on a
// comment mean, and which of them outrank the others, is
// conventions.CommentState's business. The reactions are read last, so that
// only feedback that would otherwise count costs a request.
func Pending(ctx context.Context, pr *githubv39.PullRequest, h *History, p Policy, reactions *conventions.ReactionInterpreter, lastCommitTime, lastAddressedTime time.Time) []Item {
	var out []Item
	for _, c := range h.Comments {
		it := Item{
			Kind: KindComment, ID: c.GetID(), Author: c.GetUser().GetLogin(), At: c.GetCreatedAt(),
			URL: c.GetHTMLURL(), Body: c.GetBody(), user: c.GetUser(),
		}
		if p.Ignore(pr, it, lastCommitTime, lastAddressedTime) {
			continue
		}
		if !reactions.CommentState(ctx, c.GetID()).NeedsAttention() {
			continue
		}
		out = append(out, it)
	}

	for _, r := range h.Reviews {
		// The review body and its inline comments are judged independently:
		// an empty or approving review body can still carry inline feedback.
		it := Item{
			Kind: KindReview, ID: r.GetID(), NodeID: r.GetNodeID(), Author: r.GetUser().GetLogin(), At: r.GetSubmittedAt(),
			URL: r.GetHTMLURL(), Body: r.GetBody(), user: r.GetUser(), reviewState: r.GetState(),
		}
		if !p.Ignore(pr, it, lastCommitTime, lastAddressedTime) && reactions.ReviewState(ctx, r.GetNodeID()).NeedsAttention() {
			out = append(out, it)
		}

		for _, rc := range h.ReviewComments[r.GetID()] {
			// An inline comment only becomes visible when its review is
			// submitted, so it is timed from then rather than from its draft.
			it := Item{
				Kind: KindReviewComment, ID: rc.GetID(), Author: rc.GetUser().GetLogin(),
				At:   conventions.ReviewCommentTime(rc, r.GetSubmittedAt()),
				Path: rc.GetPath(), Line: rc.GetLine(), URL: rc.GetHTMLURL(), Body: rc.GetBody(), user: rc.GetUser(),
			}
			if p.Ignore(pr, it, lastCommitTime, lastAddressedTime) {
				continue
			}
			if !reactions.ReviewCommentState(ctx, rc.GetID()).NeedsAttention() {
				continue
			}
			out = append(out, it)
		}
	}
	return out
}

// Reactor is the write side of the GitHub client that React needs.
type Reactor interface {
	AddIssueCommentReaction(ctx context.Context, commentID int64, content string) error
	AddPullRequestCommentReaction(ctx context.Context, commentID int64, content string) error
	AddReviewReaction(ctx context.Context, reviewNodeID string, content string) error
}

// React records a reaction on each item, each where its kind is reached: a
// review by its node ID. It tries every item and returns the first failure.
func React(ctx context.Context, r Reactor, items []Item, content conventions.Reaction) error {
	var first error
	for _, it := range items {
		var err error
		switch it.Kind {
		case KindComment:
			err = r.AddIssueCommentReaction(ctx, it.ID, string(content))
		case KindReviewComment:
			err = r.AddPullRequestCommentReaction(ctx, it.ID, string(content))
		case KindReview:
			if it.NodeID == "" {
				continue // cannot carry a reaction (conventions.ReactionInterpreter.ReviewState)
			}
			err = r.AddReviewReaction(ctx, it.NodeID, string(content))
		default:
			err = fmt.Errorf("unknown feedback kind %q", it.Kind)
		}
		if err != nil && first == nil {
			first = fmt.Errorf("reacting %q to %s %d: %w", content, it.Kind, it.ID, err)
		}
	}
	return first
}
