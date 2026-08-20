package watch

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
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

type mockTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

type mockMergeQueueHTTPClient struct {
	response *http.Response
	err      error
}

func (m *mockMergeQueueHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.response, m.err
}

func TestProcessPRs_MergeQueue(t *testing.T) {
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

	queuePRNum := 30
	// Create a dummy pending task file for the queue PR to verify removal
	queueTaskFile := filepath.Join(incomingDir, "task-pr-30-iterate.yaml")
	_ = os.WriteFile(queueTaskFile, []byte("type: pr-iterate\n"), 0644)

	cfg := &config.FactoryConfig{
		MinNumber: 10,
	}

	prIssues := []*githubv39.Issue{
		{
			Number: &queuePRNum,
		},
	}

	processedPRs := make(map[int]prWatchState)
	allBotUsers := []string{"bot1"}

	// Set MANUAL_PAT environment variable so github.GetGithubToken succeeds
	os.Setenv("MANUAL_PAT", "test-token")
	defer os.Unsetenv("MANUAL_PAT")

	// Stub github.DefaultHTTPClient to return that the PR is in the merge queue
	origDefaultClient := github.DefaultHTTPClient
	defer func() {
		github.DefaultHTTPClient = origDefaultClient
	}()

	mockResp := `{
		"data": {
			"repository": {
				"pullRequest": {
					"mergeQueueEntry": {
						"position": 1
					}
				}
			}
		}
	}`
	github.DefaultHTTPClient = &mockMergeQueueHTTPClient{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
		},
	}

	// Stub the githubv39.Client's PullRequests.Get call
	tc := &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				// Return a valid Pull Request json
				prJSON := `{"number": 30, "user": {"login": "bot1"}, "mergeable": true}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(prJSON)),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	ghClient := githubv39.NewClient(tc)

	w.processPRs(context.Background(), ghClient, nil, cfg, prIssues, processedPRs, allBotUsers, "bot1", incomingDir, processingDir, processedDir, "factory")

	// Verify that the task-pr-30-iterate.yaml was deleted from incoming because the PR was in the merge queue and skipped
	if _, err := os.Stat(queueTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected queue PR task file to be removed, but it still exists")
	}
}
