package prs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

func TestPRCommentsTriggerMetadata(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	prNum := 10
	mergeable := true
	headSHA := "sha-1234"

	commitTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	comment1Time := time.Date(2026, 8, 1, 12, 10, 0, 0, time.UTC)
	comment2Time := time.Date(2026, 8, 1, 12, 25, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			pr := &githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				CreatedAt: &commitTime,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &commitTime},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			comments := []*githubv39.IssueComment{
				{
					ID:                int64Ptr(1001),
					User:              &githubv39.User{Login: stringPtr("reviewer-bob")},
					AuthorAssociation: stringPtr("MEMBER"),
					Body:              stringPtr("Please add a test."),
					CreatedAt:         &comment2Time,
				},
				{
					ID:                int64Ptr(1000),
					User:              &githubv39.User{Login: stringPtr("reviewer-alice")},
					AuthorAssociation: stringPtr("MEMBER"),
					Body:              stringPtr("Please rename this function."),
					CreatedAt:         &comment1Time,
				},
			}
			_ = json.NewEncoder(w).Encode(comments)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/reactions"):
			_ = json.NewEncoder(w).Encode(map[string]string{"content": "eyes"})
		default:
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	u, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = u

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "",
		TriggerLabel: "factory",
	})

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	s.evaluateAll(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-comments.yaml to be created: %v", err)
	}

	var task api.QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(comment1Time) {
		t.Errorf("expected triggerEventTime %v (oldest comment), got %v", comment1Time, task.TriggerEventTime)
	}
	if task.TriggerReason != api.TriggerReasonPRCommentsAdded {
		t.Errorf("expected triggerReason %s, got %s", api.TriggerReasonPRCommentsAdded, task.TriggerReason)
	}
	if !strings.Contains(task.TriggerNotes, "reviewer-alice") || !strings.Contains(task.TriggerNotes, "1000") {
		t.Errorf("unexpected triggerNotes: %s", task.TriggerNotes)
	}
}

func TestPRInvestigateTriggerMetadata(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	prNum := 10
	mergeable := true
	headSHA := "sha-5678"

	commitTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fail1Time := time.Date(2026, 8, 1, 12, 15, 0, 0, time.UTC)
	fail2Time := time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			pr := &githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				CreatedAt: &commitTime,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &commitTime},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			checkRuns := []*githubv39.CheckRun{
				{
					Name:        stringPtr("e2e-tests"),
					Status:      stringPtr("completed"),
					Conclusion:  stringPtr("failure"),
					CompletedAt: &githubv39.Timestamp{Time: fail2Time},
				},
				{
					Name:        stringPtr("lint-go"),
					Status:      stringPtr("completed"),
					Conclusion:  stringPtr("failure"),
					CompletedAt: &githubv39.Timestamp{Time: fail1Time},
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": checkRuns})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		default:
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	u, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = u

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "",
		TriggerLabel: "factory",
	})

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	s.evaluateAll(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-investigate.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-investigate.yaml to be created: %v", err)
	}

	var task api.QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(fail1Time) {
		t.Errorf("expected triggerEventTime %v (earliest failure), got %v", fail1Time, task.TriggerEventTime)
	}
	if task.TriggerReason != api.TriggerReasonPRCheckFailed {
		t.Errorf("expected triggerReason %s, got %s", api.TriggerReasonPRCheckFailed, task.TriggerReason)
	}
	if !strings.Contains(task.TriggerNotes, "lint-go") || !strings.Contains(task.TriggerNotes, "2 failed check(s)") {
		t.Errorf("unexpected triggerNotes: %s", task.TriggerNotes)
	}
}

func TestPRIterateTriggerMetadata(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	prNum := 10
	mergeable := false // Conflicting
	headSHA := "sha-conflict"

	commitTime := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	updateTime := time.Date(2026, 8, 1, 14, 5, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			pr := &githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				Base:      &githubv39.PullRequestBranch{Ref: stringPtr("main")},
				CreatedAt: &commitTime,
				UpdatedAt: &updateTime,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &commitTime},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		default:
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	u, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = u

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "",
		TriggerLabel: "factory",
	})

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	s.evaluateAll(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-iterate.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-iterate.yaml to be created: %v", err)
	}

	var task api.QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(commitTime) {
		t.Errorf("expected triggerEventTime %v (commit time), got %v", commitTime, task.TriggerEventTime)
	}
	if task.TriggerReason != api.TriggerReasonPRMergeConflict {
		t.Errorf("expected triggerReason %s, got %s", api.TriggerReasonPRMergeConflict, task.TriggerReason)
	}
	if !strings.Contains(task.TriggerNotes, "merge conflicts with base branch 'main'") {
		t.Errorf("unexpected triggerNotes: %s", task.TriggerNotes)
	}
	// conflict's policy.revisit every-scan: the evaluation is not recorded,
	// so the next fast pass looks at the pull request again.
	if got := s.state.get(prNum).lastEvaluatedUpdatedAt; !got.IsZero() {
		t.Errorf("the conflicted PR's evaluation was recorded at %v; want it looked at every scan", got)
	}
}

