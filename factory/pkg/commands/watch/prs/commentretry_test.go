package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
)

// TestCommentRetryState covers what the queue's record of the last
// address-feedback task is allowed to say about the next one.
func TestCommentRetryState(t *testing.T) {
	const headSHA = "sha-head"
	// Truncated to the second because the task file records it as RFC3339, and
	// the due time asserted below is derived from what was read back.
	failedAt := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	prCreatedAt := failedAt.Add(-24 * time.Hour)

	humanComment := func(at time.Time) []*githubv39.IssueComment {
		return []*githubv39.IssueComment{{
			User:      &githubv39.User{Login: stringPtr("reviewer-human")},
			Body:      stringPtr("please rename this"),
			CreatedAt: &at,
		}}
	}

	tests := []struct {
		name         string
		task         string
		comments     []*githubv39.IssueComment
		wantActive   bool
		wantAttempts int
	}{
		{
			name: "nothing has finished",
		},
		{
			name: "the last attempt succeeded",
			task: "status: Completed\ncommitSHA: " + headSHA + "\nattempt: 1\n",
		},
		{
			name: "a failure recorded before attempts were counted",
			task: "status: Failed\ncommitSHA: " + headSHA + "\n",
		},
		{
			name: "a failure against an older revision",
			task: "status: Failed\ncommitSHA: sha-old\nattempt: 1\n",
		},
		{
			name:         "a first failure against this revision",
			task:         "status: Failed\ncommitSHA: " + headSHA + "\nattempt: 1\n",
			wantActive:   true,
			wantAttempts: 1,
		},
		{
			name:         "a second failure against this revision",
			task:         "status: Failed\ncommitSHA: " + headSHA + "\nattempt: 2\n",
			wantActive:   true,
			wantAttempts: 2,
		},
		{
			name: "the budget is spent",
			task: "status: Failed\ncommitSHA: " + headSHA + "\nattempt: 3\n",
		},
		{
			name:     "a human has commented since the failure",
			task:     "status: Failed\ncommitSHA: " + headSHA + "\nattempt: 1\n",
			comments: humanComment(failedAt.Add(time.Minute)),
		},
		{
			// The watcher's own chatter is not a change in the situation, so it
			// must not hand the pull request a fresh budget every cycle.
			name:         "the watcher has commented since the failure",
			task:         "status: Failed\ncommitSHA: " + headSHA + "\nattempt: 1\n",
			comments:     []*githubv39.IssueComment{{User: &githubv39.User{Login: stringPtr("bot1")}, Body: stringPtr("🤖 AI Factory started addressing review feedback"), CreatedAt: timePtr(failedAt.Add(time.Minute))}},
			wantActive:   true,
			wantAttempts: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if tc.task != "" {
				writeProcessedTask(t, tempDir, "task-pr-10-comments.yaml", "type: pr-comments\nnumber: 10\ncompletedAt: \""+failedAt.Format(time.RFC3339)+"\"\n"+tc.task)
			}

			s, queue := newTestScanner(t, tempDir, testOpts{
				BotUsers:        []string{"bot1"},
				GitHubLogin:     "bot1",
				TriggerLabel:    "factory",
				AllowlistedBots: []string{"reviewbot"},
			})
			if err := queue.LoadFromDisk(); err != nil {
				t.Fatalf("loading queue: %v", err)
			}

			pr := &githubv39.PullRequest{CreatedAt: &prCreatedAt}
			history := &prHistory{comments: tc.comments, revCommentsMap: map[int64][]*githubv39.PullRequestComment{}}

			got := s.commentRetryState(10, headSHA, pr, history)

			if got.active != tc.wantActive {
				t.Errorf("active = %v, want %v", got.active, tc.wantActive)
			}
			if got.attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got.attempts, tc.wantAttempts)
			}
			if tc.wantActive {
				if want := failedAt.Add(commentRetryCooldown); !got.dueAt.Equal(want) {
					t.Errorf("dueAt = %v, want %v", got.dueAt, want)
				}
				if got.attempt() != tc.wantAttempts+1 {
					t.Errorf("attempt() = %d, want %d", got.attempt(), tc.wantAttempts+1)
				}
			} else if got.attempt() != 1 {
				t.Errorf("attempt() = %d, want 1 when no retry is owed", got.attempt())
			}
		})
	}
}

