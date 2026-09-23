package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
)

func TestStartQueueHTTPServer(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskFile := filepath.Join(incomingDir, "task-1.yaml")
	if err := os.WriteFile(taskFile, []byte("type: issue-fix\npriority: low\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pick a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir: tempDir,
	})
	if err := mgr.LoadFromDisk(); err != nil {
		t.Fatal(err)
	}

	go startQueueHTTPServer(ctx, mgr, addr)

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Test GET /api/v1/queue
	getResp, err := http.Get(fmt.Sprintf("http://%s/api/v1/queue", addr))
	if err != nil {
		t.Fatalf("GET /api/v1/queue failed: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK, got %v", getResp.StatusCode)
	}
	var qResp api.QueueResponse
	if err := json.NewDecoder(getResp.Body).Decode(&qResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	_ = getResp.Body.Close()
	if qResp.Summary.TotalPending != 1 {
		t.Errorf("expected 1 pending task, got %d", qResp.Summary.TotalPending)
	}

	// Test POST /api/v1/queue/task-1.yaml/priority
	postBody := strings.NewReader(`{"priority":"urgent"}`)
	postResp, err := http.Post(fmt.Sprintf("http://%s/api/v1/queue/task-1.yaml/priority", addr), "application/json", postBody)
	if err != nil {
		t.Fatalf("POST priority failed: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK for update priority, got %v", postResp.StatusCode)
	}
	_ = postResp.Body.Close()

	// Verify file content updated
	updatedContent, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("failed to read updated task file: %v", err)
	}
	if !strings.Contains(string(updatedContent), "priority: urgent") {
		t.Errorf("expected priority: urgent in task file, got %s", string(updatedContent))
	}

	// Test DELETE /api/v1/queue/task-1.yaml
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://%s/api/v1/queue/task-1.yaml", addr), nil)
	if err != nil {
		t.Fatalf("failed to create DELETE request: %v", err)
	}
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE task failed: %v", err)
	}
	if delResp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK for DELETE, got %v", delResp.StatusCode)
	}
	_ = delResp.Body.Close()

	if _, err := os.Stat(taskFile); !os.IsNotExist(err) {
		t.Errorf("expected task-1.yaml to be deleted")
	}
}
