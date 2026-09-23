package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// TestWatcherTaskCoordinator_NotifyTaskFinished covers what a finished
// address-feedback task leaves on the comments it was queued for.
//
// The distinction that matters is between a failure with attempts left and the
// last one. Reactions here can only be added, so a 'confused' written between
// attempts would be a permanent statement that the watcher gave up on feedback
// it is in fact about to try again.
func TestWatcherTaskCoordinator_NotifyTaskFinished(t *testing.T) {
	const commentID = int64(777)

	tests := []struct {
		name          string
		attempt       int
		taskErr       error
		wantReactions []string
		wantComment   bool
	}{
		{
			name:          "a successful task resolves the comment",
			attempt:       1,
			wantReactions: []string{string(conventions.ReactionResolved)},
		},
		{
			name:    "a failure with attempts left says nothing",
			attempt: 1,
			taskErr: errors.New("sandbox died"),
		},
		{
			name:    "the attempt before the last still says nothing",
			attempt: api.MaxPRCommentAttempts - 1,
			taskErr: errors.New("sandbox died"),
		},
		{
			name:          "the final failure marks the comment and explains",
			attempt:       api.MaxPRCommentAttempts,
			taskErr:       errors.New("sandbox died"),
			wantReactions: []string{string(conventions.ReactionFailed)},
			wantComment:   true,
		},
		{
			// A task queued before attempts were counted keeps the behaviour it
			// was queued under exactly: the mark, and no give-up notice for a
			// budget it never had.
			name:          "a failure with no attempt recorded is final",
			attempt:       0,
			taskErr:       errors.New("sandbox died"),
			wantReactions: []string{string(conventions.ReactionFailed)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu        sync.Mutex
				reactions []string
				comments  []string
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				reactionsPath := fmt.Sprintf("/repos/test-owner/test-repo/issues/comments/%d/reactions", commentID)
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
					_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{{
						ID:   githubv39.Int64(commentID),
						User: &githubv39.User{Login: githubv39.String("reviewer-human")},
						Body: githubv39.String("please rename this"),
					}})
				case r.Method == http.MethodGet && r.URL.Path == reactionsPath:
					// The comment was acknowledged when the task was queued,
					// which is what makes it this task's to close out.
					_ = json.NewEncoder(w).Encode([]*githubv39.Reaction{{
						Content: githubv39.String(string(conventions.ReactionAcknowledged)),
						User:    &githubv39.User{Login: githubv39.String("watcher-bot")},
					}})
				case r.Method == http.MethodPost && r.URL.Path == reactionsPath:
					var posted struct {
						Content string `json:"content"`
					}
					_ = json.NewDecoder(r.Body).Decode(&posted)
					mu.Lock()
					reactions = append(reactions, posted.Content)
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(&githubv39.Reaction{})
				case r.Method == http.MethodPost && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
					var posted githubv39.IssueComment
					_ = json.NewDecoder(r.Body).Decode(&posted)
					mu.Lock()
					comments = append(comments, posted.GetBody())
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(&githubv39.IssueComment{})
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := githubv39.NewClient(nil)
			ghClient.BaseURL, _ = url.Parse(server.URL + "/")

			w := newTestWatcher(t, t.TempDir())
			w.cfg = &config.FactoryConfig{}
			w.githubLogin = "watcher-bot"
			w.repoClient = github.ForRepo(ghClient, "test-owner", "test-repo")

			coordinator := &watcherTaskCoordinator{w: w}
			coordinator.NotifyTaskFinished(context.Background(), &api.QueueTask{
				Type:      api.TypePRComments,
				Number:    10,
				CommitSHA: "sha-1234567890",
				Attempt:   tc.attempt,
			}, tc.taskErr)

			mu.Lock()
			defer mu.Unlock()

			if len(reactions) != len(tc.wantReactions) {
				t.Fatalf("reactions = %v, want %v", reactions, tc.wantReactions)
			}
			for i, want := range tc.wantReactions {
				if reactions[i] != want {
					t.Errorf("reaction %d = %q, want %q", i, reactions[i], want)
				}
			}

			if !tc.wantComment {
				if len(comments) != 0 {
					t.Errorf("posted %v, want no comment", comments)
				}
				return
			}
			if len(comments) != 1 {
				t.Fatalf("posted %d comments, want 1", len(comments))
			}
			// The give-up notice names the revision it applies to and the way
			// back in, and deliberately does not mention a stop label: the rest
			// of the automation on this pull request keeps running.
			if !strings.Contains(comments[0], "sha-123") {
				t.Errorf("give-up comment %q does not name the revision", comments[0])
			}
			if strings.Contains(comments[0], "stop") {
				t.Errorf("give-up comment %q mentions a stop label", comments[0])
			}
		})
	}
}
