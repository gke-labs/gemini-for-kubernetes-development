package watch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	githubv39 "github.com/google/go-github/v39/github"
)

func TestRunWatch(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	// Backup package variables
	origNewGH := newGitHubClient
	origNewKube := newKubernetesClient
	defer func() {
		newGitHubClient = origNewGH
		newKubernetesClient = origNewKube
	}()

	// Mock github client
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

	// Mock kube clientset to return a secret using the package variable
	fakeKubeSecretData = `{"metadata":{"name":"test-secret","namespace":"test-ns"},"data":{"GITHUB_LOGIN":"ZmFjdG9yeS1ib3Q="}}`
	defer func() { fakeKubeSecretData = "" }()

	kubeClient := newFakeKubeClient()

	// Override package vars
	newGitHubClient = func(ctx context.Context) (*githubv39.Client, error) {
		return ghClient, nil
	}
	newKubernetesClient = func() (*clients.KubernetesClient, error) {
		return kubeClient, nil
	}

	// Pre-create directories and pre-populate files to trigger recovery and processed history loading
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	// Pre-populate stuck task to trigger recovery block
	stuckTask := &QueueTask{
		Type:     "issue-fix",
		URL:      "https://github.com/owner/repo/issues/15",
		Number:   15,
		Priority: "medium",
		Status:   "Running",
	}
	_ = writeTaskAtomically(processingDir, "task-issue-15.yaml", stuckTask)

	// Pre-populate processed issue task
	processedTask := &QueueTask{
		Type:     "issue-fix",
		URL:      "https://github.com/owner/repo/issues/16",
		Number:   16,
		Priority: "medium",
		Status:   "Completed",
	}
	_ = writeTaskAtomically(processedDir, "task-issue-16.yaml", processedTask)

	// Pre-populate processed PR task
	processedPRTask := &QueueTask{
		Type:      "pr-iterate",
		URL:       "https://github.com/owner/repo/pull/17",
		Number:    17,
		Priority:  "medium",
		Status:    "Completed",
		CommitSHA: "sha17",
	}
	_ = writeTaskAtomically(processedDir, "task-pr-17-comments.yaml", processedPRTask)

	opts := Options{
		Owner:      "owner",
		Repo:       "repo",
		SecretName: "test-secret",
		Namespace:  "test-ns",
		Once:       true,
		DryRun:     true,
		QueueDir:   tempDir,
		Mode:       "all",
		PRMode:     "disabled",
		IssueMode:  "disabled",
	}

	err := RunWatch(ctx, opts)
	if err != nil {
		t.Fatalf("RunWatch returned unexpected error: %v", err)
	}

	// Verify that stuck task 15 was recovered back to incoming
	if _, err := os.Stat(filepath.Join(incomingDir, "task-issue-15.yaml")); os.IsNotExist(err) {
		t.Errorf("expected stuck task 15 to be recovered to incoming directory")
	}
}

func TestRunWatchLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &watchContext{
		ctx:  ctx,
		opts: Options{Once: false, WatchTimeout: 1 * time.Second},
		state: &watchState{
			referencedIssues: make(map[int]bool),
		},
	}

	var wg sync.WaitGroup

	// Scenario A: Ticker triggers checkRepo
	calledCount := 0
	checkRepoMock := func() {
		calledCount++
	}

	tickChan := make(chan time.Time, 1)
	timeoutChan := make(chan time.Time, 1)

	// Start runLoop in background
	errChan := make(chan error, 1)
	go func() {
		errChan <- w.runLoop(ctx, &wg, checkRepoMock, tickChan, timeoutChan)
	}()

	// 1. First checkRepo is called immediately upon entering runLoop
	time.Sleep(10 * time.Millisecond) // short wait to allow scheduler to run the goroutine
	if calledCount != 1 {
		t.Errorf("expected initial checkRepo call, got %d", calledCount)
	}

	// 2. Trigger ticker
	tickChan <- time.Now()
	time.Sleep(10 * time.Millisecond)
	if calledCount != 2 {
		t.Errorf("expected second checkRepo call after tick, got %d", calledCount)
	}

	// Scenario B: Timeout trigger
	timeoutChan <- time.Now()
	err := <-errChan
	if err != nil {
		t.Errorf("expected nil error on timeout exit, got %v", err)
	}

	// Scenario C: Cancelled context exit
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2() // cancel immediately

	err2 := w.runLoop(ctx2, &wg, func() {}, tickChan, timeoutChan)
	if err2 != context.Canceled {
		t.Errorf("expected context.Canceled error, got %v", err2)
	}
}

