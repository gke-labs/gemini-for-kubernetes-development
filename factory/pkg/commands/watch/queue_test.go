package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	githubv39 "github.com/google/go-github/v39/github"
	"sigs.k8s.io/yaml"
)

func TestParseProcessedPRTask(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Create a review task file
	reviewTaskPath := filepath.Join(tempDir, "task-pr-123-review.yaml")
	reviewTaskData := []byte(`
type: pr-review
commitSHA: "abcd123"
`)
	if err := os.WriteFile(reviewTaskPath, reviewTaskData, 0644); err != nil {
		t.Fatalf("failed to write review task file: %v", err)
	}

	// 2. Create a comments task file
	commentsTaskPath := filepath.Join(tempDir, "task-pr-123-comments.yaml")
	commentsTaskData := []byte(`
type: pr-comments
commitSHA: "csha789"
completedAt: "2026-07-23T12:00:00Z"
`)
	if err := os.WriteFile(commentsTaskPath, commentsTaskData, 0644); err != nil {
		t.Fatalf("failed to write comments task file: %v", err)
	}

	// 3. Create an investigate task file
	investigateTaskPath := filepath.Join(tempDir, "task-pr-123-investigate.yaml")
	investigateTaskData := []byte(`
type: pr-investigate
commitSHA: "invsha123"
completedAt: "2026-07-23T13:00:00Z"
`)
	if err := os.WriteFile(investigateTaskPath, investigateTaskData, 0644); err != nil {
		t.Fatalf("failed to write investigate task file: %v", err)
	}

	// 4. Create an iterate task file
	iterateTaskPath := filepath.Join(tempDir, "task-pr-123-iterate.yaml")
	iterateTaskData := []byte(`
type: pr-iterate
commitSHA: "efgh456"
completedAt: "2026-07-23T14:00:00Z"
`)
	if err := os.WriteFile(iterateTaskPath, iterateTaskData, 0644); err != nil {
		t.Fatalf("failed to write iterate task file: %v", err)
	}

	initialState := prWatchState{}

	// Process review task
	fInfoReview, _ := os.Stat(reviewTaskPath)
	state := parseProcessedPRTask(reviewTaskPath, "task-pr-123-review", fInfoReview, initialState)
	if state.lastReviewedSHA != "abcd123" {
		t.Errorf("expected lastReviewedSHA to be 'abcd123', got '%s'", state.lastReviewedSHA)
	}

	// Process comments task
	fInfoComments, _ := os.Stat(commentsTaskPath)
	state = parseProcessedPRTask(commentsTaskPath, "task-pr-123-comments", fInfoComments, state)
	expectedCommentTime, _ := time.Parse(time.RFC3339, "2026-07-23T12:00:00Z")
	if !state.lastCommentAddressedTime.Equal(expectedCommentTime) {
		t.Errorf("expected lastCommentAddressedTime to be %v, got %v", expectedCommentTime, state.lastCommentAddressedTime)
	}
	if state.lastCommentAddressedSHA != "csha789" {
		t.Errorf("expected lastCommentAddressedSHA to be 'csha789', got '%s'", state.lastCommentAddressedSHA)
	}

	// Process investigate task
	fInfoInvestigate, _ := os.Stat(investigateTaskPath)
	state = parseProcessedPRTask(investigateTaskPath, "task-pr-123-investigate", fInfoInvestigate, state)
	expectedInvestigateTime, _ := time.Parse(time.RFC3339, "2026-07-23T13:00:00Z")
	if !state.lastInvestigatedTime.Equal(expectedInvestigateTime) {
		t.Errorf("expected lastInvestigatedTime to be %v, got %v", expectedInvestigateTime, state.lastInvestigatedTime)
	}
	if state.lastInvestigatedSHA != "invsha123" {
		t.Errorf("expected lastInvestigatedSHA to be 'invsha123', got '%s'", state.lastInvestigatedSHA)
	}

	// Process iterate task
	fInfoIterate, _ := os.Stat(iterateTaskPath)
	state = parseProcessedPRTask(iterateTaskPath, "task-pr-123-iterate", fInfoIterate, state)
	expectedIterateTime, _ := time.Parse(time.RFC3339, "2026-07-23T14:00:00Z")
	if !state.lastIteratedTime.Equal(expectedIterateTime) {
		t.Errorf("expected lastIteratedTime to be %v, got %v", expectedIterateTime, state.lastIteratedTime)
	}
	if state.lastIteratedSHA != "efgh456" {
		t.Errorf("expected lastIteratedSHA to be 'efgh456', got '%s'", state.lastIteratedSHA)
	}

	// 5. Test that a Failed task is ignored
	failedTaskPath := filepath.Join(tempDir, "task-pr-123-comments-failed.yaml")
	failedTaskData := []byte(`
type: pr-comments
status: Failed
completedAt: "2026-07-23T20:00:00Z"
`)
	_ = os.WriteFile(failedTaskPath, failedTaskData, 0644)
	fInfoFailed, _ := os.Stat(failedTaskPath)
	state = parseProcessedPRTask(failedTaskPath, "task-pr-123-comments", fInfoFailed, state)
	if !state.lastCommentAddressedTime.Equal(expectedCommentTime) {
		t.Errorf("expected lastCommentAddressedTime to remain unchanged when task is Failed, got %v", state.lastCommentAddressedTime)
	}
}

