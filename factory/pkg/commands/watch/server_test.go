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

	"github.com/google/go-cmp/cmp"
)

func TestBuildQueueResponse(t *testing.T) {
	t.Run("Empty and non-existent directories", func(t *testing.T) {
		tempDir := t.TempDir()
		resp := buildQueueResponse(tempDir)

		if resp.Summary.TotalPending != 0 {
			t.Errorf("expected totalPending 0, got %d", resp.Summary.TotalPending)
		}
		if resp.Summary.TotalProcessing != 0 {
			t.Errorf("expected totalProcessing 0, got %d", resp.Summary.TotalProcessing)
		}
		if resp.Summary.TotalCompleted != 0 {
			t.Errorf("expected totalCompleted 0, got %d", resp.Summary.TotalCompleted)
		}
		if len(resp.Incoming) != 0 || len(resp.Processing) != 0 || len(resp.Processed) != 0 {
			t.Errorf("expected empty slices, got incoming=%d, processing=%d, processed=%d",
				len(resp.Incoming), len(resp.Processing), len(resp.Processed))
		}
	})

	t.Run("Field extraction and defaults", func(t *testing.T) {
		tempDir := t.TempDir()
		incomingDir := filepath.Join(tempDir, "incoming")
		if err := os.MkdirAll(incomingDir, 0755); err != nil {
			t.Fatal(err)
		}

		task1Data := `
type: pr-review
url: "https://github.com/org/repo/pull/42"
number: 42
priority: critical
phase: 2
createdAt: "2026-08-10T10:00:00Z"
enqueuedAt: "2026-08-10T10:05:00Z"
assignee: alice
status: Pending
commitSHA: "abc123def"
`
		task2Data := `
type: issue-fix
url: "https://github.com/org/repo/issues/99"
number: 99
status: Pending
createdAt: "2026-08-10T11:00:00Z"
`
		if err := os.WriteFile(filepath.Join(incomingDir, "task1.yaml"), []byte(task1Data), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(incomingDir, "task2.yaml"), []byte(task2Data), 0644); err != nil {
			t.Fatal(err)
		}

		resp := buildQueueResponse(tempDir)
		if len(resp.Incoming) != 2 {
			t.Fatalf("expected 2 incoming tasks, got %d", len(resp.Incoming))
		}

		if resp.Incoming[1].EnqueuedAt == "" {
			t.Errorf("expected non-empty fallback enqueuedAt for task 2")
		}

		expectedIncoming := []QueueTaskItem{
			{
				FileName:   "task1.yaml",
				QueueState: "incoming",
				Type:       "pr-review",
				URL:        "https://github.com/org/repo/pull/42",
				Number:     42,
				Priority:   "critical",
				Phase:      2,
				CreatedAt:  "2026-08-10T10:00:00Z",
				EnqueuedAt: "2026-08-10T10:05:00Z",
				Assignee:   "alice",
				Status:     "Pending",
				CommitSHA:  "abc123def",
				Rank:       1,
			},
			{
				FileName:   "task2.yaml",
				QueueState: "incoming",
				Type:       "issue-fix",
				URL:        "https://github.com/org/repo/issues/99",
				Number:     99,
				Priority:   "medium",
				Phase:      0,
				CreatedAt:  "2026-08-10T11:00:00Z",
				EnqueuedAt: resp.Incoming[1].EnqueuedAt,
				Assignee:   "",
				Status:     "Pending",
				CommitSHA:  "",
				Rank:       2,
			},
		}

		if diff := cmp.Diff(expectedIncoming, resp.Incoming); diff != "" {
			t.Errorf("incoming tasks mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("Sorting by priority, phase, and createdAt", func(t *testing.T) {
		tempDir := t.TempDir()
		incomingDir := filepath.Join(tempDir, "incoming")
		if err := os.MkdirAll(incomingDir, 0755); err != nil {
			t.Fatal(err)
		}

		tasks := []struct {
			filename string
			priority string
			phase    int
			created  string
		}{
			{"low.yaml", "low", 1, "2026-08-10T01:00:00Z"},
			{"med_ph2.yaml", "medium", 2, "2026-08-10T01:00:00Z"},
			{"crit_later.yaml", "critical", 1, "2026-08-10T02:00:00Z"},
			{"urgent.yaml", "urgent", 1, "2026-08-10T01:00:00Z"},
			{"med_ph1_earlier.yaml", "medium", 1, "2026-08-10T01:00:00Z"},
			{"crit_earlier.yaml", "critical", 1, "2026-08-10T01:00:00Z"},
			{"important.yaml", "important", 1, "2026-08-10T01:00:00Z"},
			{"high.yaml", "high", 1, "2026-08-10T01:00:00Z"},
			{"med_ph1_later.yaml", "medium", 1, "2026-08-10T02:00:00Z"},
		}

		for _, task := range tasks {
			content := fmt.Sprintf("type: pr-review\npriority: %s\nphase: %d\ncreatedAt: %s\nenqueuedAt: %s\n", task.priority, task.phase, task.created, task.created)
			if err := os.WriteFile(filepath.Join(incomingDir, task.filename), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		resp := buildQueueResponse(tempDir)
		if len(resp.Incoming) != len(tasks) {
			t.Fatalf("expected %d tasks, got %d", len(tasks), len(resp.Incoming))
		}

		expectedOrder := []string{
			"crit_earlier.yaml",
			"crit_later.yaml",
			"urgent.yaml",
			"important.yaml",
			"high.yaml",
			"med_ph1_earlier.yaml",
			"med_ph1_later.yaml",
			"med_ph2.yaml",
			"low.yaml",
		}

		for i, exp := range expectedOrder {
			gotFile := resp.Incoming[i].FileName
			gotRank := resp.Incoming[i].Rank
			if gotFile != exp {
				t.Errorf("at index %d: expected %s, got %v", i, exp, gotFile)
			}
			if gotRank != i+1 {
				t.Errorf("at index %d: expected rank %d, got %v", i, i+1, gotRank)
			}
		}
	})

	t.Run("Fair round-robin across multiple entities matching watch loop", func(t *testing.T) {
		tempDir := t.TempDir()
		incomingDir := filepath.Join(tempDir, "incoming")
		if err := os.MkdirAll(incomingDir, 0755); err != nil {
			t.Fatal(err)
		}

		baseTime := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

		// 3 tasks for PR 10
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_1.yaml"), []byte(fmt.Sprintf("number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(1*time.Minute).Format(time.RFC3339))), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_2.yaml"), []byte(fmt.Sprintf("number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(3*time.Minute).Format(time.RFC3339))), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_3.yaml"), []byte(fmt.Sprintf("number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(4*time.Minute).Format(time.RFC3339))), 0644)

		// 2 tasks for PR 20
		_ = os.WriteFile(filepath.Join(incomingDir, "pr20_1.yaml"), []byte(fmt.Sprintf("number: 20\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(5*time.Minute).Format(time.RFC3339))), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr20_2.yaml"), []byte(fmt.Sprintf("number: 20\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(6*time.Minute).Format(time.RFC3339))), 0644)

		resp := buildQueueResponse(tempDir)
		if len(resp.Incoming) != 5 {
			t.Fatalf("expected 5 incoming tasks, got %d", len(resp.Incoming))
		}

		// Expected round-robin order between PR 10 and PR 20
		expectedOrder := []string{"pr10_1.yaml", "pr20_1.yaml", "pr10_2.yaml", "pr20_2.yaml", "pr10_3.yaml"}
		for i, exp := range expectedOrder {
			if resp.Incoming[i].FileName != exp {
				t.Errorf("at index %d: expected %s, got %v", i, exp, resp.Incoming[i].FileName)
			}
			if resp.Incoming[i].Rank != i+1 {
				t.Errorf("at index %d: expected rank %d, got %v", i, i+1, resp.Incoming[i].Rank)
			}
		}
	})

	t.Run("Summary counts and breakdowns", func(t *testing.T) {
		tempDir := t.TempDir()
		incomingDir := filepath.Join(tempDir, "incoming")
		processingDir := filepath.Join(tempDir, "processing")
		processedDir := filepath.Join(tempDir, "processed")

		for _, d := range []string{incomingDir, processingDir, processedDir} {
			if err := os.MkdirAll(d, 0755); err != nil {
				t.Fatal(err)
			}
		}

		// 3 incoming tasks (2 critical pr-review, 1 high issue-fix)
		_ = os.WriteFile(filepath.Join(incomingDir, "task1.yaml"), []byte("type: pr-review\npriority: critical\n"), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "task2.yaml"), []byte("type: pr-review\npriority: critical\n"), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "task3.yaml"), []byte("type: issue-fix\npriority: high\n"), 0644)

		// 2 processing tasks
		_ = os.WriteFile(filepath.Join(processingDir, "proc1.yaml"), []byte("type: pr-review\npriority: critical\n"), 0644)
		_ = os.WriteFile(filepath.Join(processingDir, "proc2.yaml"), []byte("type: agent-chore\npriority: low\n"), 0644)

		// 1 processed task
		_ = os.WriteFile(filepath.Join(processedDir, "done1.yaml"), []byte("type: pr-review\nstatus: Completed\n"), 0644)

		resp := buildQueueResponse(tempDir)

		if resp.Summary.TotalPending != 3 {
			t.Errorf("expected totalPending 3, got %v", resp.Summary.TotalPending)
		}
		if resp.Summary.TotalProcessing != 2 {
			t.Errorf("expected totalProcessing 2, got %v", resp.Summary.TotalProcessing)
		}
		if resp.Summary.TotalCompleted != 1 {
			t.Errorf("expected totalCompleted 1, got %v", resp.Summary.TotalCompleted)
		}

		if resp.Summary.ByPriority["critical"] != 2 || resp.Summary.ByPriority["high"] != 1 {
			t.Errorf("expected byPriority map [critical:2, high:1], got %+v", resp.Summary.ByPriority)
		}

		if resp.Summary.ByType["pr-review"] != 2 || resp.Summary.ByType["issue-fix"] != 1 {
			t.Errorf("expected byType map [pr-review:2, issue-fix:1], got %+v", resp.Summary.ByType)
		}
	})

	t.Run("Processed queue capping at 20", func(t *testing.T) {
		tempDir := t.TempDir()
		processedDir := filepath.Join(tempDir, "processed")
		if err := os.MkdirAll(processedDir, 0755); err != nil {
			t.Fatal(err)
		}

		for i := 1; i <= 30; i++ {
			fn := fmt.Sprintf("task-%02d.yaml", i)
			_ = os.WriteFile(filepath.Join(processedDir, fn), []byte("type: pr-review\nstatus: Completed\n"), 0644)
		}

		resp := buildQueueResponse(tempDir)

		if len(resp.Processed) != 20 {
			t.Errorf("expected processed capped at 20 items, got %d", len(resp.Processed))
		}

		if resp.Summary.TotalCompleted != 20 {
			t.Errorf("expected totalCompleted 20 in summary, got %v", resp.Summary.TotalCompleted)
		}
	})
}

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

	go startQueueHTTPServer(ctx, tempDir, addr)

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
	var qResp QueueResponse
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
