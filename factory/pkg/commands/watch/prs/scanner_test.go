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
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
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

	// 3. New commit or check run becomes queued/in_progress on PR with label -> label SHOULD be removed
	prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: stringPtr("factory/ready-for-human")})
	checkRunStatus = "queued"
	checkRunConclusion = ""
	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})
	if len(removedLabels) != 1 || removedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label removed when checks become queued, got %v", removedLabels)
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

func TestEvaluate_CommentsRetry(t *testing.T) {
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
	botCommentTime := time.Date(2026, 8, 1, 11, 5, 0, 0, time.UTC)

	var listComments []*githubv39.IssueComment
	var addedComments []string
	var addedLabels []string

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
			_ = json.NewEncoder(w).Encode(listComments)
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/pulls/10/reviews":
			_ = json.NewEncoder(w).Encode([]*githubv39.PullRequestReview{})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"check_runs": []*githubv39.CheckRun{}})
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/commits/"+headSHA+"/statuses":
			_ = json.NewEncoder(w).Encode([]*githubv39.RepoStatus{})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/comments"):
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addedComments = append(addedComments, body.Body)
			_ = json.NewEncoder(w).Encode(&githubv39.IssueComment{Body: stringPtr(body.Body)})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/labels"):
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]*githubv39.Label{})
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

	// Case 1: Human comment is present, but previously enqueued comments task has Failed.
	// Attempt count is 1 (which is < maxCommentAttempts).
	// It should trigger a retry and enqueue task-pr-10-comments.yaml again.
	listComments = []*githubv39.IssueComment{
		{
			ID:        githubv39.Int64(100),
			User:      &githubv39.User{Login: stringPtr("human-alice")},
			CreatedAt: &commentTime,
			Body:      stringPtr("Please fix the typo in the config"),
		},
		{
			ID:        githubv39.Int64(101),
			User:      &githubv39.User{Login: stringPtr("bot1")},
			CreatedAt: &botCommentTime,
			Body:      stringPtr("🤖 AI Factory started addressing review feedback for this pull request."),
		},
	}

	// Write failed task to processed dir
	failedTaskFile := filepath.Join(processedDir, "task-pr-10-comments.yaml")
	_ = os.WriteFile(failedTaskFile, []byte("type: pr-comments\nstatus: Failed\ncommitSHA: sha-1234\n"), 0644)

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	commentsTaskFile := filepath.Join(incomingDir, "task-pr-10-comments.yaml")
	if _, err := os.Stat(commentsTaskFile); os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml to be enqueued for retry when last task failed and attempt count is 1")
	}

	// Case 2: Attempt count is 3 (>= maxCommentAttempts = 3) and last task failed.
	// It should NOT queue a retry, but should add a stop comment and stop label.
	_ = os.Remove(commentsTaskFile)
	_ = queue.RemoveTask("task-pr-10-comments.yaml")

	botCommentTime2 := botCommentTime.Add(time.Minute)
	botCommentTime3 := botCommentTime.Add(2 * time.Minute)

	listComments = []*githubv39.IssueComment{
		{
			ID:        githubv39.Int64(100),
			User:      &githubv39.User{Login: stringPtr("human-alice")},
			CreatedAt: &commentTime,
			Body:      stringPtr("Please fix the typo in the config"),
		},
		{
			ID:        githubv39.Int64(101),
			User:      &githubv39.User{Login: stringPtr("bot1")},
			CreatedAt: &botCommentTime,
			Body:      stringPtr("🤖 AI Factory started addressing review feedback for this pull request."),
		},
		{
			ID:        githubv39.Int64(102),
			User:      &githubv39.User{Login: stringPtr("bot1")},
			CreatedAt: &botCommentTime2,
			Body:      stringPtr("🤖 AI Factory started addressing review feedback for this pull request."),
		},
		{
			ID:        githubv39.Int64(103),
			User:      &githubv39.User{Login: stringPtr("bot1")},
			CreatedAt: &botCommentTime3,
			Body:      stringPtr("🤖 AI Factory started addressing review feedback for this pull request."),
		},
	}

	s.evaluateAll(context.Background(), []*githubv39.Issue{prIssue})

	if _, err := os.Stat(commentsTaskFile); !os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml NOT to be enqueued because maxCommentAttempts is reached")
	}

	// Verify stop comment was posted
	foundStopComment := false
	for _, c := range addedComments {
		if strings.Contains(c, "pausing automated processing") {
			foundStopComment = true
			break
		}
	}
	if !foundStopComment {
		t.Errorf("expected stop comment to be added, got: %v", addedComments)
	}

	// Verify stop label was added
	foundStopLabel := false
	for _, l := range addedLabels {
		if l == "factory/stop" {
			foundStopLabel = true
			break
		}
	}
	if !foundStopLabel {
		t.Errorf("expected stop label 'factory/stop' to be added, got: %v", addedLabels)
	}
}

