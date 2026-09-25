package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

// TestEvaluate_Filters covers the two reasons a candidate is dropped before any
// GitHub round trip is made for it: it predates the configured minimum, or it
// carries the stop label - in which case its pending work is withdrawn too.
func TestEvaluate_Filters(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")

	s, queue := newTestScanner(t, tempDir, testOpts{
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
		MinNumber:    10,
	})

	// A pending task for the stopped pull request, which must be withdrawn.
	stopTaskFile := filepath.Join(incomingDir, "task-pr-20-iterate.yaml")
	if err := os.WriteFile(stopTaskFile, []byte("type: pr-iterate\n"), 0644); err != nil {
		t.Fatalf("writing task file: %v", err)
	}
	_ = queue.LoadFromDisk()

	lowPRNum := 5
	stopPRNum := 20
	prIssues := []*githubv39.Issue{
		{Number: &lowPRNum},
		{
			Number: &stopPRNum,
			Labels: []*githubv39.Label{{Name: stringPtr("overseer/stop")}},
		},
	}

	s.evaluateAll(context.Background(), prIssues)

	if _, err := os.Stat(stopTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected stop PR task file to be removed, but it still exists")
	}
}

func TestEvaluate_ReadyForHuman_GatedByActiveTask(t *testing.T) {
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
	now := time.Now()

	var addedLabels []string
	var unassignCalls []string

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
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			reviews := []*githubv39.PullRequestReview{
				{
					User:        &githubv39.User{Login: stringPtr("reviewbot")},
					CommitID:    stringPtr(headSHA),
					State:       stringPtr("APPROVED"),
					SubmittedAt: &now,
				},
			}
			_ = json.NewEncoder(w).Encode(reviews)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			bodyBytes, _ := io.ReadAll(r.Body)
			unassignCalls = append(unassignCalls, strings.TrimSpace(string(bodyBytes)))
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// 1. When a task is pending in incomingDir, PR must NOT be marked ready for human
	taskPath := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	_ = os.WriteFile(taskPath, []byte("type: pr-comments\n"), 0644)

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while task is in incomingDir, got %d (%v)", len(addedLabels), addedLabels)
	}
	if len(unassignCalls) != 0 {
		t.Errorf("expected 0 unassign calls while task is in incomingDir, got %d (%v)", len(unassignCalls), unassignCalls)
	}

	// 2. When the task moves to processingDir, PR must still NOT be marked ready for human
	_ = os.Remove(taskPath)
	processingTaskPath := filepath.Join(processingDir, "task-pr-10-comments.yaml")
	_ = os.WriteFile(processingTaskPath, []byte("type: pr-comments\n"), 0644)

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while task is in processingDir, got %d (%v)", len(addedLabels), addedLabels)
	}
	if len(unassignCalls) != 0 {
		t.Errorf("expected 0 unassign calls while task is in processingDir, got %d (%v)", len(unassignCalls), unassignCalls)
	}

	// 3. When the task is completed and moved to processedDir, PR SHOULD be marked ready for human
	_ = os.Remove(processingTaskPath)
	processedTaskPath := filepath.Join(processedDir, "task-pr-10-comments.yaml")
	_ = os.WriteFile(processedTaskPath, []byte("type: pr-comments\nstatus: Completed\n"), 0644)

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after task completion, got %v", addedLabels)
	}
	if len(unassignCalls) != 1 {
		t.Errorf("expected 1 unassign call after task completion, got %d (%v)", len(unassignCalls), unassignCalls)
	}
}

func TestEvaluate_UnassignOnReadyForHuman(t *testing.T) {
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
	now := time.Now()

	var unassignCalls []string
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
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			reviews := []*githubv39.PullRequestReview{
				{
					User:        &githubv39.User{Login: stringPtr("reviewbot")},
					CommitID:    stringPtr(headSHA),
					State:       stringPtr("APPROVED"),
					SubmittedAt: &now,
				},
			}
			_ = json.NewEncoder(w).Encode(reviews)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			bodyBytes, _ := io.ReadAll(r.Body)
			unassignCalls = append(unassignCalls, strings.TrimSpace(string(bodyBytes)))
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if len(unassignCalls) != 1 {
		t.Fatalf("expected 1 unassign API call, got %d (%v)", len(unassignCalls), unassignCalls)
	}
	if !strings.Contains(unassignCalls[0], "bot1") {
		t.Errorf("expected unassign call body to contain 'bot1', got %s", unassignCalls[0])
	}
}

func TestEvaluate_ReadyForHuman_GatedByPendingCheckRuns(t *testing.T) {
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
	now := time.Now()

	var addedLabels []string
	var removedLabels []string
	checkRunStatus := "in_progress"
	checkRunConclusion := ""

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
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			runs := []*githubv39.CheckRun{
				{
					ID:         githubv39.Int64(1),
					Name:       stringPtr("tests"),
					Status:     stringPtr(checkRunStatus),
					Conclusion: stringPtr(checkRunConclusion),
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": runs})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/10/labels/"):
			label := strings.TrimPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/10/labels/")
			removedLabels = append(removedLabels, label)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// 1. Check runs are in_progress -> label must NOT be added
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while check runs are in_progress, got %v", addedLabels)
	}

	// 2. Check runs complete successfully -> label SHOULD be added
	checkRunStatus = "completed"
	checkRunConclusion = "success"
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after check runs completed, got %v", addedLabels)
	}

	// 3. New commit or check run becomes queued/in_progress on PR with label -> label must NOT be removed
	prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: stringPtr("factory/ready-for-human")})
	checkRunStatus = "queued"
	checkRunConclusion = ""
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(removedLabels) != 0 {
		t.Errorf("expected ready-for-human label NOT to be removed when checks become queued, got %v", removedLabels)
	}
}

