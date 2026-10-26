package whyclosed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

type fakeGitHub struct {
	prs      map[int]*githubv39.PullRequest
	timeline map[int][]*githubv39.Timeline
	comments map[int][]*githubv39.IssueComment
	added    map[int][]string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		prs:      make(map[int]*githubv39.PullRequest),
		timeline: make(map[int][]*githubv39.Timeline),
		comments: make(map[int][]*githubv39.IssueComment),
		added:    make(map[int][]string),
	}
}

func (f *fakeGitHub) GetPullRequest(_ context.Context, number int) (*githubv39.PullRequest, error) {
	return f.prs[number], nil
}

func (f *fakeGitHub) ListIssueTimeline(_ context.Context, issueNum int) ([]*githubv39.Timeline, bool, error) {
	return f.timeline[issueNum], true, nil
}

func (f *fakeGitHub) AddComment(_ context.Context, number int, body string) error {
	f.added[number] = append(f.added[number], body)
	f.comments[number] = append(f.comments[number], &githubv39.IssueComment{
		Body:      githubv39.String(body),
		CreatedAt: &time.Time{},
	})
	return nil
}

func (f *fakeGitHub) ListIssueComments(_ context.Context, number int) ([]*githubv39.IssueComment, error) {
	return f.comments[number], nil
}

func TestWhyClosed_Disabled(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{Enabled: false}, Deps{GitHub: gh})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(10),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("bot")},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if len(gh.added[10]) > 0 {
		t.Errorf("expected no comments when disabled, got %v", gh.added[10])
	}
	if tracker.State().IsAsked(10) {
		t.Error("expected PR not recorded as asked when disabled")
	}
}

func TestWhyClosed_MergedOrOpenIgnored(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{Enabled: true}, Deps{GitHub: gh})

	openPR := &githubv39.PullRequest{
		Number: githubv39.Int(1),
		State:  githubv39.String("open"),
		User:   &githubv39.User{Login: githubv39.String("bot")},
	}
	tracker.OnClosedPR(context.Background(), openPR)

	mergedPR := &githubv39.PullRequest{
		Number: githubv39.Int(2),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(true),
		User:   &githubv39.User{Login: githubv39.String("bot")},
	}
	tracker.OnClosedPR(context.Background(), mergedPR)

	if len(gh.added) > 0 {
		t.Errorf("expected no comments for open or merged PRs, got %v", gh.added)
	}
}

func TestWhyClosed_NonAgentPRIgnored(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
		BotUsers:    []string{"pool-bot"},
	}, Deps{GitHub: gh})

	humanPR := &githubv39.PullRequest{
		Number: githubv39.Int(3),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("human-dev")},
		Body:   githubv39.String("Just a regular human PR"),
	}
	tracker.OnClosedPR(context.Background(), humanPR)

	if len(gh.added[3]) > 0 {
		t.Errorf("expected no comments for non-agent PR, got %v", gh.added[3])
	}
}

func TestWhyClosed_BotCloserIgnored(t *testing.T) {
	gh := newFakeGitHub()
	gh.timeline[4] = []*githubv39.Timeline{
		{
			Event: githubv39.String("closed"),
			Actor: &githubv39.User{Login: githubv39.String("our-bot")},
		},
	}

	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
		BotUsers:    []string{"pool-bot"},
	}, Deps{GitHub: gh})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(4),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("pool-bot")},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if len(gh.added[4]) > 0 {
		t.Errorf("expected no comments when bot closed PR itself, got %v", gh.added[4])
	}
}