// TestEvaluate_AddressCommentsRetry drives a full evaluation over a pull request
// whose address-feedback task failed, and asserts on what the next cycle does
// with the feedback that attempt never answered.
//
// The comment carries the watcher's own 'eyes' mark throughout, which is what
// would otherwise make the retry invisible: the assertions are therefore about
// the scanner setting its own bookkeeping aside, not about the comment changing.
func TestEvaluate_AddressCommentsRetry(t *testing.T) {
	tests := []struct {
		name string
		// failedAttempt is the attempt number on the recorded failure.
		failedAttempt int
		// failedAgo is how long ago that attempt finished.
		failedAgo time.Duration
		// reactions are the marks already on the comment.
		reactions []conventions.Reaction
		// wantAttempt is the attempt number expected on the queued task, or
		// zero when nothing should be queued.
		wantAttempt int
	}{
		{
			name:          "retries the same feedback after a failure",
			failedAttempt: 1,
			failedAgo:     commentRetryCooldown + time.Minute,
			reactions:     []conventions.Reaction{conventions.ReactionAcknowledged},
			wantAttempt:   2,
		},
		{
			name:          "carries the count through to the last attempt",
			failedAttempt: 2,
			failedAgo:     commentRetryCooldown + time.Minute,
			reactions:     []conventions.Reaction{conventions.ReactionAcknowledged},
			wantAttempt:   3,
		},
		{
			name:          "waits out the cooldown",
			failedAttempt: 1,
			failedAgo:     time.Minute,
			reactions:     []conventions.Reaction{conventions.ReactionAcknowledged},
		},
		{
			// The third failure is the one that marks the comment, and the mark
			// is what parks it: no fourth attempt is queued.
			name:          "stops once the budget is spent",
			failedAttempt: api.MaxPRCommentAttempts,
			failedAgo:     commentRetryCooldown + time.Minute,
			reactions:     []conventions.Reaction{conventions.ReactionAcknowledged, conventions.ReactionFailed},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const (
				prNum     = 10
				headSHA   = "sha-1234"
				commentID = int64(555)
			)
			completedAt := time.Now().Add(-tc.failedAgo)
			// The attempt was queued before it failed, and the watcher
			// announced it on the pull request in between - which is the bot
			// reply a retry has to see past.
			enqueuedAt := completedAt.Add(-10 * time.Minute)
			announcedAt := enqueuedAt.Add(time.Minute)

			tempDir := t.TempDir()
			writeProcessedTask(t, tempDir, "task-pr-10-comments.yaml", fmt.Sprintf(
				"type: pr-comments\nnumber: %d\nstatus: Failed\ncommitSHA: %s\nattempt: %d\nenqueuedAt: \"%s\"\ncompletedAt: \"%s\"\n",
				prNum, headSHA, tc.failedAttempt, enqueuedAt.Format(time.RFC3339), completedAt.Format(time.RFC3339)))

			srv := newFeedbackServer(t, feedbackServerOpts{
				prNumber:    prNum,
				headSHA:     headSHA,
				commentID:   commentID,
				commentTime: time.Now().Add(-2 * time.Hour),
				commitTime:  time.Now().Add(-3 * time.Hour),
				reactions:   tc.reactions,
				watcher:     "bot1",
				announcedAt: announcedAt,
			})
			defer srv.Close()

			ghClient := githubv39.NewClient(nil)
			ghClient.BaseURL, _ = url.Parse(srv.URL + "/")

			s, queue := newTestScanner(t, tempDir, testOpts{
				GitHub:         ghClient,
				BotUsers:       []string{"bot1"},
				GitHubLogin:    "bot1",
				TriggerLabel:   "factory",
				ReviewerLogins: []string{"reviewbot"},
			})
			if err := queue.LoadFromDisk(); err != nil {
				t.Fatalf("loading queue: %v", err)
			}

			prIssue := &githubv39.Issue{
				Number:    githubv39.Int(prNum),
				Assignees: []*githubv39.User{{Login: stringPtr("bot1")}},
				Labels:    []*githubv39.Label{{Name: stringPtr("factory")}},
			}
			s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

			task := readIncomingTask(t, tempDir, "task-pr-10-comments.yaml")

			if tc.wantAttempt == 0 {
				if task != nil {
					t.Fatalf("queued an address-comments task (attempt %d), want none", task.Attempt)
				}
				if got := srv.acknowledged(); len(got) != 0 {
					t.Errorf("acknowledged comments %v, want none", got)
				}
				return
			}

			if task == nil {
				t.Fatal("no address-comments task was queued")
			}
			if task.Attempt != tc.wantAttempt {
				t.Errorf("task attempt = %d, want %d", task.Attempt, tc.wantAttempt)
			}
			if task.CommitSHA != headSHA {
				t.Errorf("task commitSHA = %q, want %q", task.CommitSHA, headSHA)
			}
			// The retry has to carry the same feedback as the attempt it
			// repeats: the comment the failed attempt acknowledged is the one
			// acknowledged again, and the one named in the trigger notes.
			if got := srv.acknowledged(); len(got) != 1 || got[0] != commentID {
				t.Errorf("acknowledged comments = %v, want [%d]", got, commentID)
			}
			if !strings.Contains(task.TriggerNotes, fmt.Sprintf("ID %d", commentID)) {
				t.Errorf("trigger notes %q do not name comment %d", task.TriggerNotes, commentID)
			}
			if !strings.Contains(task.TriggerNotes, fmt.Sprintf("attempt %d of %d", tc.wantAttempt, api.MaxPRCommentAttempts)) {
				t.Errorf("trigger notes %q do not record the attempt number", task.TriggerNotes)
			}
		})
	}
}

