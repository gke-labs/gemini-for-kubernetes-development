package watch

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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

func TestGetIssueTriggerInfo(t *testing.T) {
	num := 42
	t1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 1, 11, 30, 0, 0, time.UTC)

	t.Run("Issue created with label (no later timeline label event)", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &t1,
			Labels: []*githubv39.Label{
				{Name: stringPtr("factory")},
			},
		}

		eventTime, reason, notes := getIssueTriggerInfo(issue, nil, "factory", false)
		if !eventTime.Equal(t1) {
			t.Errorf("expected eventTime %v, got %v", t1, eventTime)
		}
		if !strings.Contains(reason, "Issue #42 created with label 'factory'") {
			t.Errorf("unexpected reason: %s", reason)
		}
		if !strings.Contains(notes, "Issue #42 created at 2026-08-01T10:00:00Z with trigger label 'factory'") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})

	t.Run("Issue labeled later by user", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &t1,
			Labels: []*githubv39.Label{
				{Name: stringPtr("factory")},
			},
		}
		timeline := []*githubv39.Timeline{
			{
				Event:     stringPtr("labeled"),
				CreatedAt: &t2,
				Label:     &githubv39.Label{Name: stringPtr("factory")},
				Actor:     &githubv39.User{Login: stringPtr("alice")},
			},
		}

		eventTime, reason, notes := getIssueTriggerInfo(issue, timeline, "factory", false)
		if !eventTime.Equal(t2) {
			t.Errorf("expected eventTime %v, got %v", t2, eventTime)
		}
		if !strings.Contains(reason, "Trigger label 'factory' added to issue #42 by alice") {
			t.Errorf("unexpected reason: %s", reason)
		}
		if !strings.Contains(notes, "trigger label 'factory' added by alice at 2026-08-01T11:30:00Z") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})

	t.Run("Issue auto-labeled by watcher", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &t1,
			User:      &githubv39.User{Login: stringPtr("bob")},
		}

		eventTime, reason, notes := getIssueTriggerInfo(issue, nil, "factory", true)
		if !eventTime.Equal(t1) {
			t.Errorf("expected eventTime %v, got %v", t1, eventTime)
		}
		if !strings.Contains(reason, "Issue #42 created by bob") {
			t.Errorf("unexpected reason: %s", reason)
		}
		if !strings.Contains(notes, "auto-applied by watcher") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})
}

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

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-ns",
		},
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		processedPRs:  make(map[int]prWatchState),
		allBotUsers:   []string{"bot1"},
		triggerLabel:  "factory",
		ghClient:      ghClient,
	}

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	w.processPRs(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-comments.yaml to be created: %v", err)
	}

	var task QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(comment1Time) {
		t.Errorf("expected triggerEventTime %v (oldest comment), got %v", comment1Time, task.TriggerEventTime)
	}
	if !strings.Contains(task.TriggerReason, "PR review comments (2 new)") {
		t.Errorf("unexpected triggerReason: %s", task.TriggerReason)
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

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-ns",
		},
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		processedPRs:  make(map[int]prWatchState),
		allBotUsers:   []string{"bot1"},
		triggerLabel:  "factory",
		ghClient:      ghClient,
	}

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	w.processPRs(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-investigate.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-investigate.yaml to be created: %v", err)
	}

	var task QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(fail1Time) {
		t.Errorf("expected triggerEventTime %v (earliest failure), got %v", fail1Time, task.TriggerEventTime)
	}
	if !strings.Contains(task.TriggerReason, "lint-go") || !strings.Contains(task.TriggerReason, "failure") {
		t.Errorf("unexpected triggerReason: %s", task.TriggerReason)
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

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-ns",
		},
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		processedPRs:  make(map[int]prWatchState),
		allBotUsers:   []string{"bot1"},
		triggerLabel:  "factory",
		ghClient:      ghClient,
	}

	prIssues := []*githubv39.Issue{
		{
			Number:           &prNum,
			PullRequestLinks: &githubv39.PullRequestLinks{},
		},
	}

	w.processPRs(context.Background(), prIssues)

	taskFile := filepath.Join(incomingDir, "task-pr-10-iterate.yaml")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("expected task-pr-10-iterate.yaml to be created: %v", err)
	}

	var task QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	if !task.TriggerEventTime.Equal(commitTime) {
		t.Errorf("expected triggerEventTime %v (commit time), got %v", commitTime, task.TriggerEventTime)
	}
	if !strings.Contains(task.TriggerReason, "merge conflicts detected against main") {
		t.Errorf("unexpected triggerReason: %s", task.TriggerReason)
	}
	if !strings.Contains(task.TriggerNotes, "merge conflicts with base branch 'main'") {
		t.Errorf("unexpected triggerNotes: %s", task.TriggerNotes)
	}
}

