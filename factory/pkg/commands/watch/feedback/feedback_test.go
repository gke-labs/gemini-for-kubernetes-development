package feedback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func tp(t time.Time) *time.Time { return &t }

func user(login string) *githubv39.User { return &githubv39.User{Login: githubv39.String(login)} }

func comment(id int64, login string, m int, body string) *githubv39.IssueComment {
	return &githubv39.IssueComment{ID: githubv39.Int64(id), User: user(login), CreatedAt: tp(at(m)), Body: githubv39.String(body)}
}

func review(id int64, node, login string, m int, state, body string) *githubv39.PullRequestReview {
	return &githubv39.PullRequestReview{ID: githubv39.Int64(id), NodeID: githubv39.String(node), User: user(login), SubmittedAt: tp(at(m)), State: githubv39.String(state), Body: githubv39.String(body)}
}

func inline(id, reviewID int64, login string, m int, body string) *githubv39.PullRequestComment {
	return &githubv39.PullRequestComment{ID: githubv39.Int64(id), PullRequestReviewID: githubv39.Int64(reviewID), User: user(login), CreatedAt: tp(at(m)), Path: githubv39.String("a.go"), Line: githubv39.Int(3), Body: githubv39.String(body)}
}

type fakeReactions map[string][]*githubv39.Reaction

func (f fakeReactions) IssueCommentReactions(_ context.Context, id int64) ([]*githubv39.Reaction, error) {
	return f[key("comment", id)], nil
}

func (f fakeReactions) PullRequestCommentReactions(_ context.Context, id int64) ([]*githubv39.Reaction, error) {
	return f[key("review-comment", id)], nil
}

func (f fakeReactions) ReviewReactions(_ context.Context, node string) ([]*githubv39.Reaction, error) {
	return f["review "+node], nil
}

func key(kind string, id int64) string { return kind + " " + githubv39.Stringify(id) }

func react(content conventions.Reaction, login string) *githubv39.Reaction {
	return &githubv39.Reaction{Content: githubv39.String(string(content)), User: user(login)}
}

func ids(items []Item) string {
	var s []string
	for _, it := range items {
		s = append(s, string(it.Kind)+" "+githubv39.Stringify(it.ID))
	}
	return strings.Join(s, ", ")
}

// Pending lists the conversation, then each review's body and its inline
// comments, and drops what the policy ignores: the watcher's own words,
// approvals, empty bodies, and what predates the last commit.
func TestPending(t *testing.T) {
	pr := &githubv39.PullRequest{User: user("author")}
	h := &History{
		Comments: []*githubv39.IssueComment{
			comment(1, "alice", 10, "please rename foo"),
			comment(2, "watcher", 11, "on it"),
			comment(3, "alice", 12, "/lgtm"),
			comment(4, "alice", 1, "before the commit"),
			comment(5, "author", 13, "a note to myself"),
			comment(6, "alice", 14, "  "),
		},
		Reviews: []*githubv39.PullRequestReview{
			review(10, "PRR_10", "bob", 20, "CHANGES_REQUESTED", "see inline"),
			review(11, "PRR_11", "bob", 21, "APPROVED", "ship it"),
		},
		ReviewComments: map[int64][]*githubv39.PullRequestComment{
			// Drafted before the commit, but submitted after it: it counts.
			10: {inline(100, 10, "bob", 1, "nit: a name")},
			// An approval's body is ignored, its inline comments are not.
			11: {inline(110, 11, "bob", 21, "one more thing")},
		},
	}
	p := Policy{SelfLogin: "watcher"}
	got := Pending(context.Background(), pr, h, p, nil, at(5), time.Time{})
	if want := "comment 1, review 10, review-comment 100, review-comment 110"; ids(got) != want {
		t.Errorf("pending = %s, want %s", ids(got), want)
	}
	if it := got[2]; it.Path != "a.go" || it.Line != 3 || !it.At.Equal(at(20)) || it.Author != "bob" {
		t.Errorf("inline item = %+v", it)
	}
	if got[1].NodeID != "PRR_10" {
		t.Errorf("review item has node %q", got[1].NodeID)
	}

	// The PR author counts when the policy says so, and Skip drops bodies.
	p = Policy{CountPRAuthor: true, Skip: func(b string) bool { return strings.Contains(b, "rename") }}
	got = Pending(context.Background(), pr, h, p, nil, time.Time{}, time.Time{})
	if want := "comment 2, comment 4, comment 5, review 10, review-comment 100, review-comment 110"; ids(got) != want {
		t.Errorf("fix-style pending = %s, want %s", ids(got), want)
	}

	// After the last addressed time, only later feedback counts.
	got = Pending(context.Background(), pr, h, Policy{SelfLogin: "watcher"}, nil, time.Time{}, at(15))
	if want := "review 10, review-comment 100, review-comment 110"; ids(got) != want {
		t.Errorf("pending after addressed = %s, want %s", ids(got), want)
	}
}