func TestEvaluate_ReadyForHuman_GatedByPendingCommitStatus(t *testing.T) {
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
	now := time.Now()

	var addedLabels []string
	commitState := "pending"

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
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			statuses := []*githubv39.RepoStatus{
				{
					Context: stringPtr("ci/prow/presubmit"),
					State:   stringPtr(commitState),
				},
			}
			_ = json.NewEncoder(w).Encode(statuses)
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// 1. Status is pending -> label must NOT be added
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while commit status is pending, got %v", addedLabels)
	}

	// 2. Status is success -> label SHOULD be added
	commitState = "success"
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after commit status became success, got %v", addedLabels)
	}
}

func TestEvaluate_Review_GatedByPendingCheckRuns(t *testing.T) {
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
	now := time.Now()

	checkRunStatus := "in_progress"
	checkRunConclusion := ""

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
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			runs := []*githubv39.CheckRun{
				{
					ID:         githubv39.Int64(1),
					Name:       stringPtr("tests"),
					Status:     stringPtr(checkRunStatus),
					Conclusion: stringPtr(checkRunConclusion),
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": runs})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		Kube:           kubeClient,
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
			{Name: stringPtr("factory/review")},
		},
	}

	// 1. While CI is in_progress -> review task must NOT be queued
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	reviewTaskFile := filepath.Join(incomingDir, "task-pr-10-review.yaml")
	if _, err := os.Stat(reviewTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected review task NOT to be created while CI is in_progress")
	}

	// 2. Once CI completes successfully -> review task SHOULD be queued
	checkRunStatus = "completed"
	checkRunConclusion = "success"
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if _, err := os.Stat(reviewTaskFile); os.IsNotExist(err) {
		t.Errorf("expected review task to be created once CI completes successfully")
	}
}

func TestEvaluate_CommentsPrioritizedOverCIFailures(t *testing.T) {
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
	commitTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	commentTime := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)

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
					ID:        githubv39.Int64(100),
					User:      &githubv39.User{Login: stringPtr("human-alice")},
					CreatedAt: &commentTime,
					Body:      stringPtr("Please fix the typo in the config"),
				},
			}
			_ = json.NewEncoder(w).Encode(comments)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			checkRuns := []*githubv39.CheckRun{
				{
					Name:        stringPtr("ci-tests"),
					Status:      stringPtr("completed"),
					Conclusion:  stringPtr("failure"),
					CompletedAt: &githubv39.Timestamp{Time: commentTime},
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": checkRuns})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]*githubv39.RepoStatus{})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/reactions"):
			_ = json.NewEncoder(w).Encode(&githubv39.Reaction{Content: stringPtr("eyes")})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	parsedURL, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = parsedURL
	ghClient.UploadURL = parsedURL

	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	s, queue := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         kubeClient,
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// When both failing CI and new comments exist, comments (Phase 1) must be prioritized over investigate (Phase 3)
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	commentsTaskFile := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	if _, err := os.Stat(commentsTaskFile); os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml to be created when unaddressed comments exist despite CI failure")
	}

	investigateTaskFile := filepath.Join(incomingDir, "task-pr-10-investigate.yaml")
	if _, err := os.Stat(investigateTaskFile); !os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-investigate.yaml NOT to be created when comments take priority")
	}

	// 2. Clear incomingDir and simulate that investigate already ran for this SHA without fixing CI.
	// When another new comment arrives, comments task MUST still be created (no starvation due to failing CI).
	_ = os.Remove(commentsTaskFile)
	_ = queue.RemoveTask("task-pr-10-comments.yaml")
	s.state.set(10, prState{
		lastInvestigatedSHA:  headSHA,
		lastInvestigatedTime: time.Now(),
	})
	commentTime = commentTime.Add(10 * time.Minute)
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if _, err := os.Stat(commentsTaskFile); os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml to be created when new comment arrives on a PR with previously investigated failing CI")
	}
}

func TestEvaluate_CommentsPrioritizedOverMergeConflicts(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	prNum := 10
	mergeable := false // Conflicting / needs rebase!
	headSHA := "sha-1234"
	commitTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	commentTime := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)

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
					ID:        githubv39.Int64(100),
					User:      &githubv39.User{Login: stringPtr("human-alice")},
					CreatedAt: &commentTime,
					Body:      stringPtr("Please fix the typo in the config"),
				},
			}
			_ = json.NewEncoder(w).Encode(comments)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/reactions"):
			_ = json.NewEncoder(w).Encode(&githubv39.Reaction{Content: stringPtr("eyes")})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	parsedURL, _ := url.Parse(server.URL + "/")
	ghClient.BaseURL = parsedURL
	ghClient.UploadURL = parsedURL

	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	kubeClient := &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}

	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       ghClient,
		Kube:         kubeClient,
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// When both merge conflicts and new comments exist, comments (Phase 1) must be prioritized over iterate (Phase 2)
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	commentsTaskFile := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	if _, err := os.Stat(commentsTaskFile); os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml to be created when unaddressed comments exist despite merge conflict")
	}

	iterateTaskFile := filepath.Join(incomingDir, "task-pr-10-iterate.yaml")
	if _, err := os.Stat(iterateTaskFile); !os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-iterate.yaml NOT to be created when comments take priority")
	}
}

