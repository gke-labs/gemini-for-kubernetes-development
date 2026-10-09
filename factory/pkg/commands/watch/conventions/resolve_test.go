package conventions

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// fakeResolverClient serves canned feedback and reactions for each of the
// three kinds of feedback on a pull request, and records which items had their
// reactions read and which reactions were added.
type fakeResolverClient struct {
	comments    []*githubv39.IssueComment
	reviews     []*githubv39.PullRequestReview
	revComments []*githubv39.PullRequestComment

	commentReactions   map[int64][]*githubv39.Reaction
	reviewReactions    map[string][]*githubv39.Reaction
	revCommentReaction map[int64][]*githubv39.Reaction

	listCommentsErr    error
	listReviewsErr     error
	listRevCommentsErr error

	read  []string
	added []string
}

func (f *fakeResolverClient) IssueCommentReactions(_ context.Context, id int64) ([]*githubv39.Reaction, error) {
	f.read = append(f.read, "comment:"+itoa(id))
	return f.commentReactions[id], nil
}

func (f *fakeResolverClient) ListIssueComments(context.Context, int) ([]*githubv39.IssueComment, error) {
	return f.comments, f.listCommentsErr
}

func (f *fakeResolverClient) AddIssueCommentReaction(_ context.Context, id int64, content string) error {
	f.added = append(f.added, "comment:"+itoa(id)+":"+content)
	return nil
}

func (f *fakeResolverClient) ListReviews(context.Context, int) ([]*githubv39.PullRequestReview, error) {
	return f.reviews, f.listReviewsErr
}

func (f *fakeResolverClient) ReviewReactions(_ context.Context, nodeID string) ([]*githubv39.Reaction, error) {
	f.read = append(f.read, "review:"+nodeID)
	return f.reviewReactions[nodeID], nil
}

func (f *fakeResolverClient) AddReviewReaction(_ context.Context, nodeID string, content string) error {
	f.added = append(f.added, "review:"+nodeID+":"+content)
	return nil
}

func (f *fakeResolverClient) ListAllReviewComments(context.Context, int) ([]*githubv39.PullRequestComment, error) {
	return f.revComments, f.listRevCommentsErr
}

func (f *fakeResolverClient) PullRequestCommentReactions(_ context.Context, id int64) ([]*githubv39.Reaction, error) {
	f.read = append(f.read, "review-comment:"+itoa(id))
	return f.revCommentReaction[id], nil
}

func (f *fakeResolverClient) AddPullRequestCommentReaction(_ context.Context, id int64, content string) error {
	f.added = append(f.added, "review-comment:"+itoa(id)+":"+content)
	return nil
}

func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}

func user(login string) *githubv39.User {
	return &githubv39.User{Login: stringPtr(login)}
}

func int64Ptr(i int64) *int64 { return &i }

// newAcknowledgedFixture has one acknowledged and one unacknowledged item of
// each kind of feedback, plus one acknowledged item authored by the watcher
// itself, which must never be resolved.
func newAcknowledgedFixture() *fakeResolverClient {
	acked := []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin)}
	return &fakeResolverClient{
		comments: []*githubv39.IssueComment{
			{ID: int64Ptr(1), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(2), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(3), User: user(testSelfLogin)},
		},
		reviews: []*githubv39.PullRequestReview{
			{ID: int64Ptr(10), NodeID: stringPtr("PRR_10"), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(11), NodeID: stringPtr("PRR_11"), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(12), NodeID: stringPtr("PRR_12"), User: user(testSelfLogin)},
			// A review with no node ID cannot be reached for reactions.
			{ID: int64Ptr(13), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
		},
		revComments: []*githubv39.PullRequestComment{
			{ID: int64Ptr(20), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(21), User: user(testHuman), AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(22), User: user(testSelfLogin)},
		},
		commentReactions:   map[int64][]*githubv39.Reaction{1: acked, 3: acked},
		reviewReactions:    map[string][]*githubv39.Reaction{"PRR_10": acked, "PRR_12": acked},
		revCommentReaction: map[int64][]*githubv39.Reaction{20: acked, 22: acked},
	}
}

const testReviewerBot = "gemini-code-assist[bot]"

func testResolveOptions(resolution Reaction) ResolveOptions {
	return ResolveOptions{
		PRNumber:        7,
		Resolution:      resolution,
		SelfLogin:       testSelfLogin,
		AllowlistedBots: testBots(),
		ReviewerLogins:  []string{testReviewerBot},
		// As config.FactoryConfig.TrustedLogins composes it.
		TrustedLogins: append(testBots(), testReviewerBot),
	}
}

