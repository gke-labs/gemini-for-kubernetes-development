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