func TestEvaluate_InMergeQueue(t *testing.T) {
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
	now := time.Now()

	// 1. Setup mock GitHub REST and GraphQL server
	var restCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		restCalls = append(restCalls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			pr := &githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/graphql":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"data": {
					"repository": {
						"pullRequest": {
							"mergeQueueEntry": {
								"state": "QUEUED"
							}
						}
					}
				}
			}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	// Override GraphQLEndpoint
	origEndpoint := github.GraphQLEndpoint
	github.GraphQLEndpoint = server.URL + "/graphql"
	defer func() {
		github.GraphQLEndpoint = origEndpoint
	}()

	// Ensure GetGithubToken won't fail or trigger gh CLI command.
	os.Setenv("MANUAL_PAT", "dummy-token")
	defer os.Unsetenv("MANUAL_PAT")

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, queue := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// Create a dummy pending task file to verify it gets removed when PR is in merge queue
	taskFile := filepath.Join(incomingDir, "task-pr-10-iterate.yaml")
	_ = os.WriteFile(taskFile, []byte("type: pr-iterate\n"), 0644)
	_ = queue.LoadFromDisk()

	// Execute one evaluation cycle
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	// Check if taskFile was removed
	if _, err := os.Stat(taskFile); !os.IsNotExist(err) {
		t.Errorf("expected pending task file to be removed when PR is in merge queue, but it still exists")
	}

	// Verify that besides fetching the PR and checking GraphQL, no other operations (like fetching commits, comments, reviews, or adding labels) were executed.
	for _, call := range restCalls {
		if strings.Contains(call, "/commits") || strings.Contains(call, "/comments") || strings.Contains(call, "/reviews") || strings.Contains(call, "/labels") {
			t.Errorf("unexpected REST API call made after merge queue check: %s", call)
		}
	}
}

// TestEvaluate_FetchesReferencedIssueOnce is about what an evaluation *costs*,
// rather than what it decides. The cost is not visible in the behaviour: a
// scanner that fetches the same parent issue three times and one that fetches
// it once queue exactly the same work, and the difference only shows up as a
// rate limit in production. The test therefore asserts on the requests the
// scanner made, not on its conclusions.
//
// It covers the memoisation: the label sync, the review opt-in check and the
// readiness check all want the issues the pull request closes, and they used to
// fetch them one after another.
func TestEvaluate_FetchesReferencedIssueOnce(t *testing.T) {
	f := &prFixture{
		num:     10,
		headSHA: "sha-1",
		body:    "Fixes #7",
		updated: time.Now().Add(-time.Hour),
	}
	s := newFixtureScanner(t, f)

	updated := time.Now()
	s.evaluate(context.Background(), &githubv39.Issue{
		Number:           githubv39.Int(10),
		UpdatedAt:        &updated,
		PullRequestLinks: &githubv39.PullRequestLinks{},
		Labels:           []*githubv39.Label{{Name: githubv39.String("factory")}},
	})

	if got := f.count("/issues/7"); got != 1 {
		t.Errorf("fetched referenced issue #7 %d times in one evaluation, want 1", got)
	}
}

// TestScanOnce_SweepsThenFastPasses pins the two-cadence design: the first cycle
// after startup is a full sweep, and the cycles until the sweep interval comes
// round again are the cheap pass over assigned pull requests.
//
// The distinguishing evidence is the open pull request listing, which only the
// sweep performs: it is the expensive query whose cost is the reason the
// cadences were split in the first place.
func TestScanOnce_SweepsThenFastPasses(t *testing.T) {
	gh, calls := recordingServer(t)
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"bot1"},
		TriggerLabel: "factory",
	})
	// Long enough that the second cycle cannot be a sweep.
	s.cfg.SweepInterval = time.Hour

	s.ScanOnce(context.Background())

	if got := countCalls(calls(), "/repos/test-owner/test-repo/pulls"); got != 1 {
		t.Errorf("open PR listings during the first cycle = %d, want 1 (a sweep)", got)
	}
	if got := countCalls(calls(), "labels=factory"); got == 0 {
		t.Error("the first cycle did not run the labelled listing; want a sweep")
	}

	before := len(calls())
	s.ScanOnce(context.Background())
	second := calls()[before:]

	if got := countCalls(second, "/repos/test-owner/test-repo/pulls"); got != 0 {
		t.Errorf("open PR listings during the second cycle = %d, want 0 (a fast pass)", got)
	}
	if got := countCalls(second, "labels=factory"); got != 0 {
		t.Errorf("labelled listings during the second cycle = %d, want 0 (a fast pass)", got)
	}
	if got := countCalls(second, "assignee=bot1"); got == 0 {
		t.Error("the second cycle did not list assigned pull requests; want a fast pass")
	}
}