func TestSortTasksFairly(t *testing.T) {
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	t.Run("FIFO within single entity prevents LIFO starvation", func(t *testing.T) {
		task1 := api.TaskItem{
			Filename: "task1.yaml",
			Task: &api.QueueTask{
				Type:       api.TypePRComments,
				Number:     10,
				Priority:   api.PriorityMedium,
				Phase:      api.PhaseComments,
				CreatedAt:  baseTime,
				EnqueuedAt: baseTime.Add(1 * time.Minute),
			},
		}
		task2 := api.TaskItem{
			Filename: "task2.yaml",
			Task: &api.QueueTask{
				Type:       api.TypePRComments,
				Number:     10,
				Priority:   api.PriorityMedium,
				Phase:      api.PhaseComments,
				CreatedAt:  baseTime.Add(1 * time.Hour),
				EnqueuedAt: baseTime.Add(2 * time.Minute),
			},
		}
		task3 := api.TaskItem{
			Filename: "task3.yaml",
			Task: &api.QueueTask{
				Type:       api.TypePRComments,
				Number:     10,
				Priority:   api.PriorityMedium,
				Phase:      api.PhaseComments,
				CreatedAt:  baseTime.Add(2 * time.Hour),
				EnqueuedAt: baseTime.Add(3 * time.Minute),
			},
		}

		items := []api.TaskItem{task3, task2, task1}
		got := sortTasksFairly(items)

		expectedOrder := []string{"task1.yaml", "task2.yaml", "task3.yaml"}
		for i, expected := range expectedOrder {
			if got[i].Filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].Filename)
			}
		}
	})

	t.Run("Round-Robin across entities prevents entity starvation", func(t *testing.T) {
		pr10Task1 := api.TaskItem{
			Filename: "pr10_1.yaml",
			Task:     &api.QueueTask{Number: 10, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(1 * time.Minute)},
		}
		pr10Task2 := api.TaskItem{
			Filename: "pr10_2.yaml",
			Task:     &api.QueueTask{Number: 10, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(3 * time.Minute)},
		}
		pr10Task3 := api.TaskItem{
			Filename: "pr10_3.yaml",
			Task:     &api.QueueTask{Number: 10, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(4 * time.Minute)},
		}

		pr20Task1 := api.TaskItem{
			Filename: "pr20_1.yaml",
			Task:     &api.QueueTask{Number: 20, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(5 * time.Minute)},
		}

		pr20Task2 := api.TaskItem{
			Filename: "pr20_2.yaml",
			Task:     &api.QueueTask{Number: 20, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(6 * time.Minute)},
		}

		items := []api.TaskItem{pr10Task1, pr10Task2, pr10Task3, pr20Task1, pr20Task2}
		got := sortTasksFairly(items)

		expectedOrder := []string{"pr10_1.yaml", "pr20_1.yaml", "pr10_2.yaml", "pr20_2.yaml", "pr10_3.yaml"}
		for i, expected := range expectedOrder {
			if got[i].Filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].Filename)
			}
		}
	})

	t.Run("Priority and Phase are respected across entities", func(t *testing.T) {
		criticalTask := api.TaskItem{
			Filename: "critical.yaml",
			Task:     &api.QueueTask{Number: 10, Priority: api.PriorityCritical, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(5 * time.Minute)},
		}
		mediumTask := api.TaskItem{
			Filename: "medium.yaml",
			Task:     &api.QueueTask{Number: 20, Priority: api.PriorityMedium, Phase: api.PhaseComments, EnqueuedAt: baseTime.Add(1 * time.Minute)},
		}
		phase1Task := api.TaskItem{
			Filename: "phase1.yaml",
			Task:     &api.QueueTask{Number: 20, Priority: api.PriorityMedium, Phase: api.PhaseIterate, EnqueuedAt: baseTime.Add(2 * time.Minute)},
		}

		items := []api.TaskItem{mediumTask, criticalTask, phase1Task}
		got := sortTasksFairly(items)

		expectedOrder := []string{"critical.yaml", "phase1.yaml", "medium.yaml"}
		for i, expected := range expectedOrder {
			if got[i].Filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].Filename)
			}
		}
	})

	t.Run("Fallback to modTime or CreatedAt when EnqueuedAt is zero", func(t *testing.T) {
		taskOldCreated := &api.QueueTask{
			CreatedAt: baseTime,
		}
		taskNewCreated := &api.QueueTask{
			CreatedAt: baseTime.Add(1 * time.Hour),
		}
		taskWithEnqueued := &api.QueueTask{
			CreatedAt:  baseTime.Add(2 * time.Hour),
			EnqueuedAt: baseTime.Add(10 * time.Minute),
		}

		t1 := getEnqueueTime(taskOldCreated, time.Time{})
		if !t1.Equal(baseTime) {
			t.Errorf("expected fallback to CreatedAt %v, got %v", baseTime, t1)
		}

		t2 := getEnqueueTime(taskNewCreated, baseTime.Add(5*time.Minute))
		if !t2.Equal(baseTime.Add(5 * time.Minute)) {
			t.Errorf("expected fallback to modTime %v, got %v", baseTime.Add(5*time.Minute), t2)
		}

		t3 := getEnqueueTime(taskWithEnqueued, baseTime.Add(5*time.Minute))
		if !t3.Equal(baseTime.Add(10 * time.Minute)) {
			t.Errorf("expected EnqueuedAt %v, got %v", baseTime.Add(10*time.Minute), t3)
		}
	})
}

