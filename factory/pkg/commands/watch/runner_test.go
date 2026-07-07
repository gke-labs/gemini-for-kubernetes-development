package watch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	if os.Getenv("BE_HELPER_PROCESS") == "1" {
		os.Exit(0)
	}
	if os.Getenv("BE_HELPER_PROCESS") == "fail" {
		os.Exit(1)
	}
}

func TestRun(t *testing.T) {
	// Set the environment variable so any child subprocess calls exit successfully immediately
	os.Setenv("BE_HELPER_PROCESS", "1")
	defer os.Unsetenv("BE_HELPER_PROCESS")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Set logs env var
	t.Setenv("FACTORY_LOGS", tempDir)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/issues/1") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"number":1,"assignees":[]}`)),
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
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10, TaskTimeout: 5 * time.Second},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		targetAssignee:  "factory-bot",
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	task := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/1",
		Number:    1,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
	}
	err := writeTaskAtomically(incomingDir, "task-issue-1.yaml", task)
	if err != nil {
		t.Fatalf("failed to write task: %v", err)
	}

	var wg sync.WaitGroup
	w.run(ctx, &wg, true)
	wg.Wait()

	// After execution, the task should have been run and renamed/completed
	// Verify that it moved to processed
	processedPath := filepath.Join(processedDir, "task-issue-1.yaml")
	if _, err := os.Stat(processedPath); os.IsNotExist(err) {
		t.Errorf("expected task-issue-1.yaml to be completed and moved to processed")
	}
}

func TestRunLimitsAndSkips(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Scenario A: MaxActions reached
	wLimit := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 0, MaxPending: 10},
		ghClient:        nil,
		kubeClient:      newFakeKubeClient(),
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

	task := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/1",
		Number:    1,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
	}
	_ = writeTaskAtomically(incomingDir, "task-issue-1.yaml", task)

	var wg sync.WaitGroup
	wLimit.run(ctx, &wg, true)
	wg.Wait()

	// Task should still be in incoming directory (unexecuted because MaxActions = 0)
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-1.yaml")); os.IsNotExist(err) {
		t.Errorf("expected task to remain in incoming when MaxActions is 0")
	}

	_ = os.Remove(filepath.Join(incomingDir, "task-issue-1.yaml"))

	// Scenario B: Recovered completed task should be marked Completed immediately without subprocess run
	completedSandbox := newFakeSandbox("fix-repo-2", "ns", "Completed", "fix-issue")
	kubeClient := newFakeKubeClient([]runtime.Object{completedSandbox}...)

	wRecovered := &watchContext{
		ctx:             ctx,
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10},
		ghClient:        nil,
		kubeClient:      kubeClient,
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

	recoveredTask := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/2",
		Number:    2,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
		Recovered: true,
	}
	_ = writeTaskAtomically(incomingDir, "task-issue-2.yaml", recoveredTask)

	wRecovered.run(ctx, &wg, true)
	wg.Wait()

	// The recovered task should be marked as completed and moved to processed
	if _, err := os.Stat(filepath.Join(processedDir, "task-issue-2.yaml")); os.IsNotExist(err) {
		t.Errorf("expected recovered task-issue-2.yaml to be completed and moved to processed")
	}
}

func TestRunFailure(t *testing.T) {
	// Set the environment variable so any child subprocess calls exit with non-zero code
	os.Setenv("BE_HELPER_PROCESS", "fail")
	defer os.Unsetenv("BE_HELPER_PROCESS")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	t.Setenv("FACTORY_LOGS", tempDir)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
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
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10, TaskTimeout: 5 * time.Second},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		targetAssignee:  "factory-bot",
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	task := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/1",
		Number:    1,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
	}
	_ = writeTaskAtomically(incomingDir, "task-issue-1.yaml", task)

	var wg sync.WaitGroup
	w.run(ctx, &wg, true)
	wg.Wait()

	// Since task execution failed, it should still be moved to processed, but with status "Failed"
	processedPath := filepath.Join(processedDir, "task-issue-1.yaml")
	if _, err := os.Stat(processedPath); os.IsNotExist(err) {
		t.Fatalf("expected task-issue-1.yaml to be moved to processed even after failure")
	}
}

func TestRunTimeout(t *testing.T) {
	// Set helper process to block indefinitely (non-existent mode blocks/timeouts)
	os.Setenv("BE_HELPER_PROCESS", "block")
	defer os.Unsetenv("BE_HELPER_PROCESS")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	t.Setenv("FACTORY_LOGS", tempDir)

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
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
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10, TaskTimeout: 1 * time.Millisecond}, // Tiny timeout
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		targetAssignee:  "factory-bot",
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	task1 := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/1",
		Number:    1,
		Priority:  "high",
		Phase:     3,
		CreatedAt: time.Now(),
		Status:    "Pending",
		SessionID: "session-123",
	}
	_ = writeTaskAtomically(incomingDir, "task-issue-1.yaml", task1)

	task2 := &QueueTask{
		Type:      "agent-chore",
		URL:       "https://github.com/owner/repo/issues/2",
		Number:    2,
		Priority:  "medium",
		Phase:     4,
		CreatedAt: time.Now(),
		Status:    "Pending",
		SessionID: "session-456",
		AgentFile: "workflows/chore.yaml",
	}
	_ = writeTaskAtomically(incomingDir, "task-workflow-chore-issue-2.yaml", task2)

	task3 := &QueueTask{
		Type:      "agent-chore",
		URL:       "https://github.com/owner/repo/issues/3",
		Number:    3,
		Priority:  "medium",
		Phase:     4,
		CreatedAt: time.Now(),
		Status:    "Pending",
		AgentFile: "workflows/chore.yaml",
	}
	_ = writeTaskAtomically(incomingDir, "task-workflow-chore-issue-3.yaml", task3)

	var wg sync.WaitGroup
	w.run(ctx, &wg, true)
	wg.Wait()
}