// TestScanOnce_PausedWhileDraining checks that a draining watcher stops the
// scanner before it spends any GitHub requests, not just before it queues.
func TestScanOnce_PausedWhileDraining(t *testing.T) {
	gh, calls := recordingServer(t)
	s, _ := newTestScanner(t, t.TempDir(), testOpts{GitHub: gh, BotUsers: []string{"bot1"}})
	s.paused = func() bool { return true }

	s.ScanOnce(context.Background())

	if got := len(calls()); got != 0 {
		t.Errorf("made %d GitHub requests while draining, want 0: %v", got, calls())
	}
}

// TestRun_StopsOnContextCancellation checks that cancellation stops the scanner
// and is not reported as a failure: it is how the subcontroller is asked to stop.
func TestRun_StopsOnContextCancellation(t *testing.T) {
	s, _ := newTestScanner(t, t.TempDir(), testOpts{})
	s.cfg.Interval = time.Hour
	s.paused = func() bool { return true }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v, want nil after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}
}

func TestEvaluateAll_Sequential(t *testing.T) {
	var inFlight int32
	var maxInFlight int32
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)

		mu.Lock()
		if current > maxInFlight {
			maxInFlight = current
		}
		mu.Unlock()

		// Sleep briefly to catch any concurrent evaluations
		time.Sleep(10 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		http.NotFound(w, r)
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub: gh,
	})

	candidates := []*githubv39.Issue{
		{Number: githubv39.Int(1)},
		{Number: githubv39.Int(2)},
		{Number: githubv39.Int(3)},
	}

	s.evaluateAll(context.Background(), candidates)

	if maxInFlight != 1 {
		t.Errorf("max in-flight evaluations = %d, want 1 (sequential)", maxInFlight)
	}
}

func TestEvaluateAll_ContextCancelled(t *testing.T) {
	var evaluated int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evaluated++
		w.Header().Set("Content-Type", "application/json")
		http.NotFound(w, r)
	}))
	defer server.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub: gh,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel upfront

	candidates := []*githubv39.Issue{
		{Number: githubv39.Int(1)},
		{Number: githubv39.Int(2)},
	}

	s.evaluateAll(ctx, candidates)

	if evaluated != 0 {
		t.Errorf("evaluated = %d after context cancellation, want 0", evaluated)
	}
}

// Helpers shared by the tests in this package.

func stringPtr(s string) *string { return &s }

func timePtr(t time.Time) *time.Time {
	return &t
}

func int64Ptr(i int64) *int64 {
	return &i
}

// newTestKubeClient returns a client backed by an empty fake cluster, which
// makes every sandbox lookup report "not running" - the state in which the
// scanner is free to queue work.
func newTestKubeClient() *clients.KubernetesClient {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	return &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}
}

// testOpts are the parts of a Scanner's configuration a test cares about. The
// rest are fixed by newTestScanner so that individual tests do not restate them.
type testOpts struct {
	// GitHub is the client, normally pointed at an httptest server.
	GitHub *githubv39.Client
	// Kube backs the sandbox service. A nil client makes every sandbox lookup
	// report "not running", which is what most tests want.
	Kube *clients.KubernetesClient
	// BotUsers is the pool of accounts whose pull requests are evaluated.
	BotUsers []string
	// GitHubLogin is the watcher's own account.
	GitHubLogin string
	// TriggerLabel is the label prefix under test.
	TriggerLabel string
	// ReviewerLogins are the accounts whose reviews count as review feedback.
	ReviewerLogins []string
	// AllowlistedBots are the automated accounts whose comments are acted on.
	AllowlistedBots []string
	// MinNumber skips pull requests numbered below it.
	MinNumber int
}

// newTestScanner builds a Scanner over a real queue manager rooted at tempDir,
// and returns both so that a test can assert on the queue as well as on GitHub.
//
// The intervals are left at their defaults: nothing here drives the Run loop,
// and the tests that do set them explicitly.
func newTestScanner(t *testing.T, tempDir string, opts testOpts) (*Scanner, *concurrency.TaskQueueManager) {
	t.Helper()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	for _, dir := range []string{incomingDir, processingDir, processedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}

	queue := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir:      tempDir,
		IncomingDir:   incomingDir,
		ProcessingDir: processingDir,
		ProcessedDir:  processedDir,
	})

	sandboxes := sandbox.NewService(sandbox.ServiceConfig{
		Namespace: "test-ns",
		Owner:     "test-owner",
		Repo:      "test-repo",
	}, sandbox.ServiceDeps{
		Kube:   opts.Kube,
		GitHub: opts.GitHub,
	})

	scanner := New(Config{
		TriggerLabel:    opts.TriggerLabel,
		GitHubLogin:     opts.GitHubLogin,
		BotUsers:        opts.BotUsers,
		ReviewerLogins:  opts.ReviewerLogins,
		AllowlistedBots: opts.AllowlistedBots,
		MinNumber:       opts.MinNumber,
	}, Deps{
		GitHub:    github.ForRepo(opts.GitHub, "test-owner", "test-repo"),
		Queue:     queue,
		Entities:  concurrency.NewEntityStateCache(),
		Sandboxes: sandboxes,
	})

	return scanner, queue
}