func TestBuildQueueResponseTriggerFields(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	_ = os.MkdirAll(incomingDir, 0755)

	eventTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	enqueuedTime := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)

	task := &QueueTask{
		Type:             "pr-comments",
		URL:              "https://github.com/owner/repo/pull/123",
		Number:           123,
		Priority:         "medium",
		Phase:            2,
		CreatedAt:        eventTime,
		EnqueuedAt:       enqueuedTime,
		TriggerEventTime: eventTime,
		TriggerReason:    "PR review comments (1 new)",
		TriggerNotes:     "Oldest comment by alice added at 2026-08-01T10:00:00Z",
		Status:           "Pending",
	}

	_ = writeTaskAtomically(incomingDir, "task-pr-123-comments.yaml", task)

	resp := buildQueueResponse(tempDir)
	if len(resp.Incoming) != 1 {
		t.Fatalf("expected 1 incoming task, got %d", len(resp.Incoming))
	}

	item := resp.Incoming[0]
	if item.TriggerEventTime != eventTime.Format(time.RFC3339) {
		t.Errorf("expected item TriggerEventTime %s, got %s", eventTime.Format(time.RFC3339), item.TriggerEventTime)
	}
	if item.TriggerReason != "PR review comments (1 new)" {
		t.Errorf("expected item TriggerReason 'PR review comments (1 new)', got '%s'", item.TriggerReason)
	}
	if item.TriggerNotes != "Oldest comment by alice added at 2026-08-01T10:00:00Z" {
		t.Errorf("expected item TriggerNotes 'Oldest comment by alice added at 2026-08-01T10:00:00Z', got '%s'", item.TriggerNotes)
	}
}

func TestBuildQueueResponseStartedCompletedDuration(t *testing.T) {
	tempDir := t.TempDir()
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	startTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 8, 1, 10, 5, 30, 0, time.UTC)

	processingTask := &QueueTask{
		Type:      "pr-review",
		URL:       "https://github.com/owner/repo/pull/1",
		Number:    1,
		Status:    "Running",
		StartedAt: startTime,
		Priority:  "high",
		Phase:     2,
	}
	_ = writeTaskAtomically(processingDir, "task-pr-1-review.yaml", processingTask)

	completedTask := &QueueTask{
		Type:        "issue-fix",
		URL:         "https://github.com/owner/repo/issues/2",
		Number:      2,
		Status:      "Completed",
		StartedAt:   startTime,
		CompletedAt: endTime,
		Priority:    "medium",
		Phase:       3,
	}
	_ = writeTaskAtomically(processedDir, "task-issue-2-fix.yaml", completedTask)

	resp := buildQueueResponse(tempDir)
	if len(resp.Processing) != 1 {
		t.Fatalf("expected 1 processing task, got %d", len(resp.Processing))
	}
	if resp.Processing[0].StartedAt != startTime.Format(time.RFC3339) {
		t.Errorf("expected processing startedAt %s, got %s", startTime.Format(time.RFC3339), resp.Processing[0].StartedAt)
	}

	if len(resp.Processed) != 1 {
		t.Fatalf("expected 1 processed task, got %d", len(resp.Processed))
	}
	p := resp.Processed[0]
	if p.StartedAt != startTime.Format(time.RFC3339) {
		t.Errorf("expected processed startedAt %s, got %s", startTime.Format(time.RFC3339), p.StartedAt)
	}
	if p.CompletedAt != endTime.Format(time.RFC3339) {
		t.Errorf("expected processed completedAt %s, got %s", endTime.Format(time.RFC3339), p.CompletedAt)
	}
	expectedDuration := float64(330) // 5 minutes 30 seconds
	if p.DurationSeconds != expectedDuration {
		t.Errorf("expected durationSeconds %v, got %v", expectedDuration, p.DurationSeconds)
	}
}
