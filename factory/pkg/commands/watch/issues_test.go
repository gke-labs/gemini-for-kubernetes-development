package watch

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestQueueIssueTasks(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	queueDir := tempDir
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Mock github client
	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/issues/10/timeline") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/search/issues") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"total_count":0,"items":[]}`)),
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

	now := time.Now()
	issues := []*githubv39.Issue{
		{
			Number:    githubv39.Int(10),
			Title:     githubv39.String("A test issue to fix"),
			Body:      githubv39.String("Please fix this bug"),
			CreatedAt: &now,
			Labels: []*githubv39.Label{
				{Name: githubv39.String("factory")},
			},
		},
	}

	processedIssues := make(map[int]time.Time)
	refIssues := make(map[int]bool)

	queueIssueTasks(
		ctx,
		ghClient,
		newFakeKubeClient(),
		nil,
		"owner",
		"repo",
		issues,
		processedIssues,
		refIssues,
		"factory-bot",
		[]string{"factory-bot"},
		incomingDir,
		processingDir,
		processedDir,
		queueDir,
		false,
		"factory",
		"ns",
	)

	// Verify that a task file was created
	filename := "task-issue-10.yaml"
	taskPath := filepath.Join(incomingDir, filename)
	if _, err := os.Stat(taskPath); os.IsNotExist(err) {
		t.Fatalf("expected task file %s to be created in incoming", filename)
	}

	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("failed to read task file: %v", err)
	}

	var task QueueTask
	if err := yaml.Unmarshal(data, &task); err != nil {
		t.Fatalf("failed to unmarshal generated task: %v", err)
	}

	if task.Type != "issue-fix" {
		t.Errorf("expected task type issue-fix, got %q", task.Type)
	}
	if task.Number != 10 {
		t.Errorf("expected task number 10, got %d", task.Number)
	}
}

func TestQueueIssueTasksLimitsAndWorkflows(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Mock github client
	httpClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "/repos/owner/repo/contents/.agents/my-workflow.yaml") {
				agentYAML := `---
name: "my-workflow"
schedule: "0 9 * * 1"
mode: workflow
---
Prompt`
				encoded := base64.StdEncoding.EncodeToString([]byte(agentYAML))
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"name":"my-workflow.yaml","type":"file","content":"` + encoded + `","encoding":"base64"}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/timeline") {
				if strings.Contains(req.URL.Path, "/issues/10/timeline") {
					// Returns a timeline with a cross-referenced open PR event
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(`[{"event":"cross-referenced","source":{"type":"issue","issue":{"number":101,"state":"open","pull_request":{}}}}]`)),
						Header:     make(http.Header),
					}
				}
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/search/issues") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"total_count":0,"items":[]}`)),
					Header:     make(http.Header),
				}
			}
			if strings.Contains(req.URL.Path, "/issues/30/labels") {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewBufferString("")),
				Header:     make(http.Header),
			}
		}),
	}
	ghClient := githubv39.NewClient(httpClient)

	now := time.Now()
	issues := []*githubv39.Issue{
		{
			Number:    githubv39.Int(5), // Less than MinNumber (10) -> skipped
			Title:     githubv39.String("Issue below MinNumber"),
			CreatedAt: &now,
			Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
		},
		{
			Number:    githubv39.Int(10), // Linked PR -> skipped
			Title:     githubv39.String("Issue with linked PR"),
			CreatedAt: &now,
			Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
		},
		{
			Number:    githubv39.Int(20), // Valid workflow issue -> queued
			Title:     githubv39.String("Workflow issue"),
			Body:      githubv39.String("Please run: .agents/my-workflow.yaml"),
			CreatedAt: &now,
			Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
		},
		{
			Number:    githubv39.Int(30), // Missing factory trigger label -> label added & queued
			Title:     githubv39.String("Issue missing label"),
			CreatedAt: &now,
			Labels:    []*githubv39.Label{},
		},
		{
			Number:    githubv39.Int(40), // In-flight sandbox -> skipped
			Title:     githubv39.String("Issue with busy sandbox"),
			CreatedAt: &now,
			Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
		},
	}

	processedIssues := make(map[int]time.Time)
	refIssues := make(map[int]bool)

	// Set up fake client with a running sandbox for issue 40
	runningSandbox := newFakeSandbox("fix-repo-40", "ns", "Running", "fix-issue")
	kubeClient := newFakeKubeClient([]runtime.Object{runningSandbox}...)

	cfg := &config.FactoryConfig{
		MinNumber: 10,
	}

	queueIssueTasks(
		ctx,
		ghClient,
		kubeClient,
		cfg,
		"owner",
		"repo",
		issues,
		processedIssues,
		refIssues,
		"factory-bot",
		[]string{"factory-bot"},
		incomingDir,
		processingDir,
		processedDir,
		tempDir,
		false,
		"factory",
		"ns",
	)

	// Issue 5 should be skipped (MinNumber)
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-5.yaml")); err == nil {
		t.Errorf("expected issue 5 to be skipped due to MinNumber")
	}

	// Issue 10 (linked PR) should be skipped
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-10.yaml")); err == nil {
		t.Errorf("expected issue 10 (linked PR) to be skipped")
	}

	// Issue 20 (workflow task) should be queued
	workflowFilename := "task-workflow-my-workflow-issue-20.yaml"
	if _, err := os.Stat(filepath.Join(incomingDir, workflowFilename)); os.IsNotExist(err) {
		t.Errorf("expected workflow task %s to be created", workflowFilename)
	}

	// Issue 30 (missing label) should be queued
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-30.yaml")); os.IsNotExist(err) {
		t.Errorf("expected issue 30 (trigger label added) to be queued")
	}

	// Issue 40 (busy sandbox) should be skipped
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-40.yaml")); err == nil {
		t.Errorf("expected issue 40 (busy sandbox) to be skipped")
	}

	// Move workflow task 20 to processed directory to check cooldown
	task20Incoming := filepath.Join(incomingDir, workflowFilename)
	task20Processed := filepath.Join(processedDir, workflowFilename)
	_ = os.Rename(task20Incoming, task20Processed)

	// Set modtime to now (within 10m cooldown)
	nowTime := time.Now()
	_ = os.Chtimes(task20Processed, nowTime, nowTime)

	// Run queueIssueTasks again
	queueIssueTasks(
		ctx,
		ghClient,
		kubeClient,
		cfg,
		"owner",
		"repo",
		issues,
		processedIssues,
		refIssues,
		"factory-bot",
		[]string{"factory-bot"},
		incomingDir,
		processingDir,
		processedDir,
		tempDir,
		false,
		"factory",
		"ns",
	)

	// Task 20 should NOT be recreated in incomingDir because it is within cooldown
	if _, err := os.Stat(task20Incoming); err == nil {
		t.Errorf("expected workflow task 20 NOT to be recreated (cooldown active)")
	}

	// Set modtime to 1 hour ago (outside cooldown)
	pastTime := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(task20Processed, pastTime, pastTime)

	// Run queueIssueTasks again
	queueIssueTasks(
		ctx,
		ghClient,
		kubeClient,
		cfg,
		"owner",
		"repo",
		issues,
		processedIssues,
		refIssues,
		"factory-bot",
		[]string{"factory-bot"},
		incomingDir,
		processingDir,
		processedDir,
		tempDir,
		false,
		"factory",
		"ns",
	)

	// Task 20 should now be recreated because cooldown expired
	if _, err := os.Stat(task20Incoming); os.IsNotExist(err) {
		t.Errorf("expected workflow task 20 to be recreated (cooldown expired)")
	}
}