// recordingServer serves empty listings and records the path and query of every
// request, which is how these tests tell a sweep apart from a fast pass.
func recordingServer(t *testing.T) (*githubv39.Client, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		call := r.URL.Path
		if q := r.URL.RawQuery; q != "" {
			call += "?" + q
		}
		calls = append(calls, call)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]interface{}{})
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	return gh, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func countCalls(calls []string, substr string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// prFixture is a GitHub stand-in for one pull request that records every path
// it is asked for.
//
// The knobs are the inputs that decide whether a cycle may skip the pull
// request - its updated_at, whether CI is still running - so that a test can
// move one of them between cycles and watch what the scanner does about it.
type prFixture struct {
	mu sync.Mutex

	num     int
	headSHA string
	// body is the pull request body, which is where a "Fixes #7" reference
	// that pulls a parent issue into the evaluation comes from.
	body string
	// updated is the updated_at the listing reports for the pull request.
	updated time.Time
	// pending makes the head commit's CI report an unfinished check run.
	pending bool
	// mergeable overrides the pull request's Mergeable pointer when set, and
	// mergeableNil leaves Mergeable nil (GitHub still computing mergeability).
	mergeable    *bool
	mergeableNil bool
	// readyForHuman adds the trigger's ready-for-human label to the listed PR.
	readyForHuman bool
	// checkErr makes the check-runs listing fail with a 500 error.
	checkErr bool
	// reviews are the submitted reviews, whose inline comments used to cost a
	// request each.
	reviews []*githubv39.PullRequestReview
	// inline are the review comments the pull request-wide listing returns.
	inline []*githubv39.PullRequestComment

	calls []string
}

// start brings up the server and returns a client pointed at it.
func (f *prFixture) start(t *testing.T) *githubv39.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		call := r.Method + " " + r.URL.Path
		if q := r.URL.RawQuery; q != "" {
			call += "?" + q
		}
		f.calls = append(f.calls, call)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		f.route(w, r)
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	return gh
}

func (f *prFixture) route(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	base := "/repos/test-owner/test-repo"
	mergeable := true
	mergeablePtr := &mergeable
	if f.mergeableNil {
		mergeablePtr = nil
	} else if f.mergeable != nil {
		mergeablePtr = f.mergeable
	}
	now := time.Now()

	switch r.URL.Path {
	case base + "/issues":
		// Both the fast pass's assignee query and the sweep's label query.
		_ = json.NewEncoder(w).Encode([]*githubv39.Issue{f.listingIssueLocked()})

	case base + "/pulls":
		_ = json.NewEncoder(w).Encode([]*githubv39.PullRequest{})

	case fmt.Sprintf("%s/pulls/%d", base, f.num):
		_ = json.NewEncoder(w).Encode(&githubv39.PullRequest{
			Number:    githubv39.Int(f.num),
			Mergeable: mergeablePtr,
			State:     githubv39.String("open"),
			Body:      githubv39.String(f.body),
			User:      &githubv39.User{Login: githubv39.String("bot1")},
			Head:      &githubv39.PullRequestBranch{SHA: githubv39.String(f.headSHA)},
			CreatedAt: &now,
		})

	case fmt.Sprintf("%s/pulls/%d/commits", base, f.num):
		_ = json.NewEncoder(w).Encode([]*githubv39.RepositoryCommit{{
			SHA:    githubv39.String(f.headSHA),
			Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: &now}},
		}})

	case fmt.Sprintf("%s/issues/%d/comments", base, f.num):
		_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})

	case fmt.Sprintf("%s/pulls/%d/reviews", base, f.num):
		_ = json.NewEncoder(w).Encode(f.reviews)

	case fmt.Sprintf("%s/pulls/%d/comments", base, f.num):
		_ = json.NewEncoder(w).Encode(f.inline)

	case base + "/commits/" + f.headSHA + "/check-runs":
		if f.checkErr {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		runs := []*githubv39.CheckRun{}
		if f.pending {
			runs = append(runs, &githubv39.CheckRun{
				Name:   githubv39.String("build"),
				Status: githubv39.String("in_progress"),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": runs})

	case base + "/commits/" + f.headSHA + "/statuses":
		_ = json.NewEncoder(w).Encode([]*githubv39.RepoStatus{})

	case fmt.Sprintf("%s/issues/%d/labels", base, f.num):
		// Label reads and writes both land here, and both answer with a list.
		_ = json.NewEncoder(w).Encode([]*githubv39.Label{})

	default:
		// Referenced issue reads, assignee writes and anything else the
		// evaluation happens to touch.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": 7,
			"labels": []interface{}{},
		})
	}
}