// The watcher's 👀 or 👍 marks feedback handled; somebody else's does not,
// and a 🚀 that is not the watcher's lifts a 👀 (a 👍 is final).
func TestPendingReadsReactions(t *testing.T) {
	pr := &githubv39.PullRequest{User: user("author")}
	h := &History{
		Comments: []*githubv39.IssueComment{
			comment(1, "alice", 10, "acked"),
			comment(2, "alice", 11, "resolved"),
			comment(3, "alice", 12, "someone else's eyes"),
			comment(4, "alice", 13, "redo"),
			comment(5, "alice", 14, "the watcher's own rocket"),
		},
		Reviews: []*githubv39.PullRequestReview{review(10, "PRR_10", "bob", 20, "COMMENTED", "acked review")},
		ReviewComments: map[int64][]*githubv39.PullRequestComment{
			10: {inline(100, 10, "bob", 20, "acked inline"), inline(101, 10, "bob", 20, "new inline")},
		},
	}
	reactions := fakeReactions{
		"comment 1":          {react(conventions.ReactionAcknowledged, "watcher")},
		"comment 2":          {react(conventions.ReactionAcknowledged, "watcher"), react(conventions.ReactionResolved, "watcher")},
		"comment 3":          {react(conventions.ReactionAcknowledged, "carol")},
		"comment 4":          {react(conventions.ReactionAcknowledged, "watcher"), react(conventions.ReactionRedo, "alice")},
		"comment 5":          {react(conventions.ReactionAcknowledged, "watcher"), react(conventions.ReactionRedo, "watcher")},
		"review PRR_10":      {react(conventions.ReactionAcknowledged, "watcher")},
		"review-comment 100": {react(conventions.ReactionAcknowledged, "watcher")},
	}
	ri := conventions.NewReactionInterpreter(reactions, "watcher", nil)
	got := Pending(context.Background(), pr, h, Policy{SelfLogin: "watcher"}, ri, time.Time{}, time.Time{})
	if want := "comment 3, comment 4, review-comment 101"; ids(got) != want {
		t.Errorf("pending = %s, want %s", ids(got), want)
	}
}

type fakeLister struct {
	commits  []*githubv39.RepositoryCommit
	comments []*githubv39.IssueComment
	reviews  []*githubv39.PullRequestReview
	inline   []*githubv39.PullRequestComment
	err      error
}

func (f fakeLister) ListCommits(context.Context, int) ([]*githubv39.RepositoryCommit, error) {
	return f.commits, f.err
}

func (f fakeLister) ListIssueComments(context.Context, int) ([]*githubv39.IssueComment, error) {
	return f.comments, nil
}

func (f fakeLister) ListReviews(context.Context, int) ([]*githubv39.PullRequestReview, error) {
	return f.reviews, nil
}

func (f fakeLister) ListAllReviewComments(context.Context, int) ([]*githubv39.PullRequestComment, error) {
	return f.inline, nil
}

// Fetch groups inline comments by review, dropping those with none, and
// tolerates a failure to list commits.
func TestFetch(t *testing.T) {
	commit := func(m int) *githubv39.RepositoryCommit {
		return &githubv39.RepositoryCommit{Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: tp(at(m))}}}
	}
	l := fakeLister{
		commits: []*githubv39.RepositoryCommit{commit(3), commit(7), commit(5)},
		inline:  []*githubv39.PullRequestComment{inline(100, 10, "bob", 1, "a"), inline(101, 0, "bob", 1, "b"), inline(102, 10, "bob", 2, "c")},
	}
	h, err := Fetch(context.Background(), l, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !h.LastCommitTime.Equal(at(7)) {
		t.Errorf("last commit = %v", h.LastCommitTime)
	}
	if len(h.ReviewComments) != 1 || len(h.ReviewComments[10]) != 2 {
		t.Errorf("review comments = %v", h.ReviewComments)
	}
	l.err = errors.New("boom")
	if h, err = Fetch(context.Background(), l, 1); err != nil || !h.LastCommitTime.IsZero() {
		t.Errorf("Fetch with failing commits = %v, %v", h, err)
	}
}

type fakeReactor struct{ got []string }

func (f *fakeReactor) AddIssueCommentReaction(_ context.Context, id int64, c string) error {
	f.got = append(f.got, "comment "+githubv39.Stringify(id)+" "+c)
	return nil
}

func (f *fakeReactor) AddPullRequestCommentReaction(_ context.Context, id int64, c string) error {
	f.got = append(f.got, "review-comment "+githubv39.Stringify(id)+" "+c)
	return errors.New("forbidden")
}

func (f *fakeReactor) AddReviewReaction(_ context.Context, node string, c string) error {
	f.got = append(f.got, "review "+node+" "+c)
	return nil
}

// React reaches each kind its own way, skips a review without a node ID,
// tries every item and returns the first failure.
func TestReact(t *testing.T) {
	r := &fakeReactor{}
	err := React(context.Background(), r, []Item{
		{Kind: KindReviewComment, ID: 100},
		{Kind: KindComment, ID: 1},
		{Kind: KindReview, ID: 10, NodeID: "PRR_10"},
		{Kind: KindReview, ID: 11},
	}, conventions.ReactionAcknowledged)
	if want := "review-comment 100 eyes, comment 1 eyes, review PRR_10 eyes"; strings.Join(r.got, ", ") != want {
		t.Errorf("reactions = %s, want %s", strings.Join(r.got, ", "), want)
	}
	if err == nil || !strings.Contains(err.Error(), "review-comment 100") {
		t.Errorf("err = %v", err)
	}
}
