package prs

import (
	"context"
	"encoding/json"
	"io"
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
)

// ReviewMode gates automated reviews: enabled queues one for a green PR
// carrying the review label; disabled and dryrun queue none, and readiness
// does not wait for a review that will never come.
func TestEvaluate_ReviewMode(t *testing.T) {
	for _, tc := range []struct {
		mode        string
		wantReview  bool
		wantReadied bool
	}{
		{"enabled", true, false},
		{"", true, false},
		{"disabled", false, true},
		{"dryrun", false, true},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			tempDir := t.TempDir()
			for _, d := range []string{"incoming", "processing", "processed"} {
				_ = os.MkdirAll(filepath.Join(tempDir, d), 0o755)
			}
			prNum, mergeable, headSHA, now := 10, true, "sha-1234", time.Now()
			var mu sync.Mutex
			var addedLabels []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
					_ = json.NewEncoder(w).Encode(&githubv39.PullRequest{
						Number: &prNum, Mergeable: &mergeable, State: stringPtr("open"),
						User: &githubv39.User{Login: stringPtr("bot1")},
						Head: &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)}, CreatedAt: &now,
					})
				case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
					_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{{
						SHA: stringPtr(headSHA), Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: &now}},
					}})
				case r.Method == "GET" && (strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/reviews") || strings.HasSuffix(r.URL.Path, "/statuses")):
					_, _ = w.Write([]byte("[]"))
				case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []*githubv39.CheckRun{{
						ID: githubv39.Int64(1), Name: stringPtr("tests"), Status: stringPtr("completed"), Conclusion: stringPtr("success"),
					}}})
				case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					addedLabels = append(addedLabels, string(body))
					mu.Unlock()
					_, _ = w.Write([]byte("[]"))
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()
			ghClient := githubv39.NewClient(nil)
			ghClient.BaseURL, _ = url.Parse(server.URL + "/")

			s, _ := newTestScanner(t, tempDir, testOpts{
				GitHub: ghClient, BotUsers: []string{"bot1"}, GitHubLogin: "bot1",
				TriggerLabel: "factory", ReviewerLogins: []string{"reviewbot"},
			})
			s.cfg.ReviewMode = tc.mode
			prIssue := &githubv39.Issue{
				Number:    &prNum,
				Assignees: []*githubv39.User{{Login: stringPtr("bot1")}},
				Labels:    []*githubv39.Label{{Name: stringPtr("factory")}, {Name: stringPtr("factory/review")}},
			}
			s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

			_, err := os.Stat(filepath.Join(tempDir, "incoming", "task-pr-10-review.yaml"))
			if gotReview := err == nil; gotReview != tc.wantReview {
				t.Errorf("review task queued = %v, want %v", gotReview, tc.wantReview)
			}
			mu.Lock()
			readied := strings.Contains(strings.Join(addedLabels, " "), "ready-for-human")
			mu.Unlock()
			if readied != tc.wantReadied {
				t.Errorf("ready-for-human added = %v (labels %v), want %v", readied, addedLabels, tc.wantReadied)
			}
		})
	}
}