// listingIssueLocked is the pull request as the issue listings report it. The
// caller must hold f.mu.
func (f *prFixture) listingIssueLocked() *githubv39.Issue {
	updated := f.updated
	labels := []*githubv39.Label{{Name: githubv39.String("factory")}}
	if f.readyForHuman {
		labels = append(labels, &githubv39.Label{Name: githubv39.String("factory/ready-for-human")})
	}
	return &githubv39.Issue{
		Number:           githubv39.Int(f.num),
		UpdatedAt:        &updated,
		PullRequestLinks: &githubv39.PullRequestLinks{},
		Assignees:        []*githubv39.User{{Login: githubv39.String("bot1")}},
		Labels:           labels,
	}
}

// count returns how many recorded paths contain substr.
func (f *prFixture) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// reset forgets the recorded calls, so the next assertion covers only what
// happened after it.
func (f *prFixture) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *prFixture) set(mutate func(*prFixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(f)
}

// newFixtureScanner wires a scanner to the fixture with the settings the
// request-counting tests share.
func newFixtureScanner(t *testing.T, f *prFixture) *Scanner {
	t.Helper()
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       f.start(t),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})
	return s
}

// evaluated reports whether anything was fetched for the pull request itself,
// which is the signal that a full evaluation ran rather than being skipped.
func evaluated(f *prFixture) bool {
	return f.count(fmt.Sprintf("/pulls/%d", f.num)) > 0
}

// TestFastPass_SkipsUnchangedPullRequest is the point of the skip gate: a pull
// request nobody has touched is listed, found unchanged, and left alone.
//
// Before the gate, this second cycle re-fetched the pull request, its commits,
// its conversation, its reviews and its CI to arrive at the verdict it had
// already reached a minute earlier.
func TestFastPass_SkipsUnchangedPullRequest(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour)}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	s.fastPass(ctx)
	if !evaluated(f) {
		t.Fatal("the first pass did not evaluate the pull request; the fixture is not wired up")
	}

	f.reset()
	s.fastPass(ctx)

	if evaluated(f) {
		t.Errorf("the second pass re-evaluated an unchanged pull request, want it skipped")
	}
	if f.count("/issues?") == 0 {
		t.Error("the second pass did not list assigned pull requests; the gate must skip the evaluation, not the cycle")
	}
}

// TestFastPass_ReevaluatesWhenUpdatedAtMoves covers the ordinary way a pull
// request becomes interesting again: somebody pushed, commented or labelled it.
func TestFastPass_ReevaluatesWhenUpdatedAtMoves(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour)}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	s.fastPass(ctx)
	f.reset()

	f.set(func(f *prFixture) { f.updated = time.Now() })
	s.fastPass(ctx)

	if !evaluated(f) {
		t.Error("a pull request whose updated_at moved was skipped, want it re-evaluated")
	}
}

// TestFastPass_ReevaluatesWhileCIInFlight is the blind spot the gate has to
// cover explicitly: a check run completing does not move the pull request's
// updated_at, so a pull request waiting on CI would otherwise be skipped until
// the next sweep - exactly the pull request the fast pass exists for.
func TestFastPass_ReevaluatesWhileCIInFlight(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour), pending: true}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	s.fastPass(ctx)
	f.reset()

	// Nothing about the pull request changed, and nothing about it can: CI
	// finishing is invisible in updated_at.
	s.fastPass(ctx)

	if !evaluated(f) {
		t.Error("a pull request with CI in flight was skipped, want it re-evaluated every cycle")
	}
}

// TestFastPass_ReevaluatesWhenTaskFinishes covers the other invisible
// transition: a task completing is what makes a pull request ready for a human,
// and a task that pushed nothing leaves updated_at untouched.
func TestFastPass_ReevaluatesWhenTaskFinishes(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour)}

	tempDir := t.TempDir()
	s, _ := newTestScanner(t, tempDir, testOpts{
		GitHub:       f.start(t),
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})
	ctx := context.Background()

	// A task is in flight, so the first pass records the pull request as
	// having work against it.
	taskPath := filepath.Join(tempDir, "incoming", "task-pr-10-comments.yaml")
	if err := os.WriteFile(taskPath, []byte("type: pr-comments\n"), 0644); err != nil {
		t.Fatalf("writing task file: %v", err)
	}
	s.fastPass(ctx)
	if !evaluated(f) {
		t.Fatal("the first pass did not evaluate the pull request")
	}

	// The task finishes without touching the pull request on GitHub, so
	// updated_at is exactly where the first pass left it.
	if err := os.Remove(taskPath); err != nil {
		t.Fatalf("removing task file: %v", err)
	}

	f.reset()
	s.fastPass(ctx)

	if !evaluated(f) {
		t.Error("a pull request whose task just finished was skipped, want it re-evaluated so the ready-for-human label is reconciled")
	}
}

// TestSweep_EvaluatesRegardlessOfSkipGate pins the safety valve the gate relies
// on. Every signal the gate cannot see - a re-run check, a change upstream - is
// bounded by the sweep, so the sweep must never consult it.
func TestSweep_EvaluatesRegardlessOfSkipGate(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour)}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	// A fast pass first, so the pull request is on record as evaluated and a
	// gated cycle would skip it.
	s.fastPass(ctx)
	f.reset()

	s.sweep(ctx)

	if !evaluated(f) {
		t.Error("the sweep skipped an unchanged pull request; it must evaluate everything unconditionally")
	}
}

