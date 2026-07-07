package watch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestScan(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Scenario A: Successful Scan cycle
	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	w := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", Mode: "all"},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		allBotUsers:     []string{"factory-bot"},
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	w.scan(ctx)

	// Scenario B: Scan with HTTP failures (ensure it doesn't crash)
	errorHTTPClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewBufferString("Internal Server Error")),
				Header:     make(http.Header),
			}
		}),
	}
	wError := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", Mode: "all"},
		ghClient:        githubv39.NewClient(errorHTTPClient),
		kubeClient:      newFakeKubeClient(),
		allBotUsers:     []string{"factory-bot"},
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	wError.scan(ctx)
}

func TestScanCreatorIssues(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/pulls") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/repos/owner/repo/issues") {
				// Return issue created by creator-user if creator param is specified
				if req.URL.Query().Get("creator") == "creator-user" {
					issueJSON := `[
						{
							"number": 100,
							"state": "open",
							"title": "Fix this issue",
							"user": {"login": "creator-user"},
							"assignees": [],
							"labels": []
						}
					]`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(issueJSON)),
						Header:     make(http.Header),
					}
				}
				// Default list by repo return empty
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			// Fallback (POST endpoints like labels, assignees)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString("{}")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	w := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", Mode: "all", IssueMode: "enabled"},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		allBotUsers:     []string{"factory-bot"},
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		githubLogin:     "creator-user",
		targetAssignee:  "factory-bot",
		triggerLabel:    "ai-factory",
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	w.scan(ctx)

	// Since IssueMode is enabled, and we fetched issue 100, we expect a task file to be queued
	taskPath := filepath.Join(incomingDir, "task-issue-100.yaml")
	if _, err := os.Stat(taskPath); os.IsNotExist(err) {
		t.Errorf("expected task-issue-100.yaml to be created under incoming directory")
	}
}
