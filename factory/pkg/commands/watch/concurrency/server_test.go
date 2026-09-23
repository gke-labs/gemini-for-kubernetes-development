package concurrency

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/google/go-cmp/cmp"
)

func TestTaskQueueManager_GetQueueResponse(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	_ = mgr.Enqueue("task-issue-1.yaml", &api.QueueTask{Type: "issue-fix", Number: 1, Priority: "critical"})
	_ = mgr.Enqueue("task-issue-2.yaml", &api.QueueTask{Type: "issue-fix", Number: 2, Priority: "high"})
	_ = mgr.Enqueue("task-issue-3.yaml", &api.QueueTask{Type: "pr-review", Number: 3, Priority: "critical"})

	// Move one to processing
	_, claimed, _ := claimAndStartTask(mgr)

	completeFn := "task-issue-1.yaml"
	if claimed.Number != 1 {
		completeFn = fmt.Sprintf("task-issue-%d.yaml", claimed.Number)
	}
	_ = mgr.CompleteTask(completeFn, claimed)

	resp := mgr.GetQueueResponse()
	if resp.Summary.TotalPending != 2 {
		t.Errorf("expected 2 pending, got %d", resp.Summary.TotalPending)
	}
	if resp.Summary.TotalCompleted != 1 {
		t.Errorf("expected 1 completed, got %d", resp.Summary.TotalCompleted)
	}
	if len(resp.Incoming) != 2 {
		t.Errorf("expected 2 incoming items, got %d", len(resp.Incoming))
	}
	if resp.Incoming[0].Rank != 1 || resp.Incoming[1].Rank != 2 {
		t.Errorf("expected ranks 1 and 2, got %d and %d", resp.Incoming[0].Rank, resp.Incoming[1].Rank)
	}
}

func TestTaskQueueManager_GetQueueResponse_StartedCompletedDuration(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)
	processingDir := filepath.Join(queueDir, "processing")
	processedDir := filepath.Join(queueDir, "processed")

	startTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 8, 1, 10, 5, 30, 0, time.UTC)

	processingTask := &api.QueueTask{
		Type:      "pr-review",
		URL:       "https://github.com/owner/repo/pull/1",
		Number:    1,
		Status:    "Running",
		StartedAt: startTime,
		Priority:  "high",
		Phase:     2,
	}
	if err := writeTaskAtomically(processingDir, "task-pr-1-review.yaml", processingTask); err != nil {
		t.Fatalf("writeTaskAtomically processing failed: %v", err)
	}

	completedTask := &api.QueueTask{
		Type:        "issue-fix",
		URL:         "https://github.com/owner/repo/issues/2",
		Number:      2,
		Status:      "Completed",
		StartedAt:   startTime,
		CompletedAt: endTime,
		Priority:    "medium",
		Phase:       3,
	}
	if err := writeTaskAtomically(processedDir, "task-issue-2-fix.yaml", completedTask); err != nil {
		t.Fatalf("writeTaskAtomically processed failed: %v", err)
	}

	if err := mgr.LoadFromDisk(); err != nil {
		t.Fatalf("LoadFromDisk failed: %v", err)
	}
	resp := mgr.GetQueueResponse()
	if len(resp.Processing) != 1 {
		t.Fatalf("expected 1 processing task, got %d", len(resp.Processing))
	}
	if resp.Processing[0].StartedAt != startTime.Format(time.RFC3339) {
		t.Errorf("expected processing startedAt %s, got %s", startTime.Format(time.RFC3339), resp.Processing[0].StartedAt)
	}

	if len(resp.Processed) != 1 {
		t.Fatalf("expected 1 processed task, got %d", len(resp.Processed))
	}
	p := resp.Processed[0]
	if p.StartedAt != startTime.Format(time.RFC3339) {
		t.Errorf("expected processed startedAt %s, got %s", startTime.Format(time.RFC3339), p.StartedAt)
	}
	if p.CompletedAt != endTime.Format(time.RFC3339) {
		t.Errorf("expected processed completedAt %s, got %s", endTime.Format(time.RFC3339), p.CompletedAt)
	}
	expectedDuration := float64(330) // 5 minutes 30 seconds
	if p.DurationSeconds != expectedDuration {
		t.Errorf("expected durationSeconds %v, got %v", expectedDuration, p.DurationSeconds)
	}
}

func TestTaskQueueManager_UpdateTaskPriority(t *testing.T) {
	mgr, _ := setupTestQueueManager(t)

	fn := "task-issue-42.yaml"
	_ = mgr.Enqueue(fn, &api.QueueTask{Type: "issue-fix", Number: 42, Priority: "low"})

	if err := mgr.UpdateTaskPriority(fn, "critical"); err != nil {
		t.Fatalf("UpdateTaskPriority failed: %v", err)
	}

	task, ok := getTaskFromQueueResponse(mgr, fn)
	if !ok || task.QueueState != "incoming" || task.Priority != "critical" {
		t.Errorf("expected priority critical, got %+v", task)
	}
}

func TestTaskQueueManager_UpdateTaskPriority_FallbackDisk(t *testing.T) {
	mgr, queueDir := setupTestQueueManager(t)
	incomingDir := filepath.Join(queueDir, "incoming")

	fn := "task-direct-priority.yaml"
	task := &api.QueueTask{Type: "issue-fix", Number: 201, Priority: "low"}
	if err := writeTaskAtomically(incomingDir, fn, task); err != nil {
		t.Fatalf("failed to write task to disk: %v", err)
	}

	// UpdateTaskPriority should fall back to disk and update priority
	if err := mgr.UpdateTaskPriority(fn, "critical"); err != nil {
		t.Fatalf("UpdateTaskPriority disk fallback failed: %v", err)
	}

	updated, ok := getTaskFromQueueResponse(mgr, fn)
	if !ok || updated.QueueState != "incoming" || updated.Priority != "critical" {
		t.Errorf("expected updated task in incoming with critical priority, got: %+v", updated)
	}
}