func TestResolveCommentReactions(t *testing.T) {
	for _, tt := range []struct {
		name       string
		resolution Reaction
	}{
		{"success", ReactionResolved},
		{"failure", ReactionFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := newAcknowledgedFixture()

			ResolveCommentReactions(context.Background(), client, testResolveOptions(tt.resolution))

			r := string(tt.resolution)
			want := []string{
				"comment:1:" + r,
				"review:PRR_10:" + r,
				"review-comment:20:" + r,
			}
			assertAdded(t, client.added, want)
		})
	}
}

// TestResolveCommentReactions_IndependentKinds guards that failing to list one
// kind of feedback does not stop the others from being resolved.
func TestResolveCommentReactions_IndependentKinds(t *testing.T) {
	client := newAcknowledgedFixture()
	client.listCommentsErr = errors.New("github is down")
	client.listReviewsErr = errors.New("github is down")

	ResolveCommentReactions(context.Background(), client, testResolveOptions(ReactionResolved))

	assertAdded(t, client.added, []string{"review-comment:20:+1"})
}

// TestResolveCommentReactions_ReviewerBot guards that feedback from a review
// bot, which the scanner acts on and acknowledges even though the bot is not
// allowlisted, also receives its outcome.
func TestResolveCommentReactions_ReviewerBot(t *testing.T) {
	acked := []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin)}
	bot := &githubv39.User{Login: stringPtr(testReviewerBot), Type: stringPtr("Bot")}
	client := &fakeResolverClient{
		comments:           []*githubv39.IssueComment{{ID: int64Ptr(1), User: bot}},
		reviews:            []*githubv39.PullRequestReview{{ID: int64Ptr(10), NodeID: stringPtr("PRR_10"), User: bot}},
		revComments:        []*githubv39.PullRequestComment{{ID: int64Ptr(20), User: bot}},
		commentReactions:   map[int64][]*githubv39.Reaction{1: acked},
		reviewReactions:    map[string][]*githubv39.Reaction{"PRR_10": acked},
		revCommentReaction: map[int64][]*githubv39.Reaction{20: acked},
	}

	ResolveCommentReactions(context.Background(), client, testResolveOptions(ReactionResolved))

	assertAdded(t, client.added, []string{"comment:1:+1", "review:PRR_10:+1", "review-comment:20:+1"})
}

// TestResolveCommentReactions_ResolvedIsFinal guards that feedback an earlier
// task resolved is never stamped again - in particular not with 'confused' by a
// later task that failed.
func TestResolveCommentReactions_ResolvedIsFinal(t *testing.T) {
	human := user(testHuman)
	client := &fakeResolverClient{
		revComments: []*githubv39.PullRequestComment{
			{ID: int64Ptr(20), User: human, AuthorAssociation: stringPtr("MEMBER")},
			{ID: int64Ptr(21), User: human, AuthorAssociation: stringPtr("MEMBER")},
		},
		revCommentReaction: map[int64][]*githubv39.Reaction{
			20: {reaction(ReactionAcknowledged, testSelfLogin), reaction(ReactionResolved, testSelfLogin)},
			21: {reaction(ReactionAcknowledged, testSelfLogin)},
		},
	}

	ResolveCommentReactions(context.Background(), client, testResolveOptions(ReactionFailed))

	assertAdded(t, client.added, []string{"review-comment:21:confused"})
}

// TestResolveCommentReactions_RetryResolvesFailure covers the retry path: a
// failed address-comments task leaves its feedback 'confused', the retry runs
// against the same feedback without acknowledging it again, and when the retry
// succeeds that feedback gets its '+1'.
func TestResolveCommentReactions_RetryResolvesFailure(t *testing.T) {
	human := user(testHuman)
	bot := &githubv39.User{Login: stringPtr(testReviewerBot), Type: stringPtr("Bot")}
	failed := []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin), reaction(ReactionFailed, testSelfLogin)}
	client := &fakeResolverClient{
		comments:           []*githubv39.IssueComment{{ID: int64Ptr(1), User: human, AuthorAssociation: stringPtr("MEMBER")}},
		reviews:            []*githubv39.PullRequestReview{{ID: int64Ptr(10), NodeID: stringPtr("PRR_10"), User: bot}},
		revComments:        []*githubv39.PullRequestComment{{ID: int64Ptr(20), User: bot}},
		commentReactions:   map[int64][]*githubv39.Reaction{1: failed},
		reviewReactions:    map[string][]*githubv39.Reaction{"PRR_10": failed},
		revCommentReaction: map[int64][]*githubv39.Reaction{20: failed},
	}

	ResolveCommentReactions(context.Background(), client, testResolveOptions(ReactionResolved))

	assertAdded(t, client.added, []string{"comment:1:+1", "review:PRR_10:+1", "review-comment:20:+1"})
}