func TestWriteTaskAtomicallyAndTaskExists(t *testing.T) {
	dir := t.TempDir()
	procDir := t.TempDir()

	task := &api.QueueTask{
		Type:     api.TypeIssueFix,
		Number:   42,
		Priority: api.PriorityHigh,
		Status:   api.StatusPending,
	}

	filename := "task-issue-42.yaml"
	if taskExists(dir, procDir, filename) {
		t.Fatalf("taskExists returned true before write")
	}

	if err := writeTaskAtomically(dir, filename, task); err != nil {
		t.Fatalf("writeTaskAtomically failed: %v", err)
	}

	if !taskExists(dir, procDir, filename) {
		t.Fatalf("taskExists returned false after writing to incoming dir")
	}

	// Move to procDir and verify taskExists still finds it
	if err := os.Rename(filepath.Join(dir, filename), filepath.Join(procDir, filename)); err != nil {
		t.Fatalf("failed to move file: %v", err)
	}
	if !taskExists(dir, procDir, filename) {
		t.Fatalf("taskExists returned false when file is in processing dir")
	}
}

func TestIsDoNotProcess(t *testing.T) {
	queueDir := t.TempDir()

	if isDoNotProcess(queueDir) {
		t.Errorf("expected isDoNotProcess to be false for empty dir")
	}

	drainFile := filepath.Join(queueDir, ".drain")
	if err := os.WriteFile(drainFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write drain file: %v", err)
	}

	if !isDoNotProcess(queueDir) {
		t.Errorf("expected isDoNotProcess to be true when .drain file exists")
	}
}

