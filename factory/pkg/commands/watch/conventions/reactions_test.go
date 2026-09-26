package conventions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

const (
	testSelfLogin  = "factory-bot"
	testAllowBot   = "trusted-bot"
	testIgnoredBot = "random-bot"
	testHuman      = "human-dev"
)

func testBots() []string { return []string{testAllowBot} }

// reaction builds a reaction of the given content attributed to the given login.
func reaction(content Reaction, login string) *githubv39.Reaction {
	user := &githubv39.User{Login: stringPtr(login)}
	if strings.Contains(login, "bot") {
		user.Type = stringPtr("Bot")
	}
	return &githubv39.Reaction{Content: stringPtr(string(content)), User: user}
}

// TestInterpretAttribution covers the half of the meaning that comes from who
// reacted: the watcher's own marks say a comment is handled, and the same emoji
// from a human means nothing of the sort.
func TestInterpretAttribution(t *testing.T) {
	tests := []struct {
		name      string
		reactions []*githubv39.Reaction
		want      CommentState
	}{
		{
			name:      "no reactions",
			reactions: nil,
			want:      CommentState{},
		},
		{
			name:      "watcher acknowledged",
			reactions: []*githubv39.Reaction{reaction(ReactionAcknowledged, testSelfLogin)},
			want:      CommentState{Acknowledged: true},
		},
		{
			// An allowlisted bot is one whose comments the watcher acts on, so
			// it stands on the commenter's side rather than the watcher's.
			name:      "allowlisted bot reacts as a commenter",
			reactions: []*githubv39.Reaction{reaction(ReactionAcknowledged, testAllowBot)},
			want:      CommentState{},
		},
		{
			name:      "allowlisted bot can request a redo",
			reactions: []*githubv39.Reaction{reaction(ReactionRedo, testAllowBot)},
			want:      CommentState{RedoRequested: true},
		},
		{
			// A bot the watcher ignores is not a voice it listens to, so its
			// marks fall on the watcher's own side.
			name:      "unallowlisted bot counts as the watcher's side",
			reactions: []*githubv39.Reaction{reaction(ReactionAcknowledged, testIgnoredBot)},
			want:      CommentState{Acknowledged: true},
		},
		{
			name:      "human eyes is not an acknowledgement",
			reactions: []*githubv39.Reaction{reaction(ReactionAcknowledged, testHuman)},
			want:      CommentState{},
		},
		{
			name:      "human rocket requests a redo",
			reactions: []*githubv39.Reaction{reaction(ReactionRedo, testHuman)},
			want:      CommentState{RedoRequested: true},
		},
		{
			name:      "watcher's own rocket is not a redo request",
			reactions: []*githubv39.Reaction{reaction(ReactionRedo, testSelfLogin)},
			want:      CommentState{},
		},
		{
			name:      "unrelated emoji is ignored",
			reactions: []*githubv39.Reaction{reaction("heart", testHuman)},
			want:      CommentState{},
		},
		{
			name: "one mark of each kind",
			reactions: []*githubv39.Reaction{
				reaction(ReactionAcknowledged, testSelfLogin),
				reaction(ReactionResolved, testSelfLogin),
				reaction(ReactionFailed, testSelfLogin),
				reaction(ReactionRedo, testHuman),
			},
			want: CommentState{Acknowledged: true, Resolved: true, Failed: true, RedoRequested: true},
		},
		{
			name: "a human's mark does not clear the watcher's own",
			reactions: []*githubv39.Reaction{
				reaction(ReactionResolved, testSelfLogin),
				reaction(ReactionResolved, testHuman),
			},
			want: CommentState{Resolved: true},
		},
	}

	interpreter := NewReactionInterpreter(nil, testSelfLogin, testBots())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interpreter.Interpret(tt.reactions); got != tt.want {
				t.Errorf("Interpret() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestNeedsAttention covers the precedence between the marks, which is the rule
// that decides whether the watcher queues a task for a comment.
func TestNeedsAttention(t *testing.T) {
	tests := []struct {
		name  string
		state CommentState
		want  bool
	}{
		{"unmarked comment is waiting", CommentState{}, true},
		{"acknowledged comment is in flight", CommentState{Acknowledged: true}, false},
		{"resolved comment is done", CommentState{Resolved: true}, false},
		{"failed comment is not retried on its own", CommentState{Failed: true}, false},
		{"redo on an unmarked comment", CommentState{RedoRequested: true}, true},
		{"redo reopens an acknowledged comment", CommentState{Acknowledged: true, RedoRequested: true}, true},
		{"redo reopens a failed comment", CommentState{Failed: true, RedoRequested: true}, true},
		// Resolved outranks the redo request: the fix for that feedback is
		// already in the branch, so another pass would act on a stale comment.
		{"redo does not reopen a resolved comment", CommentState{Resolved: true, RedoRequested: true}, false},
		{"resolved outranks an acknowledgement", CommentState{Acknowledged: true, Resolved: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.NeedsAttention(); got != tt.want {
				t.Errorf("CommentState%+v.NeedsAttention() = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

// fakeReactionLister serves a canned reaction set, or an error.
type fakeReactionLister struct {
	reactions []*githubv39.Reaction
	err       error
	calls     int
}

func (f *fakeReactionLister) IssueCommentReactions(_ context.Context, _ int64) ([]*githubv39.Reaction, error) {
	f.calls++
	return f.reactions, f.err
}

// TestCommentStateReadsOncePerComment guards the reason the whole state is
// returned at once: the scanner asks several questions of every comment, and a
// request each would multiply the cost of a scan.
func TestCommentStateReadsOncePerComment(t *testing.T) {
	lister := &fakeReactionLister{reactions: []*githubv39.Reaction{
		reaction(ReactionAcknowledged, testSelfLogin),
		reaction(ReactionRedo, testHuman),
	}}
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots())

	state := interpreter.CommentState(context.Background(), 42)

	if lister.calls != 1 {
		t.Errorf("IssueCommentReactions called %d times, want 1", lister.calls)
	}
	want := CommentState{Acknowledged: true, RedoRequested: true}
	if state != want {
		t.Errorf("CommentState() = %+v, want %+v", state, want)
	}
	if !state.NeedsAttention() {
		t.Error("NeedsAttention() = false, want true: a human asked for another pass")
	}
}

// TestCommentStateFetchFailure pins the failure direction. Reading an
// unreachable comment as unmarked makes the watcher redo work; reading it as
// handled would drop the feedback silently, which is the worse outcome.
func TestCommentStateFetchFailure(t *testing.T) {
	lister := &fakeReactionLister{err: errors.New("github is down")}
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots())

	state := interpreter.CommentState(context.Background(), 42)

	if state != (CommentState{}) {
		t.Errorf("CommentState() = %+v, want zero value", state)
	}
	if !state.NeedsAttention() {
		t.Error("NeedsAttention() = false, want true: a failed read must not drop feedback")
	}
}

// TestCommentStateWithoutLister covers the scanner constructed without a GitHub
// client, which several tests and dry runs do.
func TestCommentStateWithoutLister(t *testing.T) {
	interpreter := NewReactionInterpreter(nil, testSelfLogin, testBots())
	if got := interpreter.CommentState(context.Background(), 42); got != (CommentState{}) {
		t.Errorf("CommentState() = %+v, want zero value", got)
	}
}

// TestCommentStateUsesCache verifies that calling CommentState multiple times
// within the TTL on the same comment ID reuses the cached value and does not make
// duplicate lister calls.
func TestCommentStateUsesCache(t *testing.T) {
	lister := &fakeReactionLister{reactions: []*githubv39.Reaction{
		reaction(ReactionAcknowledged, testSelfLogin),
	}}
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots())

	// First call - should hit lister
	state1 := interpreter.CommentState(context.Background(), 42)
	// Second call - should hit cache
	state2 := interpreter.CommentState(context.Background(), 42)

	if lister.calls != 1 {
		t.Errorf("IssueCommentReactions called %d times, want exactly 1 call (caching should prevent the 2nd call)", lister.calls)
	}
	want := CommentState{Acknowledged: true}
	if state1 != want {
		t.Errorf("state1 = %+v, want %+v", state1, want)
	}
	if state2 != want {
		t.Errorf("state2 = %+v, want %+v", state2, want)
	}
}

// TestReactionInterpreter_WithTTL_Prune_Clear verifies the Custom TTL, Prune, and Clear behaviors.
func TestReactionInterpreter_WithTTL_Prune_Clear(t *testing.T) {
	lister := &fakeReactionLister{reactions: []*githubv39.Reaction{
		reaction(ReactionAcknowledged, testSelfLogin),
	}}
	// Set a very small TTL (e.g., 5 milliseconds)
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots()).WithTTL(5 * time.Millisecond)

	// First call - should cache
	_ = interpreter.CommentState(context.Background(), 42)
	_ = interpreter.CommentState(context.Background(), 42)
	if lister.calls != 1 {
		t.Fatalf("Expected 1 call, got %d", lister.calls)
	}

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	// Third call - expired, should hit lister again
	_ = interpreter.CommentState(context.Background(), 42)
	if lister.calls != 2 {
		t.Fatalf("Expected 2 calls after expiration, got %d", lister.calls)
	}

	// Set a large TTL
	interpreter.WithTTL(10 * time.Second)
	_ = interpreter.CommentState(context.Background(), 42)
	_ = interpreter.CommentState(context.Background(), 43)

	// Prune should delete expired but keep non-expired.
	// Since we set a large TTL, 42 and 43 are not expired.
	interpreter.Prune()
	interpreter.mu.RLock()
	if len(interpreter.cache) != 2 {
		t.Errorf("Expected 2 cached entries, got %d", len(interpreter.cache))
	}
	interpreter.mu.RUnlock()

	// Clear should empty the cache completely
	interpreter.Clear()
	interpreter.mu.RLock()
	if len(interpreter.cache) != 0 {
		t.Errorf("Expected 0 cached entries after Clear(), got %d", len(interpreter.cache))
	}
	interpreter.mu.RUnlock()
}

func TestReactionInterpreter_WithTTL_NonPositive_DisablesCaching(t *testing.T) {
	lister := &fakeReactionLister{reactions: []*githubv39.Reaction{
		reaction(ReactionAcknowledged, testSelfLogin),
	}}
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots())

	// Default should be 1 minute
	if interpreter.ttl != 1*time.Minute {
		t.Errorf("Expected default TTL of 1m, got %s", interpreter.ttl)
	}

	// Setting non-positive TTL (e.g. 0) should set it and bypass/disable caching
	// First, set a positive TTL and populate the cache
	interpreter.WithTTL(5 * time.Minute)
	_ = interpreter.CommentState(context.Background(), 99)
	interpreter.mu.RLock()
	if len(interpreter.cache) == 0 {
		t.Errorf("Expected cache to be populated, but it was empty")
	}
	interpreter.mu.RUnlock()

	// Setting non-positive TTL should clear it immediately
	interpreter.WithTTL(0)
	interpreter.mu.RLock()
	if len(interpreter.cache) != 0 {
		t.Errorf("Expected cache to be cleared immediately when TTL is set to non-positive value, but got %d entries", len(interpreter.cache))
	}
	interpreter.mu.RUnlock()

	// Reset lister calls to isolate the bypass test
	lister.calls = 0

	if interpreter.ttl != 0 {
		t.Errorf("Expected TTL of 0, got %s", interpreter.ttl)
	}

	// First call
	_ = interpreter.CommentState(context.Background(), 42)
	// Second call - should not cache because TTL is 0
	_ = interpreter.CommentState(context.Background(), 42)

	if lister.calls != 2 {
		t.Errorf("Expected cache to be bypassed when TTL is non-positive, got %d calls instead of 2", lister.calls)
	}

	// Setting negative TTL (e.g. -1s) should also set it and bypass caching
	interpreter.WithTTL(-1 * time.Second)
	if interpreter.ttl != -1*time.Second {
		t.Errorf("Expected TTL of -1s, got %s", interpreter.ttl)
	}

	// Third call
	_ = interpreter.CommentState(context.Background(), 42)
	if lister.calls != 3 {
		t.Errorf("Expected cache to be bypassed when TTL is negative, got %d calls instead of 3", lister.calls)
	}

	// Setting positive TTL should work
	interpreter.WithTTL(2 * time.Minute)
	if interpreter.ttl != 2*time.Minute {
		t.Errorf("Expected TTL of 2m after positive WithTTL, got %s", interpreter.ttl)
	}
}

func TestReactionInterpreter_CacheEviction(t *testing.T) {
	lister := &fakeReactionLister{}
	interpreter := NewReactionInterpreter(lister, testSelfLogin, testBots())

	// Call CommentState 1005 times. Since maxCommentReactionCacheSize is 1000,
	// this will trigger eviction on the 1001st call, reducing size to 899,
	// and subsequent calls will bring it to 904.
	for i := 0; i < 1005; i++ {
		_ = interpreter.CommentState(context.Background(), int64(i))
	}

	interpreter.mu.RLock()
	cacheSize := len(interpreter.cache)
	interpreter.mu.RUnlock()

	if cacheSize > 1000 {
		t.Errorf("Expected cache size to be capped, but got %d", cacheSize)
	}
	if cacheSize != 904 {
		t.Errorf("Expected cache size after eviction to be 904, but got %d", cacheSize)
	}
}