// TestEvaluate_AddressCommentsRetryAcrossCycles runs the sequence the daemon
// actually performs: queue the feedback, fail the task through the queue, and
// evaluate again.
//
// The single-cycle cases above start from a task file on disk, which leaves the
// scanner's in-memory "addressed at" stamp unset. Here the first cycle writes
// that stamp itself, so this is the case that proves a retry gets past the
// watcher's own memory as well as past the marks on the comment.
func TestEvaluate_AddressCommentsRetryAcrossCycles(t *testing.T) {
	const (
		prNum     = 10
		headSHA   = "sha-1234"
		commentID = int64(555)
		filename  = "task-pr-10-comments.yaml"
	)

	tempDir := t.TempDir()
	srv := newFeedbackServer(t, feedbackServerOpts{
		prNumber:    prNum,
		headSHA:     headSHA,
		commentID:   commentID,
		commentTime: time.Now().Add(-2 * time.Hour),
		commitTime:  time.Now().Add(-3 * time.Hour),
		watcher:     "bot1",
	})
	defer srv.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(srv.URL + "/")

	s, queue := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	prIssue := &githubv39.Issue{
		Number:    githubv39.Int(prNum),
		Assignees: []*githubv39.User{{Login: stringPtr("bot1")}},
		Labels:    []*githubv39.Label{{Name: stringPtr("factory")}},
	}

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	first := readIncomingTask(t, tempDir, filename)
	if first == nil {
		t.Fatal("first cycle queued no address-comments task")
	}
	if first.Attempt != 1 {
		t.Fatalf("first task attempt = %d, want 1", first.Attempt)
	}

	// Fail it as the dispatcher would, dated far enough back that the cooldown
	// has elapsed, and leave behind the comment the watcher posts when it
	// starts a task. The queue only stamps a completion time when the task does
	// not already carry one, which is what lets the clock be set here.
	first.EnqueuedAt = time.Now().Add(-commentRetryCooldown - 10*time.Minute)
	first.StartedAt = first.EnqueuedAt.Add(time.Minute)
	first.CompletedAt = time.Now().Add(-commentRetryCooldown - time.Minute)
	srv.announceTaskStarted(first.StartedAt)
	if err := queue.FailTask(filename, first, "sandbox died"); err != nil {
		t.Fatalf("failing task: %v", err)
	}

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	second := readIncomingTask(t, tempDir, filename)
	if second == nil {
		t.Fatal("second cycle queued no address-comments task after the failure")
	}
	if second.Attempt != 2 {
		t.Errorf("second task attempt = %d, want 2", second.Attempt)
	}
	// Both attempts were queued for the same comment, which is what makes the
	// second one a retry rather than a new piece of work.
	if got := srv.acknowledged(); len(got) != 2 || got[0] != commentID || got[1] != commentID {
		t.Errorf("acknowledged comments = %v, want [%d %d]", got, commentID, commentID)
	}
}

// feedbackServerOpts describes the pull request a feedbackServer serves: one
// commit, and one human comment left after it.
type feedbackServerOpts struct {
	prNumber    int
	headSHA     string
	commentID   int64
	commentTime time.Time
	commitTime  time.Time
	// reactions are the marks the watcher has already left on the comment.
	reactions []conventions.Reaction
	// watcher is the account those marks are attributed to.
	watcher string
	// announcedAt, when set, seeds the "started addressing review feedback"
	// comment the watcher posts for every task it dispatches. A fixture with a
	// failed attempt behind it has one, and it is newer than the feedback. It
	// is seeded in the untagged wording, which is the harder case: a thread
	// that predates the announcement marker.
	announcedAt time.Time
}

// feedbackServer is a GitHub stand-in that records the acknowledgements the
// scanner writes, which is how a test tells which comments a task was queued
// for.
//
// The reactions and comments it serves include the ones it has been sent. A
// retry has to get past everything the attempt before it left behind, so a fake
// that forgot any of it would make the second cycle easier than the real one.
type feedbackServer struct {
	*httptest.Server

	mu        sync.Mutex
	acks      []int64
	reactions []conventions.Reaction
	comments  []*githubv39.IssueComment
	watcher   string
}