func TestPriorityRankValue(t *testing.T) {
	tests := []struct {
		priority api.TaskPriority
		want     int
	}{
		{api.PriorityCritical, 1},
		{api.PriorityUrgent, 2},
		{api.PriorityImportant, 3},
		{api.PriorityHigh, 4},
		{api.PriorityMedium, 5},
		{api.PriorityLow, 6},
		{api.PriorityUnknown, 5},
	}

	for _, tc := range tests {
		got := priorityRankValue(tc.priority)
		if got != tc.want {
			t.Errorf("priorityRankValue(%q) = %d, want %d", tc.priority, got, tc.want)
		}
	}
}

func TestGetEntityKey(t *testing.T) {
	t1 := &api.QueueTask{Number: 99}
	if getEntityKey(t1) != "99" {
		t.Errorf("expected '99', got %q", getEntityKey(t1))
	}

	t2 := &api.QueueTask{AgentFile: "test-chore.yaml"}
	if getEntityKey(t2) != "chore:test-chore.yaml" {
		t.Errorf("expected 'chore:test-chore.yaml', got %q", getEntityKey(t2))
	}

	t3 := &api.QueueTask{URL: "https://github.com/org/repo"}
	if getEntityKey(t3) != "url:https://github.com/org/repo" {
		t.Errorf("expected 'url:https://github.com/org/repo', got %q", getEntityKey(t3))
	}

	t4 := &api.QueueTask{Type: "custom"}
	if getEntityKey(t4) != "type:custom" {
		t.Errorf("expected 'type:custom', got %q", getEntityKey(t4))
	}

	t5 := &api.QueueTask{}
	if getEntityKey(t5) != "default" {
		t.Errorf("expected 'default', got %q", getEntityKey(t5))
	}
}

func TestGetIssuePriority(t *testing.T) {
	nameUrgent := "priority/urgent"
	issue := &githubv39.Issue{
		Labels: []*githubv39.Label{
			{Name: &nameUrgent},
		},
	}
	if getIssuePriority(issue) != api.PriorityUrgent {
		t.Errorf("expected 'urgent', got %q", getIssuePriority(issue))
	}

	issueNoLabel := &githubv39.Issue{}
	if getIssuePriority(issueNoLabel) != api.PriorityMedium {
		t.Errorf("expected 'medium', got %q", getIssuePriority(issueNoLabel))
	}
}

func TestRemovePendingTasksForNumber(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "task-issue-10.yaml")
	f2 := filepath.Join(dir, "task-pr-10-comments.yaml")
	f3 := filepath.Join(dir, "task-issue-20.yaml")

	_ = os.WriteFile(f1, []byte(""), 0644)
	_ = os.WriteFile(f2, []byte(""), 0644)
	_ = os.WriteFile(f3, []byte(""), 0644)

	removePendingTasksForNumber(dir, 10)

	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Errorf("expected f1 to be removed")
	}
	if _, err := os.Stat(f2); !os.IsNotExist(err) {
		t.Errorf("expected f2 to be removed")
	}
	if _, err := os.Stat(f3); os.IsNotExist(err) {
		t.Errorf("expected f3 to NOT be removed")
	}
}

func TestLoadProcessedTasks(t *testing.T) {
	dir := t.TempDir()

	issueTask := filepath.Join(dir, "task-issue-100.yaml")
	_ = os.WriteFile(issueTask, []byte("type: issue-fix\ncompletedAt: \"2026-08-01T10:00:00Z\"\n"), 0644)

	prCommentsTask := filepath.Join(dir, "task-pr-200-comments.yaml")
	_ = os.WriteFile(prCommentsTask, []byte("type: pr-comments\ncommitSHA: sha200\ncompletedAt: \"2026-08-01T11:00:00Z\"\n"), 0644)

	prInvestigateTask := filepath.Join(dir, "task-pr-200-investigate.yaml")
	_ = os.WriteFile(prInvestigateTask, []byte("type: pr-investigate\ncommitSHA: sha-inv\ncompletedAt: \"2026-08-01T12:00:00Z\"\n"), 0644)

	prReviewTask := filepath.Join(dir, "task-pr-200-review.yaml")
	_ = os.WriteFile(prReviewTask, []byte("type: pr-review\ncommitSHA: sha-rev\ncompletedAt: \"2026-08-01T13:00:00Z\"\n"), 0644)

	prIterateTask := filepath.Join(dir, "task-pr-200-iterate.yaml")
	_ = os.WriteFile(prIterateTask, []byte("type: pr-iterate\ncommitSHA: sha-iter\ncompletedAt: \"2026-08-01T14:00:00Z\"\n"), 0644)

	issues, prs := loadProcessedTasks(dir)
	if _, ok := issues[100]; !ok {
		t.Errorf("expected issue 100 in loaded issues")
	}
	if state, ok := prs[200]; !ok {
		t.Errorf("expected pr 200 in loaded prs")
	} else {
		if state.lastCommentAddressedSHA != "sha200" {
			t.Errorf("expected lastCommentAddressedSHA 'sha200', got %q", state.lastCommentAddressedSHA)
		}
		if state.lastInvestigatedSHA != "sha-inv" {
			t.Errorf("expected lastInvestigatedSHA 'sha-inv', got %q", state.lastInvestigatedSHA)
		}
		if state.lastReviewedSHA != "sha-rev" {
			t.Errorf("expected lastReviewedSHA 'sha-rev', got %q", state.lastReviewedSHA)
		}
		if state.lastIteratedSHA != "sha-iter" {
			t.Errorf("expected lastIteratedSHA 'sha-iter', got %q", state.lastIteratedSHA)
		}
	}
}