func TestWhyClosed_PromptPostedAndRecorded(t *testing.T) {
	gh := newFakeGitHub()
	gh.timeline[5] = []*githubv39.Timeline{
		{
			Event: githubv39.String("closed"),
			Actor: &githubv39.User{Login: githubv39.String("alice")},
		},
	}

	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
		BotUsers:    []string{"pool-bot"},
	}, Deps{GitHub: gh})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(5),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("pool-bot")},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if len(gh.added[5]) != 1 {
		t.Fatalf("expected 1 comment on PR #5, got %d", len(gh.added[5]))
	}

	comment := gh.added[5][0]
	if !strings.Contains(comment, "@alice closing?") {
		t.Errorf("expected comment to mention @alice, got: %s", comment)
	}
	if !strings.Contains(comment, WhyClosedMarker) {
		t.Errorf("expected comment to include marker %s, got: %s", WhyClosedMarker, comment)
	}

	if !tracker.State().IsAsked(5) {
		t.Error("expected PR #5 to be recorded as asked in state")
	}
	st, ok := tracker.State().Get(5)
	if !ok || st.Closer != "alice" {
		t.Errorf("expected closer alice, got %v", st)
	}

	// Calling OnClosedPR a second time must NOT post again
	tracker.OnClosedPR(context.Background(), pr)
	if len(gh.added[5]) != 1 {
		t.Errorf("expected still 1 comment after second call, got %d", len(gh.added[5]))
	}
}

func TestWhyClosed_ExistingMarkerPreventsDuplicate(t *testing.T) {
	gh := newFakeGitHub()
	gh.timeline[6] = []*githubv39.Timeline{
		{
			Event: githubv39.String("closed"),
			Actor: &githubv39.User{Login: githubv39.String("bob")},
		},
	}
	gh.comments[6] = []*githubv39.IssueComment{
		{
			Body:      githubv39.String("Some comment\n\n" + WhyClosedMarker),
			CreatedAt: &time.Time{},
		},
	}

	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
	}, Deps{GitHub: gh})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(6),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("our-bot")},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if len(gh.added[6]) > 0 {
		t.Errorf("expected no comment when marker exists, got %v", gh.added[6])
	}
	if !tracker.State().IsAsked(6) {
		t.Error("expected PR #6 to be recognized as asked")
	}
}

func TestWhyClosed_LabelAtCloseSkipsPrompt(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:      true,
		GitHubLogin:  "our-bot",
		TriggerLabel: "overseer",
	}, Deps{GitHub: gh})

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(7),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("our-bot")},
		Labels: []*githubv39.Label{
			{Name: githubv39.String("overseer/closed-wrong-approach")},
		},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if len(gh.added[7]) > 0 {
		t.Errorf("expected no prompt when label already exists, got %v", gh.added[7])
	}

	reason, ok := tracker.CloseReason(7)
	if !ok || reason != "wrong-approach" {
		t.Errorf("expected reason wrong-approach, got %s (ok=%v)", reason, ok)
	}
}

func TestWhyClosed_PollHarvestsCloserReply(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:       true,
		GitHubLogin:   "our-bot",
		TrustedLogins: []string{"maintainer-1"},
	}, Deps{GitHub: gh})

	askedTime := time.Now().Add(-2 * time.Hour)
	tracker.State().RecordAsked(8, "alice", askedTime)

	gh.prs[8] = &githubv39.PullRequest{Number: githubv39.Int(8)}
	gh.comments[8] = []*githubv39.IssueComment{
		{
			Body:      githubv39.String("@alice closing?\n\n" + WhyClosedMarker),
			CreatedAt: &askedTime,
		},
		{
			Body: githubv39.String("duplicate of #123\nsuperseded"),
			User: &githubv39.User{Login: githubv39.String("alice")},
			CreatedAt: func() *time.Time {
				t := askedTime.Add(10 * time.Minute)
				return &t
			}(),
		},
	}

	tracker.Poll(context.Background())

	reason, ok := tracker.CloseReason(8)
	if !ok || reason != "duplicate of #123" {
		t.Errorf("expected close reason 'duplicate of #123', got %q (ok=%v)", reason, ok)
	}
}

func TestWhyClosed_PollHarvestsMaintainerReply(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:       true,
		GitHubLogin:   "our-bot",
		TrustedLogins: []string{"lead-dev"},
	}, Deps{GitHub: gh})

	askedTime := time.Now().Add(-1 * time.Hour)
	tracker.State().RecordAsked(9, "alice", askedTime)

	gh.prs[9] = &githubv39.PullRequest{Number: githubv39.Int(9)}
	gh.comments[9] = []*githubv39.IssueComment{
		{
			Body:      githubv39.String("prompt\n\n" + WhyClosedMarker),
			CreatedAt: &askedTime,
		},
		{
			Body: githubv39.String("not needed anymore"),
			User: &githubv39.User{Login: githubv39.String("lead-dev")},
			CreatedAt: func() *time.Time {
				t := askedTime.Add(5 * time.Minute)
				return &t
			}(),
		},
	}

	tracker.Poll(context.Background())

	reason, ok := tracker.CloseReason(9)
	if !ok || reason != "not needed anymore" {
		t.Errorf("expected close reason 'not needed anymore', got %q (ok=%v)", reason, ok)
	}
}

