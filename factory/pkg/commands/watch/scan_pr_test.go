package watch

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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestProcessPRs_Filters(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

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
	}

	stopPRNum := 20
	// Create a dummy pending task file for the stop PR to verify removal
	stopTaskFile := filepath.Join(incomingDir, "task-pr-20-iterate.yaml")
	_ = os.WriteFile(stopTaskFile, []byte("type: pr-iterate\n"), 0644)

	cfg := &config.FactoryConfig{
		MinNumber: 10,
	}

	lowPRNum := 5
	prIssues := []*githubv39.Issue{
		{
			Number: &lowPRNum,
		},
		{
			Number: &stopPRNum,
			Labels: []*githubv39.Label{
				{Name: stringPtr("overseer/stop")},
			},
		},
	}

	processedPRs := make(map[int]prWatchState)
	allBotUsers := []string{"bot1"}

	w.cfg = cfg
	w.processedPRs = processedPRs
	w.allBotUsers = allBotUsers
	w.githubLogin = "bot1"
	w.incomingDir = incomingDir
	w.processingDir = processingDir
	w.processedDir = processedDir
	w.triggerLabel = "factory"
	w.initQueueManager()
	_ = w.queueMgr.LoadFromDisk()

	w.processPRs(context.Background(), prIssues)

	// Verify stop task was removed
	if _, err := os.Stat(stopTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected stop PR task file to be removed, but it still exists")
	}
}

func TestProcessPRs_DisabledMode(t *testing.T) {
	w := &Watcher{
		Flags: Flags{
			PRMode: "disabled",
		},
	}
	w.initQueueManager()
	// Should return immediately without doing any operations
	w.processPRs(context.Background(), nil)
}

func TestProcessPRs_ReadyForHuman_GatedByActiveTask(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		ghClient:      ghClient,
		allBotUsers:   []string{"bot1"},
		githubLogin:   "bot1",
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
		cfg: &config.FactoryConfig{
			Roles: map[string]config.RoleConfig{
				"reviewer": {Users: []string{"reviewbot"}},
			},
		},
	}
	w.initQueueManager()

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

	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

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

	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

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

	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after task completion, got %v", addedLabels)
	}
	if len(unassignCalls) != 1 {
		t.Errorf("expected 1 unassign call after task completion, got %d (%v)", len(unassignCalls), unassignCalls)
	}
}

func TestProcessPRs_UnassignOnReadyForHuman(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		ghClient:      ghClient,
		allBotUsers:   []string{"bot1"},
		githubLogin:   "bot1",
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
		cfg: &config.FactoryConfig{
			Roles: map[string]config.RoleConfig{
				"reviewer": {Users: []string{"reviewbot"}},
			},
		},
	}
	w.initQueueManager()

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

	if len(unassignCalls) != 1 {
		t.Fatalf("expected 1 unassign API call, got %d (%v)", len(unassignCalls), unassignCalls)
	}
	if !strings.Contains(unassignCalls[0], "bot1") {
		t.Errorf("expected unassign call body to contain 'bot1', got %s", unassignCalls[0])
	}
}

func TestProcessPRs_ReadyForHuman_GatedByPendingCheckRuns(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		ghClient:      ghClient,
		allBotUsers:   []string{"bot1"},
		githubLogin:   "bot1",
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
	}
	w.initQueueManager()

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
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while check runs are in_progress, got %v", addedLabels)
	}

	// 2. Check runs complete successfully -> label SHOULD be added
	checkRunStatus = "completed"
	checkRunConclusion = "success"
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after check runs completed, got %v", addedLabels)
	}

	// 3. New commit or check run becomes queued/in_progress on PR with label -> label SHOULD be removed
	prIssue.Labels = append(prIssue.Labels, &githubv39.Label{Name: stringPtr("factory/ready-for-human")})
	checkRunStatus = "queued"
	checkRunConclusion = ""
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if len(removedLabels) != 1 || removedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label removed when checks become queued, got %v", removedLabels)
	}
}

func TestProcessPRs_ReadyForHuman_GatedByPendingCommitStatus(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		ghClient:      ghClient,
		allBotUsers:   []string{"bot1"},
		githubLogin:   "bot1",
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
	}
	w.initQueueManager()

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
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 0 {
		t.Errorf("expected 0 added labels while commit status is pending, got %v", addedLabels)
	}

	// 2. Status is success -> label SHOULD be added
	commitState = "success"
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if len(addedLabels) != 1 || addedLabels[0] != "factory/ready-for-human" {
		t.Errorf("expected ready-for-human label added after commit status became success, got %v", addedLabels)
	}
}

func TestProcessPRs_Review_GatedByPendingCheckRuns(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		kubeClient:    kubeClient,
		ghClient:      ghClient,
		allBotUsers:   []string{"bot1"},
		githubLogin:   "bot1",
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
		cfg: &config.FactoryConfig{
			Roles: map[string]config.RoleConfig{
				"reviewer": {Users: []string{"reviewbot"}},
			},
		},
	}
	w.initQueueManager()

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
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	reviewTaskFile := filepath.Join(incomingDir, "task-pr-10-review.yaml")
	if _, err := os.Stat(reviewTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected review task NOT to be created while CI is in_progress")
	}

	// 2. Once CI completes successfully -> review task SHOULD be queued
	checkRunStatus = "completed"
	checkRunConclusion = "success"
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})
	if _, err := os.Stat(reviewTaskFile); os.IsNotExist(err) {
		t.Errorf("expected review task to be created once CI completes successfully")
	}
}

func TestProcessPRs_CommentsPrioritizedOverCIFailures(t *testing.T) {
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

	w := &Watcher{
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
		githubLogin:   "bot1",
		allBotUsers:   []string{"bot1"},
		incomingDir:   incomingDir,
		processingDir: processingDir,
		processedDir:  processedDir,
		triggerLabel:  "factory",
		processedPRs:  make(map[int]prWatchState),
		ghClient:      ghClient,
		kubeClient:    kubeClient,
	}
	w.initQueueManager()

	prIssue := &githubv39.Issue{
		Number: &prNum,
		Assignees: []*githubv39.User{
			{Login: stringPtr("bot1")},
		},
		Labels: []*githubv39.Label{
			{Name: stringPtr("factory")},
		},
	}

	// When both failing CI and new comments exist, comments (Phase 2) must be prioritized over investigate (Phase 3)
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

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
	_ = w.queueMgr.RemoveTask("task-pr-10-comments.yaml")
	w.processedPRs[10] = prWatchState{
		lastInvestigatedSHA:  headSHA,
		lastInvestigatedTime: time.Now(),
	}
	commentTime = commentTime.Add(10 * time.Minute)
	w.processPRs(context.Background(), []*githubv39.Issue{prIssue})

	if _, err := os.Stat(commentsTaskFile); os.IsNotExist(err) {
		t.Fatalf("expected task-pr-10-comments.yaml to be created when new comment arrives on a PR with previously investigated failing CI")
	}
}