// TestFastPass_ReevaluatesAfterEarlyReturnOnPreviouslyEvaluatedPR covers a pull
// request that had already been evaluated cleanly at a given updated_at and
// then hit an early return (such as a check listing error) during a subsequent
// evaluation at the same timestamp: the early return must clear the previous
// evaluation record rather than leaving the old timestamp in place.
func TestFastPass_ReevaluatesAfterEarlyReturnOnPreviouslyEvaluatedPR(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour)}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	// 1. First pass completes cleanly and records updated_at.
	s.fastPass(ctx)
	if !evaluated(f) {
		t.Fatal("the first pass did not evaluate the pull request")
	}

	// 2. A sweep runs while updated_at is unchanged, and check-runs fails mid-evaluation.
	f.set(func(f *prFixture) { f.checkErr = true })
	s.sweep(ctx)

	// 3. The next fast pass must re-evaluate the pull request because the sweep
	// aborted before recordEvaluation.
	f.set(func(f *prFixture) { f.checkErr = false })
	f.reset()
	s.fastPass(ctx)

	if !evaluated(f) {
		t.Error("a pull request whose previous evaluation aborted early was skipped, want it re-evaluated")
	}
}

// TestFastPass_ReevaluatesWhenMergeableUnknown covers GitHub's asynchronous
// mergeability computation: when GetPullRequest returns Mergeable == nil, the
// evaluation must not mark the pull request as settled, because the transition
// to true or false does not move updated_at.
func TestFastPass_ReevaluatesWhenMergeableUnknown(t *testing.T) {
	f := &prFixture{num: 10, headSHA: "sha-1", updated: time.Now().Add(-time.Hour), mergeableNil: true}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	s.fastPass(ctx)
	if f.count("/labels") > 0 {
		t.Error("a pull request with unknown Mergeable state was labelled ready-for-human")
	}

	f.reset()
	f.set(func(f *prFixture) { f.mergeableNil = false })
	s.fastPass(ctx)

	if !evaluated(f) {
		t.Error("a pull request whose Mergeable state was unknown on the previous pass was skipped")
	}
}

// TestSweep_PreservesReadyForHumanWhenMergeableUnknown verifies that an
// already-ready pull request never has its ready-for-human label stripped by
// the watcher, whether mergeability is temporarily unknown or CI is pending.
func TestSweep_PreservesReadyForHumanWhenMergeableUnknown(t *testing.T) {
	f := &prFixture{
		num:           10,
		headSHA:       "sha-1",
		updated:       time.Now().Add(-time.Hour),
		mergeableNil:  true,
		readyForHuman: true,
	}
	s := newFixtureScanner(t, f)
	ctx := context.Background()

	// 1. Mergeable == nil while all other gates pass: ready-for-human must not be removed.
	s.sweep(ctx)
	if f.count("DELETE /repos/test-owner/test-repo/issues/10/labels/") > 0 {
		t.Fatal("ready-for-human label was removed when Mergeable was temporarily nil")
	}

	// 2. Even if another readiness gate fails (e.g., CI pending), ready-for-human
	// must never be removed by the watcher.
	f.reset()
	f.set(func(f *prFixture) { f.pending = true })
	s.sweep(ctx)
	if f.count("DELETE /repos/test-owner/test-repo/issues/10/labels/") > 0 {
		t.Fatal("expected ready-for-human label never to be removed when CI became pending")
	}
}