func TestWorkflowCooldownCompletedAt(t *testing.T) {
	tempDir := t.TempDir()
	processedPath := filepath.Join(tempDir, "task-workflow-test-issue-1.yaml")

	// Task completed 5 hours ago
	completedAt := time.Now().Add(-5 * time.Hour)
	taskYAML := fmt.Sprintf("completedAt: %s\n", completedAt.Format(time.RFC3339Nano))
	if err := os.WriteFile(processedPath, []byte(taskYAML), 0644); err != nil {
		t.Fatalf("Failed to write test task yaml: %v", err)
	}

	info, err := os.Stat(processedPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	lastRunTime := info.ModTime()
	if data, err := os.ReadFile(processedPath); err == nil {
		var q api.QueueTask
		if err := yaml.Unmarshal(data, &q); err == nil && !q.CompletedAt.IsZero() {
			lastRunTime = q.CompletedAt
		}
	}

	if !lastRunTime.Equal(completedAt) {
		t.Fatalf("lastRunTime = %v, want %v", lastRunTime, completedAt)
	}
}

func TestHasActivePRTask(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)

	prNum := 12046

	// 1. Initially empty
	if hasActivePRTask(incomingDir, processingDir, prNum) {
		t.Errorf("expected hasActivePRTask to be false for empty directories")
	}

	// 2. Task in incoming directory
	incomingTask := filepath.Join(incomingDir, "task-pr-12046-comments.yaml")
	_ = os.WriteFile(incomingTask, []byte("type: pr-comments\n"), 0644)
	if !hasActivePRTask(incomingDir, processingDir, prNum) {
		t.Errorf("expected hasActivePRTask to be true when task is in incomingDir")
	}

	// 3. Different PR number in incoming directory
	if hasActivePRTask(incomingDir, processingDir, 9999) {
		t.Errorf("expected hasActivePRTask to be false for different PR number")
	}

	// 4. Move task to processing directory
	_ = os.Remove(incomingTask)
	processingTask := filepath.Join(processingDir, "task-pr-12046-investigate.yaml")
	_ = os.WriteFile(processingTask, []byte("type: pr-investigate\n"), 0644)
	if !hasActivePRTask(incomingDir, processingDir, prNum) {
		t.Errorf("expected hasActivePRTask to be true when task is in processingDir")
	}

	// 5. Remove task from processing directory
	_ = os.Remove(processingTask)
	if hasActivePRTask(incomingDir, processingDir, prNum) {
		t.Errorf("expected hasActivePRTask to be false after removing tasks")
	}

	// 6. Non-matching files (e.g. issues or non-yaml files)
	issueTask := filepath.Join(incomingDir, "task-issue-12046.yaml")
	_ = os.WriteFile(issueTask, []byte("type: issue-fix\n"), 0644)
	logFile := filepath.Join(processingDir, "task-pr-12046-comments.log")
	_ = os.WriteFile(logFile, []byte("log output"), 0644)
	if hasActivePRTask(incomingDir, processingDir, prNum) {
		t.Errorf("expected hasActivePRTask to be false for issue tasks and log files")
	}
}