func TestBuildQueueResponse(t *testing.T) {
	t.Run("Empty and non-existent directories", func(t *testing.T) {
		tempDir := t.TempDir()
		resp := newTestQueueManager(tempDir).GetQueueResponse()

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

		resp := newTestQueueManager(tempDir).GetQueueResponse()
		if len(resp.Incoming) != 2 {
			t.Fatalf("expected 2 incoming tasks, got %d", len(resp.Incoming))
		}

		if resp.Incoming[1].EnqueuedAt == "" {
			t.Errorf("expected non-empty fallback enqueuedAt for task 2")
		}

		expectedIncoming := []api.QueueTaskItem{
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

		resp := newTestQueueManager(tempDir).GetQueueResponse()
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

	t.Run("Priority queue ordering across multiple entities", func(t *testing.T) {
		tempDir := t.TempDir()
		incomingDir := filepath.Join(tempDir, "incoming")
		if err := os.MkdirAll(incomingDir, 0755); err != nil {
			t.Fatal(err)
		}

		baseTime := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

		// 3 tasks for PR 10
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_1.yaml"), fmt.Appendf(nil, "number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(1*time.Minute).Format(time.RFC3339)), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_2.yaml"), fmt.Appendf(nil, "number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(3*time.Minute).Format(time.RFC3339)), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr10_3.yaml"), fmt.Appendf(nil, "number: 10\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(4*time.Minute).Format(time.RFC3339)), 0644)

		// 2 tasks for PR 20
		_ = os.WriteFile(filepath.Join(incomingDir, "pr20_1.yaml"), fmt.Appendf(nil, "number: 20\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(5*time.Minute).Format(time.RFC3339)), 0644)
		_ = os.WriteFile(filepath.Join(incomingDir, "pr20_2.yaml"), fmt.Appendf(nil, "number: 20\ntype: pr-comments\npriority: medium\nphase: 3\nenqueuedAt: %s\n", baseTime.Add(6*time.Minute).Format(time.RFC3339)), 0644)

		resp := newTestQueueManager(tempDir).GetQueueResponse()
		if len(resp.Incoming) != 5 {
			t.Fatalf("expected 5 incoming tasks, got %d", len(resp.Incoming))
		}

		// Expected priority queue order (all same priority and phase -> ordered by enqueuedAt FIFO)
		expectedOrder := []string{"pr10_1.yaml", "pr10_2.yaml", "pr10_3.yaml", "pr20_1.yaml", "pr20_2.yaml"}
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

		resp := newTestQueueManager(tempDir).GetQueueResponse()

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

		resp := newTestQueueManager(tempDir).GetQueueResponse()

		if len(resp.Processed) != 20 {
			t.Errorf("expected processed capped at 20 items, got %d", len(resp.Processed))
		}

		if resp.Summary.TotalCompleted != 20 {
			t.Errorf("expected totalCompleted 20 in summary, got %v", resp.Summary.TotalCompleted)
		}
	})
}

func TestBuildQueueResponseTriggerFields(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewTaskQueueManager(TaskQueueManagerConfig{
		QueueDir: tempDir,
	})

	eventTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	enqueuedTime := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)

	task := &api.QueueTask{
		Type:             api.TypePRComments,
		URL:              "https://github.com/owner/repo/pull/123",
		Number:           123,
		Priority:         api.PriorityMedium,
		Phase:            api.PhaseIterate,
		CreatedAt:        eventTime,
		EnqueuedAt:       enqueuedTime,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCommentsAdded,
		TriggerNotes:     "Oldest comment by alice added at 2026-08-01T10:00:00Z",
		Status:           api.StatusPending,
	}

	if err := mgr.Enqueue("task-pr-123-comments.yaml", task); err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	resp := mgr.GetQueueResponse()
	if len(resp.Incoming) != 1 {
		t.Fatalf("expected 1 incoming task, got %d", len(resp.Incoming))
	}

	item := resp.Incoming[0]
	if item.TriggerEventTime != eventTime.Format(time.RFC3339) {
		t.Errorf("expected item TriggerEventTime %s, got %s", eventTime.Format(time.RFC3339), item.TriggerEventTime)
	}
	if item.TriggerReason != api.TriggerReasonPRCommentsAdded {
		t.Errorf("expected item TriggerReason '%s', got '%s'", api.TriggerReasonPRCommentsAdded, item.TriggerReason)
	}
	if item.TriggerNotes != "Oldest comment by alice added at 2026-08-01T10:00:00Z" {
		t.Errorf("expected item TriggerNotes 'Oldest comment by alice added at 2026-08-01T10:00:00Z', got '%s'", item.TriggerNotes)
	}
}

func getTaskFromQueueResponse(mgr *TaskQueueManager, filename string) (api.QueueTaskItem, bool) {
	resp := mgr.GetQueueResponse()
	for _, item := range resp.Incoming {
		if item.FileName == filename {
			return item, true
		}
	}
	for _, item := range resp.Processing {
		if item.FileName == filename {
			return item, true
		}
	}
	for _, item := range resp.Processed {
		if item.FileName == filename {
			return item, true
		}
	}
	return api.QueueTaskItem{}, false
}

func newTestQueueManager(queueDir string) *TaskQueueManager {
	mgr := NewTaskQueueManager(TaskQueueManagerConfig{
		QueueDir: queueDir,
	})
	_ = mgr.LoadFromDisk()
	return mgr
}