func TestWhyClosed_PollIgnoresUntrustedAndBot(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
		BotUsers:    []string{"ci-bot"},
	}, Deps{GitHub: gh})

	askedTime := time.Now().Add(-1 * time.Hour)
	tracker.State().RecordAsked(10, "alice", askedTime)

	gh.prs[10] = &githubv39.PullRequest{Number: githubv39.Int(10)}
	gh.comments[10] = []*githubv39.IssueComment{
		{
			Body:      githubv39.String("prompt\n\n" + WhyClosedMarker),
			CreatedAt: &askedTime,
		},
		{
			Body:              githubv39.String("random comment from stranger"),
			User:              &githubv39.User{Login: githubv39.String("stranger")},
			AuthorAssociation: githubv39.String("NONE"),
			CreatedAt: func() *time.Time {
				t := askedTime.Add(2 * time.Minute)
				return &t
			}(),
		},
		{
			Body: githubv39.String("CI finished"),
			User: &githubv39.User{Login: githubv39.String("ci-bot")},
			CreatedAt: func() *time.Time {
				t := askedTime.Add(3 * time.Minute)
				return &t
			}(),
		},
	}

	tracker.Poll(context.Background())

	if reason, ok := tracker.CloseReason(10); ok {
		t.Errorf("expected no close reason from untrusted or bot, got %q", reason)
	}
}

func TestWhyClosed_PollHarvestsLabel(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:      true,
		GitHubLogin:  "our-bot",
		TriggerLabel: "gemini",
	}, Deps{GitHub: gh})

	askedTime := time.Now().Add(-1 * time.Hour)
	tracker.State().RecordAsked(11, "alice", askedTime)

	gh.prs[11] = &githubv39.PullRequest{
		Number: githubv39.Int(11),
		Labels: []*githubv39.Label{
			{Name: githubv39.String("gemini/closed-superseded")},
		},
	}

	tracker.Poll(context.Background())

	reason, ok := tracker.CloseReason(11)
	if !ok || reason != "superseded" {
		t.Errorf("expected close reason 'superseded', got %q (ok=%v)", reason, ok)
	}
}

func TestWhyClosed_PollExpiresAfter7Days(t *testing.T) {
	gh := newFakeGitHub()
	tracker := New(Config{
		Enabled:     true,
		GitHubLogin: "our-bot",
	}, Deps{GitHub: gh})

	oldAskedTime := time.Now().Add(-8 * 24 * time.Hour) // 8 days ago
	tracker.State().RecordAsked(12, "alice", oldAskedTime)

	gh.prs[12] = &githubv39.PullRequest{
		Number: githubv39.Int(12),
		Labels: []*githubv39.Label{
			{Name: githubv39.String("overseer/closed-stale")},
		},
	}

	tracker.Poll(context.Background())

	if reason, ok := tracker.CloseReason(12); ok {
		t.Errorf("expected PR older than 7 days not to be polled, got %q", reason)
	}
}

func TestWhyClosed_StatePersistence(t *testing.T) {
	tmpDir := t.TempDir()

	store1 := NewStateStore(tmpDir)
	askedAt := time.Now().Truncate(time.Second)
	store1.RecordAsked(100, "alice", askedAt)
	store1.SetCloseReason(100, "wrong-direction", askedAt.Add(time.Minute))

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, StateFileName)); err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}

	// Create second store from same dir
	store2 := NewStateStore(tmpDir)
	st, ok := store2.Get(100)
	if !ok {
		t.Fatal("expected PR #100 to be restored")
	}
	if st.Closer != "alice" || st.CloseReason != "wrong-direction" || !st.Asked {
		t.Errorf("unexpected restored state: %+v", st)
	}
}