func TestRunWatchErrors(t *testing.T) {
	ctx := context.Background()

	// Backup package variables
	origNewGH := newGitHubClient
	origNewKube := newKubernetesClient
	defer func() {
		newGitHubClient = origNewGH
		newKubernetesClient = origNewKube
	}()

	// 1. GitHub client creation fails
	newGitHubClient = func(ctx context.Context) (*githubv39.Client, error) {
		return nil, fmt.Errorf("github construction error")
	}
	opts1 := Options{Owner: "owner", Repo: "repo", SecretName: "s", Namespace: "ns", Once: true}
	err := RunWatch(ctx, opts1)
	if err == nil || !strings.Contains(err.Error(), "github construction error") {
		t.Errorf("expected github construction error, got %v", err)
	}

	// Restore github client for next sub-tests
	newGitHubClient = func(ctx context.Context) (*githubv39.Client, error) {
		return githubv39.NewClient(nil), nil
	}

	// 2. Kubernetes client creation fails
	newKubernetesClient = func() (*clients.KubernetesClient, error) {
		return nil, fmt.Errorf("k8s construction error")
	}
	err = RunWatch(ctx, opts1)
	if err == nil || !strings.Contains(err.Error(), "k8s construction error") {
		t.Errorf("expected k8s construction error, got %v", err)
	}

	// Restore k8s client for next sub-tests
	newKubernetesClient = func() (*clients.KubernetesClient, error) {
		return newFakeKubeClient(), nil
	}

	// 3. Secrets fetch fails (returns 404 for nonexistent secret)
	err = RunWatch(ctx, opts1)
	if err == nil || !strings.Contains(err.Error(), "fetching s secret in namespace ns") {
		t.Errorf("expected secret fetch error, got %v", err)
	}

	// 4. MkdirAll fails
	fakeKubeSecretData = `{"metadata":{"name":"test-secret","namespace":"test-ns"},"data":{"GITHUB_LOGIN":"ZmFjdG9yeS1ib3Q="}}`
	defer func() { fakeKubeSecretData = "" }()

	optsMkdir := Options{
		Owner:      "owner",
		Repo:       "repo",
		SecretName: "test-secret",
		Namespace:  "test-ns",
		Once:       true,
		DryRun:     false, // must be false to trigger MkdirAll
		QueueDir:   "/nonexistent-dir-for-mkdir-failure/invalid",
		Mode:       "all",
	}

	err = RunWatch(ctx, optsMkdir)
	if err == nil || !strings.Contains(err.Error(), "failed to create incoming queue dir") {
		t.Errorf("expected mkdir failure error, got %v", err)
	}
}

func TestWatcherIsDoNotProcess(t *testing.T) {
	tempDir := t.TempDir()
	w := &watchContext{
		queueDir: tempDir,
	}

	// 1. Scenario A: default no drain
	if w.isDoNotProcess() {
		t.Errorf("expected isDoNotProcess to be false by default")
	}

	// 2. Scenario B: environment variable trigger
	t.Setenv("DRAIN", "true")
	if !w.isDoNotProcess() {
		t.Errorf("expected isDoNotProcess to be true when DRAIN=true env var is set")
	}
	t.Setenv("DRAIN", "") // clear

	// 3. Scenario C: check file trigger (.drain)
	drainFile := filepath.Join(tempDir, ".drain")
	_ = os.WriteFile(drainFile, []byte(""), 0644)
	if !w.isDoNotProcess() {
		t.Errorf("expected isDoNotProcess to be true when .drain file exists")
	}
	_ = os.Remove(drainFile)

	// 4. Scenario D: other env vars
	envVars := []string{"DO_NOT_PROCESS", "FACTORY_DO_NOT_PROCESS", "FACTORY_DRAIN"}
	for _, env := range envVars {
		t.Setenv(env, "true")
		if !w.isDoNotProcess() {
			t.Errorf("expected isDoNotProcess to be true when %s=true", env)
		}
		t.Setenv(env, "")
	}

	// 5. Scenario E: multiple drain file paths
	subPaths := []string{
		".do_not_process",
		"do_not_process",
		"drain",
	}
	for _, sub := range subPaths {
		p := filepath.Join(tempDir, sub)
		err := os.WriteFile(p, []byte(""), 0644)
		if err != nil {
			t.Fatalf("failed to write drain file %s: %v", p, err)
		}
		if !w.isDoNotProcess() {
			t.Errorf("expected isDoNotProcess to be true when drain file %s exists", p)
		}
		_ = os.Remove(p)
	}
}