// TestPRCommentsAcknowledgesReviews covers the review feedback of an
// address-comments task: queueing it times it from the oldest feedback -
// the cutoff the acknowledgement and the resolver rely on - and reacts to
// nothing; when it starts (NotifyTaskStarted), the review body is
// acknowledged through GraphQL and the inline comment through REST. An
// inline comment is timed from its review's submission, not its draft.
func TestPRCommentsAcknowledgesReviews(t *testing.T) {
	tempDir := t.TempDir()
	prNum := 10
	mergeable := true
	headSHA := "sha-1234"
	commitTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	reviewTime := time.Date(2026, 8, 1, 12, 10, 0, 0, time.UTC)
	inlineTime := time.Date(2026, 8, 1, 12, 5, 0, 0, time.UTC)

	var reviewAcks []map[string]interface{}
	var inlineAckPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			_ = json.NewEncoder(w).Encode(&githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				CreatedAt: &commitTime,
			})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{{
				SHA:    stringPtr(headSHA),
				Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: &commitTime}},
			}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{{
				ID:                int64Ptr(500),
				NodeID:            stringPtr("PRR_500"),
				User:              &githubv39.User{Login: stringPtr("reviewer-alice")},
				AuthorAssociation: stringPtr("MEMBER"),
				State:             stringPtr("CHANGES_REQUESTED"),
				Body:              stringPtr("A few things to fix."),
				SubmittedAt:       &reviewTime,
			}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{{
				ID:                  int64Ptr(2001),
				PullRequestReviewID: int64Ptr(500),
				User:                &githubv39.User{Login: stringPtr("reviewer-alice")},
				AuthorAssociation:   stringPtr("MEMBER"),
				Body:                stringPtr("Rename this."),
				CreatedAt:           &inlineTime,
			}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var req struct {
				Query     string                 `json:"query"`
				Variables map[string]interface{} `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if strings.Contains(req.Query, "addReaction") {
				reviewAcks = append(reviewAcks, req.Variables)
				_, _ = w.Write([]byte(`{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"node":{"reactions":{"nodes":[]}}}}`))
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/repos/test-owner/test-repo/pulls/comments/"):
			inlineAckPaths = append(inlineAckPaths, r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]string{"content": "eyes"})
		default:
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         newTestKubeClient(),
		BotUsers:     []string{"bot1"},
		TriggerLabel: "factory",
	})
	s.evaluateAll(context.Background(), []*githubv39.Issue{{
		Number:           &prNum,
		PullRequestLinks: &githubv39.PullRequestLinks{},
	}})

	data, err := os.ReadFile(filepath.Join(tempDir, "incoming", "task-pr-10-comments.yaml"))
	if err != nil {
		t.Fatalf("expected task-pr-10-comments.yaml to be created: %v", err)
	}
	var task api.QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}
	// The inline comment was drafted at 12:05 but only became visible when
	// its review was submitted at 12:10, so that is when it is timed from.
	if !task.TriggerEventTime.Equal(reviewTime) {
		t.Errorf("triggerEventTime = %v, want %v (review submission)", task.TriggerEventTime, reviewTime)
	}

	// Queued, nothing is acknowledged: a task dropped before it starts
	// leaves the feedback for the next scan.
	if len(reviewAcks) != 0 || len(inlineAckPaths) != 0 {
		t.Fatalf("queueing acknowledged feedback: reviews %v, inline %v", reviewAcks, inlineAckPaths)
	}
	// It starts: the feedback since its trigger is acknowledged.
	conventions.AcknowledgeCommentReactions(context.Background(), s.gh, conventions.ResolveOptions{
		PRNumber: prNum, SelfLogin: "watcher", Since: task.TriggerEventTime, TriggerLabel: "factory",
	})
	if len(reviewAcks) != 1 || reviewAcks[0]["subjectId"] != "PRR_500" || reviewAcks[0]["content"] != "EYES" {
		t.Errorf("review acknowledgements = %v, want one EYES on PRR_500", reviewAcks)
	}
	if want := "/repos/test-owner/test-repo/pulls/comments/2001/reactions"; len(inlineAckPaths) != 1 || inlineAckPaths[0] != want {
		t.Errorf("inline acknowledgements = %v, want [%s]", inlineAckPaths, want)
	}
}
