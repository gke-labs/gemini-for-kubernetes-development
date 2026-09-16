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

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
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
					ID:        int64Ptr(1001),
					User:      &githubv39.User{Login: stringPtr("reviewer-bob")},
					CreatedAt: &comment2Time,
				},
				{
					ID:        int64Ptr(1000),
					User:      &githubv39.User{Login: stringPtr("reviewer-alice")},
					CreatedAt: &comment1Time,
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
}
