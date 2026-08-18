package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
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
	if state.lastSHA != "abcd123" {
		t.Errorf("expected lastSHA to be 'abcd123', got '%s'", state.lastSHA)
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
		task1 := taskItem{
			filename: "task1.yaml",
			task: &QueueTask{
				Type:       "pr-comments",
				Number:     10,
				Priority:   "medium",
				Phase:      2,
				CreatedAt:  baseTime,
				EnqueuedAt: baseTime.Add(1 * time.Minute),
			},
		}
		task2 := taskItem{
			filename: "task2.yaml",
			task: &QueueTask{
				Type:       "pr-comments",
				Number:     10,
				Priority:   "medium",
				Phase:      2,
				CreatedAt:  baseTime.Add(1 * time.Hour),
				EnqueuedAt: baseTime.Add(2 * time.Minute),
			},
		}
		task3 := taskItem{
			filename: "task3.yaml",
			task: &QueueTask{
				Type:       "pr-comments",
				Number:     10,
				Priority:   "medium",
				Phase:      2,
				CreatedAt:  baseTime.Add(2 * time.Hour),
				EnqueuedAt: baseTime.Add(3 * time.Minute),
			},
		}

		items := []taskItem{task3, task2, task1}
		got := sortTasksFairly(items)

		expectedOrder := []string{"task1.yaml", "task2.yaml", "task3.yaml"}
		for i, expected := range expectedOrder {
			if got[i].filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].filename)
			}
		}
	})

	t.Run("Round-Robin across entities prevents entity starvation", func(t *testing.T) {
		pr10Task1 := taskItem{
			filename: "pr10_1.yaml",
			task:     &QueueTask{Number: 10, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(1 * time.Minute)},
		}
		pr10Task2 := taskItem{
			filename: "pr10_2.yaml",
			task:     &QueueTask{Number: 10, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(3 * time.Minute)},
		}
		pr10Task3 := taskItem{
			filename: "pr10_3.yaml",
			task:     &QueueTask{Number: 10, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(4 * time.Minute)},
		}

		pr20Task1 := taskItem{
			filename: "pr20_1.yaml",
			task:     &QueueTask{Number: 20, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(5 * time.Minute)},
		}

		pr20Task2 := taskItem{
			filename: "pr20_2.yaml",
			task:     &QueueTask{Number: 20, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(6 * time.Minute)},
		}

		items := []taskItem{pr10Task1, pr10Task2, pr10Task3, pr20Task1, pr20Task2}
		got := sortTasksFairly(items)

		expectedOrder := []string{"pr10_1.yaml", "pr20_1.yaml", "pr10_2.yaml", "pr20_2.yaml", "pr10_3.yaml"}
		for i, expected := range expectedOrder {
			if got[i].filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].filename)
			}
		}
	})

	t.Run("Priority and Phase are respected across entities", func(t *testing.T) {
		criticalTask := taskItem{
			filename: "critical.yaml",
			task:     &QueueTask{Number: 10, Priority: "critical", Phase: 3, EnqueuedAt: baseTime.Add(5 * time.Minute)},
		}
		mediumTask := taskItem{
			filename: "medium.yaml",
			task:     &QueueTask{Number: 20, Priority: "medium", Phase: 3, EnqueuedAt: baseTime.Add(1 * time.Minute)},
		}
		phase1Task := taskItem{
			filename: "phase1.yaml",
			task:     &QueueTask{Number: 20, Priority: "medium", Phase: 1, EnqueuedAt: baseTime.Add(2 * time.Minute)},
		}

		items := []taskItem{mediumTask, criticalTask, phase1Task}
		got := sortTasksFairly(items)

		expectedOrder := []string{"critical.yaml", "phase1.yaml", "medium.yaml"}
		for i, expected := range expectedOrder {
			if got[i].filename != expected {
				t.Errorf("at index %d: expected %s, got %s", i, expected, got[i].filename)
			}
		}
	})

	t.Run("Fallback to modTime or CreatedAt when EnqueuedAt is zero", func(t *testing.T) {
		taskOldCreated := &QueueTask{
			CreatedAt: baseTime,
		}
		taskNewCreated := &QueueTask{
			CreatedAt: baseTime.Add(1 * time.Hour),
		}
		taskWithEnqueued := &QueueTask{
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

	task := &QueueTask{
		Type:     "issue-fix",
		Number:   42,
		Priority: "high",
		Status:   "Pending",
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
		priority string
		want     int
	}{
		{"critical", 1},
		{"urgent", 2},
		{"important", 3},
		{"high", 4},
		{"medium", 5},
		{"low", 6},
		{"unknown", 5},
	}

	for _, tc := range tests {
		got := priorityRankValue(tc.priority)
		if got != tc.want {
			t.Errorf("priorityRankValue(%q) = %d, want %d", tc.priority, got, tc.want)
		}
	}
}

func TestGetEntityKey(t *testing.T) {
	t1 := &QueueTask{Number: 99}
	if getEntityKey(t1) != "99" {
		t.Errorf("expected '99', got %q", getEntityKey(t1))
	}

	t2 := &QueueTask{AgentFile: "test-chore.yaml"}
	if getEntityKey(t2) != "chore:test-chore.yaml" {
		t.Errorf("expected 'chore:test-chore.yaml', got %q", getEntityKey(t2))
	}

	t3 := &QueueTask{URL: "https://github.com/org/repo"}
	if getEntityKey(t3) != "url:https://github.com/org/repo" {
		t.Errorf("expected 'url:https://github.com/org/repo', got %q", getEntityKey(t3))
	}

	t4 := &QueueTask{Type: "custom"}
	if getEntityKey(t4) != "type:custom" {
		t.Errorf("expected 'type:custom', got %q", getEntityKey(t4))
	}

	t5 := &QueueTask{}
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
	if getIssuePriority(issue) != "urgent" {
		t.Errorf("expected 'urgent', got %q", getIssuePriority(issue))
	}

	issueNoLabel := &githubv39.Issue{}
	if getIssuePriority(issueNoLabel) != "medium" {
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

func TestIsPRTask(t *testing.T) {
	if !isPRTask("pr-investigate") || !isPRTask("pr-comments") || !isPRTask("pr-iterate") {
		t.Errorf("expected true for PR task types")
	}
	if isPRTask("issue-fix") || isPRTask("agent-chore") || isPRTask("pr-review") {
		t.Errorf("expected false for non-PR iterate/investigate/comments task types")
	}
}

func TestLoadProcessedTasks(t *testing.T) {
	dir := t.TempDir()

	issueTask := filepath.Join(dir, "task-issue-100.yaml")
	_ = os.WriteFile(issueTask, []byte("type: issue-fix\ncompletedAt: \"2026-08-01T10:00:00Z\"\n"), 0644)

	prTask := filepath.Join(dir, "task-pr-200-comments.yaml")
	_ = os.WriteFile(prTask, []byte("type: pr-comments\ncommitSHA: sha200\ncompletedAt: \"2026-08-01T11:00:00Z\"\n"), 0644)

	issues, prs := loadProcessedTasks(dir)
	if _, ok := issues[100]; !ok {
		t.Errorf("expected issue 100 in loaded issues")
	}
	if state, ok := prs[200]; !ok {
		t.Errorf("expected pr 200 in loaded prs")
	} else if state.lastCommentAddressedSHA != "sha200" {
		t.Errorf("expected lastCommentAddressedSHA 'sha200', got %q", state.lastCommentAddressedSHA)
	}
}
