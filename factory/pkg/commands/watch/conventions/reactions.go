package conventions

import (
	"context"

	githubv39 "github.com/google/go-github/v39/github"
)

// Reaction is a GitHub reaction content string.
//
// Reactions are the watcher's memory. Nothing else survives a restart or is
// visible to the people in the thread, so the emoji on a comment is both the
// record of what the watcher did with it and the signal back to its author.
// Naming them here keeps that vocabulary in one place instead of as bare
// strings spread across the scanner and the task lifecycle.
type Reaction string

const (
	// ReactionAcknowledged ('eyes') is applied when the watcher picks a comment
	// up. It is what stops the next cycle queueing the same feedback again
	// while the first task is still running.
	ReactionAcknowledged Reaction = "eyes"
	// ReactionResolved ('+1') replaces the acknowledgement once the task that
	// addressed the comment succeeded.
	ReactionResolved Reaction = "+1"
	// ReactionFailed ('confused') replaces the acknowledgement when the task
	// failed. The comment is still treated as handled - retrying it
	// automatically would loop on whatever made it fail - so clearing it is a
	// human's call, made with ReactionRedo.
	ReactionFailed Reaction = "confused"
	// ReactionRedo ('rocket') is the human override: it asks the watcher to
	// look at a comment again even though it has already marked it.
	ReactionRedo Reaction = "rocket"
)

// CommentState is the watcher's reading of the reactions on a single comment:
// what the watcher itself recorded there, and what a human has asked for since.
//
// Authorship is half the meaning of a reaction, so it is resolved here rather
// than left to the caller. The watcher's own marks say "already handled"; the
// same emoji from a human would mean nothing of the sort. Only marks whose
// author matches the expected side are reported - a human's 'eyes' is not an
// acknowledgement, and the watcher's own 'rocket' is not a request to redo.
type CommentState struct {
	// Acknowledged is set when the watcher has marked the comment as picked up
	// but has not yet recorded an outcome for it.
	Acknowledged bool
	// Resolved is set when the watcher recorded a successful outcome.
	Resolved bool
	// Failed is set when the watcher recorded a failed outcome.
	Failed bool
	// RedoRequested is set when a human asked for another pass.
	RedoRequested bool
}

// NeedsAttention reports whether the comment is still waiting on the watcher.
//
// A comment nobody has marked needs attention; one the watcher has marked does
// not. The human override lifts the watcher's own marks, with one deliberate
// exception: a resolved comment stays resolved. 'rocket' is how a human reopens
// work the watcher acknowledged or gave up on, whereas re-running against
// feedback that was successfully addressed would act on a comment whose fix is
// already in the branch.
//
// Note that this only interprets the reactions. Whether the comment is recent
// enough, and whether its author is someone to listen to, are separate gates
// applied by the scanner before it gets here.
func (s CommentState) NeedsAttention() bool {
	if s.Resolved {
		return false
	}
	if s.RedoRequested {
		return true
	}
	return !s.Acknowledged && !s.Failed
}

// ReactionLister is the read side of the GitHub client that the interpreter
// needs. Fetching is the client's job and interpretation is this package's, and
// the seam between them is what lets the rules above be exercised without a
// GitHub server.
type ReactionLister interface {
	// IssueCommentReactions returns the reactions recorded on a comment.
	IssueCommentReactions(ctx context.Context, commentID int64) ([]*githubv39.Reaction, error)
}

// ReactionInterpreter turns the reactions on a comment into a CommentState.
//
// It exists as a value rather than a package function because the reading
// depends on who the watcher is and which bots it trusts, and those do not
// change between comments. Binding them once means a caller cannot get the
// attribution wrong on one call out of four.
type ReactionInterpreter struct {
	lister    ReactionLister
	selfLogin string
	bots      []string
}

// NewReactionInterpreter binds an interpreter to the watcher's own account and
// the bots whose reactions count as the watcher's own side.
func NewReactionInterpreter(lister ReactionLister, selfLogin string, bots []string) *ReactionInterpreter {
	return &ReactionInterpreter{lister: lister, selfLogin: selfLogin, bots: bots}
}

// CommentState fetches a comment's reactions and interprets them.
//
// One request covers every reaction on the comment, which is the point of
// returning the whole state instead of answering one emoji at a time: the
// scanner asks four questions of each comment and a busy pull request has
// dozens of them.
//
// A failed fetch reads as an unmarked comment. That errs towards the watcher
// doing the work again rather than silently dropping feedback, and a repeated
// task is recoverable in a way that ignored review comments are not.
func (i *ReactionInterpreter) CommentState(ctx context.Context, commentID int64) CommentState {
	if i == nil || i.lister == nil {
		return CommentState{}
	}
	reactions, err := i.lister.IssueCommentReactions(ctx, commentID)
	if err != nil {
		return CommentState{}
	}
	return i.Interpret(reactions)
}

// Interpret reads an already-fetched set of reactions. It is the whole of the
// attribution policy, kept separate from the fetch so it can be tested as the
// pure function it is.
func (i *ReactionInterpreter) Interpret(reactions []*githubv39.Reaction) CommentState {
	var state CommentState
	for _, r := range reactions {
		// The watcher's own account, plus the automated accounts whose comments
		// it ignores, count as its own side. An allowlisted bot is deliberately
		// not one of them: its comments are feedback the watcher acts on, so it
		// stands on the commenter's side of this conversation and its 'rocket'
		// asks for another pass just as a human's would.
		byWatcher := ShouldIgnoreUser(r.GetUser(), i.selfLogin, i.bots)
		switch Reaction(r.GetContent()) {
		case ReactionAcknowledged:
			state.Acknowledged = state.Acknowledged || byWatcher
		case ReactionResolved:
			state.Resolved = state.Resolved || byWatcher
		case ReactionFailed:
			state.Failed = state.Failed || byWatcher
		case ReactionRedo:
			state.RedoRequested = state.RedoRequested || !byWatcher
		}
	}
	return state
}
