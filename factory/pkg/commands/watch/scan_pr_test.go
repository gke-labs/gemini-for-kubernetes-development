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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
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
		case r.Method == "GET" && r.URL.Path == "/repos/test-owner/test-repo/statuses/"+headSHA:
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