// TestResolveCommentReactions_Since guards that feedback older than the task's
// trigger is skipped without its reactions being read.
func TestResolveCommentReactions_Since(t *testing.T) {
	since := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	before := timePtr(since.Add(-time.Minute))
	at := timePtr(since)
	human := user(testHuman)
	acked := []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin)}
	client := &fakeResolverClient{
		comments: []*githubv39.IssueComment{
			{ID: int64Ptr(1), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: before},
			{ID: int64Ptr(2), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: at},
		},
		reviews: []*githubv39.PullRequestReview{
			{ID: int64Ptr(10), NodeID: stringPtr("PRR_10"), User: human, AuthorAssociation: stringPtr("MEMBER"), SubmittedAt: before},
			{ID: int64Ptr(11), NodeID: stringPtr("PRR_11"), User: human, AuthorAssociation: stringPtr("MEMBER"), SubmittedAt: at},
		},
		revComments: []*githubv39.PullRequestComment{
			{ID: int64Ptr(20), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: before},
			{ID: int64Ptr(21), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: at},
		},
		commentReactions:   map[int64][]*githubv39.Reaction{1: acked, 2: acked},
		reviewReactions:    map[string][]*githubv39.Reaction{"PRR_10": acked, "PRR_11": acked},
		revCommentReaction: map[int64][]*githubv39.Reaction{20: acked, 21: acked},
	}
	opts := testResolveOptions(ReactionResolved)
	opts.Since = since

	ResolveCommentReactions(context.Background(), client, opts)

	assertAdded(t, client.added, []string{"comment:2:+1", "review:PRR_11:+1", "review-comment:21:+1"})
	if want := []string{"comment:2", "review:PRR_11", "review-comment:21"}; !equalSorted(client.read, want) {
		t.Errorf("reactions read on %v, want only %v", client.read, want)
	}
}

// TestResolveCommentReactions_InlineCommentTimedByReview covers inline comments
// drafted before the review carrying them was submitted. When the task's
// trigger is that review's submission, its inline comments are still the
// task's to resolve: they only became visible at that moment.
func TestResolveCommentReactions_InlineCommentTimedByReview(t *testing.T) {
	submitted := time.Date(2026, 9, 30, 21, 33, 55, 0, time.UTC)
	drafted := timePtr(submitted.Add(-21 * time.Second))
	human := user(testHuman)
	acked := []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin)}

	newClient := func() *fakeResolverClient {
		return &fakeResolverClient{
			reviews: []*githubv39.PullRequestReview{
				{ID: int64Ptr(10), NodeID: stringPtr("PRR_10"), User: human, AuthorAssociation: stringPtr("MEMBER"), SubmittedAt: timePtr(submitted)},
			},
			revComments: []*githubv39.PullRequestComment{
				// Drafted before, submitted with review 10.
				{ID: int64Ptr(20), PullRequestReviewID: int64Ptr(10), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: drafted},
				// Same draft time, but its review is unknown: only its own
				// creation time is available, and that predates Since.
				{ID: int64Ptr(21), PullRequestReviewID: int64Ptr(99), User: human, AuthorAssociation: stringPtr("MEMBER"), CreatedAt: drafted},
			},
			revCommentReaction: map[int64][]*githubv39.Reaction{20: acked, 21: acked},
		}
	}
	opts := testResolveOptions(ReactionResolved)
	opts.Since = submitted

	t.Run("review listed", func(t *testing.T) {
		client := newClient()
		ResolveCommentReactions(context.Background(), client, opts)
		assertAdded(t, client.added, []string{"review-comment:20:+1"})
	})

	t.Run("reviews fail to list", func(t *testing.T) {
		client := newClient()
		client.listReviewsErr = errors.New("boom")
		ResolveCommentReactions(context.Background(), client, opts)
		// Without the review, the comment falls back to its draft time.
		assertAdded(t, client.added, nil)
	})
}

func TestReviewCommentTime(t *testing.T) {
	created := time.Date(2026, 9, 30, 21, 33, 34, 0, time.UTC)
	rc := &githubv39.PullRequestComment{CreatedAt: timePtr(created)}
	for _, tt := range []struct {
		name      string
		submitted time.Time
		want      time.Time
	}{
		{"review submitted later", created.Add(time.Minute), created.Add(time.Minute)},
		{"review unknown", time.Time{}, created},
		// Never make a comment older than it is.
		{"review submitted earlier", created.Add(-time.Minute), created},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReviewCommentTime(rc, tt.submitted); !got.Equal(tt.want) {
				t.Errorf("ReviewCommentTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func equalSorted(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertAdded(t *testing.T, got, want []string) {
	t.Helper()
	if !equalSorted(got, want) {
		t.Fatalf("added reactions = %v, want %v", got, want)
	}
}