// acknowledged returns the comment IDs the scanner marked as picked up.
func (s *feedbackServer) acknowledged() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.acks...)
}

// announceTaskStarted posts the comment the watcher makes when it dispatches an
// address-feedback task, as the dispatcher's NotifyTaskStarted would.
func (s *feedbackServer) announceTaskStarted(at time.Time) {
	s.addComment(at, conventions.Announce(taskStartedBody))
}

// announceTaskStartedLegacy posts the same announcement as it was worded before
// announcements carried a marker, which is what the threads of pull requests
// that predate the marker still hold.
func (s *feedbackServer) announceTaskStartedLegacy(at time.Time) {
	s.addComment(at, taskStartedBody)
}

// taskStartedBody mirrors the watcher's own announcement for an
// address-feedback task. It is duplicated rather than imported because the
// package that posts it imports this one.
const taskStartedBody = "🤖 AI Factory started addressing review feedback for this pull request."

func (s *feedbackServer) addComment(at time.Time, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, &githubv39.IssueComment{
		ID:        githubv39.Int64(int64(900 + len(s.comments))),
		User:      &githubv39.User{Login: stringPtr(s.watcher)},
		Body:      stringPtr(body),
		CreatedAt: timePtr(at),
	})
}

func newFeedbackServer(t *testing.T, opts feedbackServerOpts) *feedbackServer {
	t.Helper()

	fs := &feedbackServer{
		reactions: opts.reactions,
		watcher:   opts.watcher,
		comments: []*githubv39.IssueComment{{
			ID:        githubv39.Int64(opts.commentID),
			User:      &githubv39.User{Login: stringPtr("reviewer-human")},
			Body:      stringPtr("please rename this"),
			CreatedAt: timePtr(opts.commentTime),
		}},
	}
	if !opts.announcedAt.IsZero() {
		fs.announceTaskStartedLegacy(opts.announcedAt)
	}
	reactionsPath := fmt.Sprintf("/repos/test-owner/test-repo/issues/comments/%d/reactions", opts.commentID)

	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d", opts.prNumber):
			_ = json.NewEncoder(w).Encode(&githubv39.PullRequest{
				Number:    githubv39.Int(opts.prNumber),
				Mergeable: githubv39.Bool(true),
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(opts.headSHA)},
				CreatedAt: timePtr(opts.commitTime.Add(-time.Hour)),
			})
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/commits", opts.prNumber):
			_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{{
				SHA:    stringPtr(opts.headSHA),
				Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: timePtr(opts.commitTime)}},
			}})
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/test-owner/test-repo/issues/%d/comments", opts.prNumber):
			fs.mu.Lock()
			comments := append([]*githubv39.IssueComment(nil), fs.comments...)
			fs.mu.Unlock()
			_ = json.NewEncoder(w).Encode(comments)
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/test-owner/test-repo/pulls/%d/reviews", opts.prNumber):
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == http.MethodGet && r.URL.Path == reactionsPath:
			fs.mu.Lock()
			var reactions []*githubv39.Reaction
			for _, content := range fs.reactions {
				reactions = append(reactions, &githubv39.Reaction{
					Content: stringPtr(string(content)),
					User:    &githubv39.User{Login: stringPtr(fs.watcher)},
				})
			}
			fs.mu.Unlock()
			_ = json.NewEncoder(w).Encode(reactions)
		case r.Method == http.MethodPost && r.URL.Path == reactionsPath:
			var posted struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&posted)
			fs.mu.Lock()
			fs.acks = append(fs.acks, opts.commentID)
			fs.reactions = append(fs.reactions, conventions.Reaction(posted.Content))
			fs.mu.Unlock()
			_ = json.NewEncoder(w).Encode(&githubv39.Reaction{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/test-owner/test-repo/commits/"+opts.headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/test-owner/test-repo/commits/"+opts.headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return fs
}

// writeProcessedTask drops a finished task into the queue's processed directory,
// which is where the scanner recovers what has already been tried.
func writeProcessedTask(t *testing.T, tempDir, filename, contents string) {
	t.Helper()
	dir := filepath.Join(tempDir, "processed")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("creating processed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(contents), 0644); err != nil {
		t.Fatalf("writing processed task: %v", err)
	}
}

// readIncomingTask returns the queued task of the given name, or nil when none
// was queued.
func readIncomingTask(t *testing.T, tempDir, filename string) *api.QueueTask {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tempDir, "incoming", filename))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading queued task: %v", err)
	}
	var task api.QueueTask
	if err := yaml.Unmarshal(b, &task); err != nil {
		t.Fatalf("parsing queued task: %v", err)
	}
	return &task
}