func TestGetCommentsAttemptCount_NilUser(t *testing.T) {
	commitTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	comments := []*githubv39.IssueComment{
		{
			ID:        githubv39.Int64(100),
			User:      nil, // Nil user
			CreatedAt: &commitTime,
			Body:      stringPtr("Comment from deleted user"),
		},
	}
	// Should not panic and return 0
	count := getCommentsAttemptCount(comments, commitTime, []string{"bot1"}, "bot1", []string{"bot1"}, "factory")
	if count != 0 {
		t.Errorf("expected count to be 0, got %d", count)
	}

	investigateCount := getInvestigationCount(comments, commitTime, []string{"bot1"}, "bot1", []string{"bot1"}, "factory")
	if investigateCount != 0 {
		t.Errorf("expected investigate count to be 0, got %d", investigateCount)
	}
}

func TestEvaluateComments_DiffCommitSHA(t *testing.T) {
	tempDir := t.TempDir()
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(processedDir, 0755)

	s := &Scanner{
		cfg: Config{
			ProcessedDir: processedDir,
		},
	}

	filename := "task-pr-10-comments.yaml"
	failedTaskFile := filepath.Join(processedDir, filename)
	_ = os.WriteFile(failedTaskFile, []byte("type: pr-comments\nstatus: Failed\ncommitSHA: sha-old\n"), 0644)

	// lastCommentsTaskFailed should be false for headSHA "sha-new" because of mismatched commit SHA
	if s.lastCommentsTaskFailed(filename, "sha-new") {
		t.Errorf("expected lastCommentsTaskFailed to be false for different commit SHA")
	}

	// lastCommentsTaskFailed should be true for headSHA "sha-old"
	if !s.lastCommentsTaskFailed(filename, "sha-old") {
		t.Errorf("expected lastCommentsTaskFailed to be true for matching commit SHA")
	}
}

func TestEvaluateComments_ResumptionWithPausingComment(t *testing.T) {
	tempDir := t.TempDir()
	s, _ := newTestScanner(t, tempDir, testOpts{
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prNum := 10
	commitTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	commentTime := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	pauseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	pr := &githubv39.PullRequest{
		Number:    &prNum,
		State:     stringPtr("open"),
		User:      &githubv39.User{Login: stringPtr("author-user")},
		Head:      &githubv39.PullRequestBranch{SHA: stringPtr("sha-1234")},
		CreatedAt: &commitTime,
	}

	// Comments include a human comment followed by a pausing automated processing comment.
	comments := []*githubv39.IssueComment{
		{
			ID:        githubv39.Int64(100),
			User:      &githubv39.User{Login: stringPtr("human-alice")},
			CreatedAt: &commentTime,
			Body:      stringPtr("Please fix the typo in the config"),
		},
		{
			ID:        githubv39.Int64(101),
			User:      &githubv39.User{Login: stringPtr("bot1")},
			CreatedAt: &pauseTime,
			Body:      stringPtr("🤖 AI Factory has attempted to address review feedback ... pausing automated processing and attaching stop label."),
		},
	}

	history := &prHistory{
		comments: comments,
	}

	// Although pauseTime (latest bot reply) is after commentTime, the pausing comment is excluded.
	// Therefore, evaluateComments should detect the human comment as outstanding and return hasNewComments = true.
	analysis := s.evaluateComments(
		context.Background(),
		prNum,
		pr,
		history,
		commitTime,
		time.Time{},
		"",
		"sha-1234",
	)

	if !analysis.hasNewComments {
		t.Errorf("expected evaluateComments to find outstanding comment even when pausing comment is the latest bot reply")
	}
}

func TestEvaluateComments_NilUser(t *testing.T) {
	tempDir := t.TempDir()
	s, _ := newTestScanner(t, tempDir, testOpts{
		BotUsers:     []string{"bot1"},
		GitHubLogin:  "bot1",
		TriggerLabel: "factory",
	})

	prNum := 10
	commitTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	commentTime := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)

	// pr user is nil too to check both c.GetUser() != nil and pr.GetUser() != nil checks
	pr := &githubv39.PullRequest{
		Number:    &prNum,
		State:     stringPtr("open"),
		User:      nil, // PR author is nil
		Head:      &githubv39.PullRequestBranch{SHA: stringPtr("sha-1234")},
		CreatedAt: &commitTime,
	}

	// Issue comment with nil user
	comments := []*githubv39.IssueComment{
		{
			ID:        githubv39.Int64(100),
			User:      nil, // Nil user
			CreatedAt: &commentTime,
			Body:      stringPtr("Comment from deleted user"),
		},
	}

	// Review with nil user
	reviews := []*githubv39.PullRequestReview{
		{
			ID:          githubv39.Int64(200),
			User:        nil, // Nil user
			SubmittedAt: &commentTime,
			Body:        stringPtr("Review from deleted user"),
		},
	}

	// Inline review comment with nil user
	revCommentsMap := map[int64][]*githubv39.PullRequestComment{
		200: {
			{
				ID:        githubv39.Int64(300),
				User:      nil, // Nil user
				CreatedAt: &commentTime,
				Body:      stringPtr("Inline review comment from deleted user"),
			},
		},
	}

	history := &prHistory{
		comments:       comments,
		reviews:        reviews,
		revCommentsMap: revCommentsMap,
	}

	// Should not panic
	analysis := s.evaluateComments(
		context.Background(),
		prNum,
		pr,
		history,
		commitTime,
		time.Time{},
		"",
		"sha-1234",
	)

	// Since they are not ignored, they should count as new comments
	if !analysis.hasNewComments {
		t.Errorf("expected evaluateComments to successfully process comments even with nil users")
	}
}