func TestRunAllTaskTypes(t *testing.T) {
	tests := []struct {
		taskType  string
		url       string
		filename  string
		agentFile string
	}{
		{
			taskType: "pr-investigate",
			url:      "https://github.com/owner/repo/pull/1",
			filename: "task-pr-1-investigate.yaml",
		},
		{
			taskType: "pr-comments",
			url:      "https://github.com/owner/repo/pull/2",
			filename: "task-pr-2-comments.yaml",
		},
		{
			taskType: "pr-iterate",
			url:      "https://github.com/owner/repo/pull/3",
			filename: "task-pr-3-iterate.yaml",
		},
		{
			taskType: "pr-review",
			url:      "https://github.com/owner/repo/pull/4",
			filename: "task-pr-4-review.yaml",
		},
		{
			taskType:  "agent-chore",
			url:       "https://github.com/owner/repo/issues/5",
			filename:  "task-workflow-chore-issue-5.yaml",
			agentFile: "workflows/chore.yaml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.taskType, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempDir := t.TempDir()
			incomingDir := filepath.Join(tempDir, "incoming")
			processingDir := filepath.Join(tempDir, "processing")
			processedDir := filepath.Join(tempDir, "processed")
			_ = os.MkdirAll(incomingDir, 0755)
			_ = os.MkdirAll(processingDir, 0755)
			_ = os.MkdirAll(processedDir, 0755)

			t.Setenv("FACTORY_LOGS", tempDir)

			httpClient := &http.Client{
				Transport: mockRoundTripper(func(req *http.Request) *http.Response {
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
				opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10, TaskTimeout: 5 * time.Second},
				ghClient:        ghClient,
				kubeClient:      newFakeKubeClient(),
				processedIssues: make(map[int]time.Time),
				processedPRs:    make(map[int]prWatchState),
				incomingDir:     incomingDir,
				processingDir:   processingDir,
				processedDir:    processedDir,
				queueDir:        tempDir,
				targetAssignee:  "factory-bot",
				state: &watchState{
					referencedIssues: make(map[int]bool),
				},
			}

			task := &QueueTask{
				Type:      tc.taskType,
				URL:       tc.url,
				Number:    1,
				Priority:  "high",
				CreatedAt: time.Now(),
				Status:    "Pending",
				AgentFile: tc.agentFile,
			}
			_ = writeTaskAtomically(incomingDir, tc.filename, task)

			var wg sync.WaitGroup
			w.run(ctx, &wg, true)
			wg.Wait()

			// Check that it completes or moves to processed directory
			processedPath := filepath.Join(processedDir, tc.filename)
			if _, err := os.Stat(processedPath); os.IsNotExist(err) {
				t.Errorf("expected task file %s to be processed", tc.filename)
			}
		})
	}
}

func TestRunLogCreationError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Set FACTORY_LOGS to a path that is guaranteed to fail creation / opening
	t.Setenv("FACTORY_LOGS", "/nonexistent-root-dir/permission-denied")

	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
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
		opts:            Options{Owner: "owner", Repo: "repo", QueueDir: tempDir, Namespace: "ns", MaxActions: 10, MaxPending: 10, TaskTimeout: 5 * time.Second},
		ghClient:        ghClient,
		kubeClient:      newFakeKubeClient(),
		processedIssues: make(map[int]time.Time),
		processedPRs:    make(map[int]prWatchState),
		incomingDir:     incomingDir,
		processingDir:   processingDir,
		processedDir:    processedDir,
		queueDir:        tempDir,
		targetAssignee:  "factory-bot",
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	task := &QueueTask{
		Type:      "issue-fix",
		URL:       "https://github.com/owner/repo/issues/1",
		Number:    1,
		Priority:  "high",
		CreatedAt: time.Now(),
		Status:    "Pending",
	}
	_ = writeTaskAtomically(incomingDir, "task-issue-1.yaml", task)

	var wg sync.WaitGroup
	w.run(ctx, &wg, true)
	wg.Wait()

	// Verify that despite log creation failure, the task is still executed and moves to processed directory
	processedPath := filepath.Join(processedDir, "task-issue-1.yaml")
	if _, err := os.Stat(processedPath); os.IsNotExist(err) {
		t.Errorf("expected task-issue-1.yaml to be processed despite log creation failure")
	}
}