func TestEvaluate_ReadyForHuman_ReviewLabelAndHumanAssignees(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")

	prNum := 10
	issueNum := 7
	mergeable := true
	headSHA := "sha-1"
	now := time.Now()

	var addedPRLabels []string
	var removedPRLabels []string
	var removedIssueLabels []string
	var addedAssignees []string
	var removedAssignees []string

	var reviews []*githubv39.PullRequestReview
	reviews = []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("reviewbot")},
			CommitID:    stringPtr(headSHA),
			State:       stringPtr("APPROVED"),
			SubmittedAt: &now,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10":
			pr := &githubv39.PullRequest{
				Number:    &prNum,
				Mergeable: &mergeable,
				State:     stringPtr("open"),
				Body:      stringPtr("Fixes #7"),
				User:      &githubv39.User{Login: stringPtr("bot1")},
				Head:      &githubv39.PullRequestBranch{SHA: stringPtr(headSHA)},
				CreatedAt: &now,
			}
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/7":
			parentIssue := &githubv39.Issue{
				Number: &issueNum,
				Labels: []*githubv39.Label{
					{Name: stringPtr("factory")},
					{Name: stringPtr("overseer/review")},
				},
				Assignees: []*githubv39.User{
					{Login: stringPtr("bot1")},
					{Login: stringPtr("reviewbot")},
					{Login: stringPtr("human-alice")},
					{Login: stringPtr("human-bob")},
				},
			}
			_ = json.NewEncoder(w).Encode(parentIssue)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/commits":
			commits := []*githubv39.RepositoryCommit{
				{
					SHA: stringPtr(headSHA),
					Commit: &githubv39.Commit{
						Committer: &githubv39.CommitAuthor{Date: &now},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(commits)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.IssueComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/comments":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestComment{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode(reviews)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []interface{}{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/labels":
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedPRLabels = append(addedPRLabels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/10/labels/"):
			label := strings.TrimPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/10/labels/")
			removedPRLabels = append(removedPRLabels, label)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/7/labels/"):
			label := strings.TrimPrefix(r.URL.Path, "/repos/test-owner/test-repo/issues/7/labels/")
			removedIssueLabels = append(removedIssueLabels, label)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			var payload struct {
				Assignees []string `json:"assignees"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			addedAssignees = append(addedAssignees, payload.Assignees...)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		case r.Method == "DELETE" && r.URL.Path == "/repos/test-owner/test-repo/issues/10/assignees":
			var payload struct {
				Assignees []string `json:"assignees"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			removedAssignees = append(removedAssignees, payload.Assignees...)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := githubv39.NewClient(nil)
	ghClient.BaseURL, _ = url.Parse(server.URL + "/")

	s, queue := newTestScanner(t, tempDir, testOpts{
		GitHub:         ghClient,
		Kube:           newTestKubeClient(),
		BotUsers:       []string{"bot1"},
		GitHubLogin:    "bot1",
		TriggerLabel:   "factory",
		ReviewerLogins: []string{"reviewbot"},
	})

	// 1. Initial settle: PR has overseer/review, bot review is completed on headSHA.
	// Expect:
	// - factory/ready-for-human added to PR
	// - overseer/review removed from PR (and NOT from parent issue #7)
	// - human-alice and human-bob assigned to PR from parent issue #7
	// - bot1 unassigned from PR
	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
			{Name: stringPtr("overseer/review")},
		},
	}
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if len(addedPRLabels) != 1 || addedPRLabels[0] != "factory/ready-for-human" {
		t.Fatalf("step 1: addedPRLabels = %v, want [factory/ready-for-human]", addedPRLabels)
	}
	if len(removedPRLabels) != 1 || removedPRLabels[0] != "overseer/review" {
		t.Fatalf("step 1: removedPRLabels = %v, want [overseer/review]", removedPRLabels)
	}
	if len(removedIssueLabels) != 0 {
		t.Fatalf("step 1: removedIssueLabels = %v, want none (parent issue must keep overseer/review)", removedIssueLabels)
	}
	if len(addedAssignees) != 2 || addedAssignees[0] != "human-alice" || addedAssignees[1] != "human-bob" {
		t.Fatalf("step 1: addedAssignees = %v, want [human-alice human-bob]", addedAssignees)
	}
	if len(removedAssignees) != 1 || removedAssignees[0] != "bot1" {
		t.Fatalf("step 1: removedAssignees = %v, want [bot1]", removedAssignees)
	}

	// 2. Subsequent cycle on a new commit (sha-2) after settling:
	// PR has factory/ready-for-human, parent issue #7 still has overseer/review.
	// overseer/review must NOT be re-synced to the PR and must NOT trigger a new review.
	addedPRLabels = nil
	removedPRLabels = nil
	addedAssignees = nil
	removedAssignees = nil
	headSHA = "sha-2"
	reviews = nil

	prIssueSettled := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("human-alice")},
			{Login: stringPtr("human-bob")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
			{Name: stringPtr("factory/ready-for-human")},
		},
	}
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssueSettled})

	if len(addedPRLabels) != 0 {
		t.Fatalf("step 2: addedPRLabels = %v, want none (overseer/review must not be re-synced from parent issue)", addedPRLabels)
	}
	reviewTaskFile := filepath.Join(incomingDir, "task-pr-10-review.yaml")
	if _, err := os.Stat(reviewTaskFile); !os.IsNotExist(err) {
		t.Fatalf("step 2: expected no review task to be queued when overseer/review was only on the parent issue")
	}
	if len(addedAssignees) != 0 {
		t.Fatalf("step 2: addedAssignees = %v, want none on already-ready PR", addedAssignees)
	}

	// 3. Human manually re-adds overseer/review to the settled PR:
	// It should queue a bot review task and keep overseer/review while the review is pending.
	prIssueReReview := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("human-alice")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
			{Name: stringPtr("factory/ready-for-human")},
			{Name: stringPtr("overseer/review")},
		},
	}
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssueReReview})

	if _, err := os.Stat(reviewTaskFile); os.IsNotExist(err) {
		t.Fatalf("step 3: expected review task to be queued when human manually re-added overseer/review to PR")
	}
	if len(removedPRLabels) != 0 {
		t.Fatalf("step 3: removedPRLabels = %v, want none while review task is still pending", removedPRLabels)
	}

	// 4. Bot review completes on sha-2 and review task finishes:
	// PR settles again and removes overseer/review from the PR again.
	_ = os.Remove(reviewTaskFile)
	_ = queue.RemoveTask("task-pr-10-review.yaml")
	reviews = []*githubv39.PullRequestReview{
		{
			User:        &githubv39.User{Login: stringPtr("reviewbot")},
			CommitID:    stringPtr(headSHA),
			State:       stringPtr("APPROVED"),
			SubmittedAt: &now,
		},
	}
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssueReReview})

	if len(removedPRLabels) != 1 || removedPRLabels[0] != "overseer/review" {
		t.Fatalf("step 4: removedPRLabels = %v, want [overseer/review] removed after re-review settled", removedPRLabels)
	}
}
