package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

func TestShouldRunChoreAt(t *testing.T) {
	// Base mock "now" time: Wednesday, July 1st, 2026 at 9:55 AM UTC
	now := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time
		expected bool
	}{
		{
			name:     "Never run before (zero lastRun)",
			schedule: "*/30 * * * *",
			lastRun:  time.Time{},
			expected: true,
		},
		{
			name:     "Interval triggers - run at 9:15 AM (40m ago, next was 9:30 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Interval skips - run at 9:40 AM (15m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 40, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Macro descriptor @hourly - run at 8:45 AM (70m ago, next was 9:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Macro descriptor @hourly - run at 9:15 AM (40m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Never schedule",
			schedule: "never",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentNow := now
			// For the Monday test cases, override mock now to Monday, July 6th, 2026, 9:55 AM UTC
			if tc.name == "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger" ||
				tc.name == "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM" {
				currentNow = time.Date(2026, 7, 6, 9, 55, 0, 0, time.UTC)
			}

			got := shouldRunChoreAt(tc.schedule, tc.lastRun, currentNow)
			if got != tc.expected {
				t.Errorf("shouldRunChoreAt(%q, %v, %v) = %v; want %v", tc.schedule, tc.lastRun, currentNow, got, tc.expected)
			}
		})
	}
}

func TestGetReferencedIssues(t *testing.T) {
	tests := []struct {
		name     string
		headRef  string
		title    string
		body     string
		expected map[int]bool
	}{
		{
			name:    "Branch name contains issue number",
			headRef: "issue_8883",
			title:   "Some PR title",
			body:    "Some PR body",
			expected: map[int]bool{
				8883: true,
			},
		},
		{
			name:    "Title and body contain issue number references",
			headRef: "my-dev-branch",
			title:   "Fixes #8883 and #10294",
			body:    "Resolves issue #9271 in config-connector",
			expected: map[int]bool{
				8883:  true,
				10294: true,
				9271:  true,
			},
		},
		{
			name:     "No references",
			headRef:  "master",
			title:    "Clean PR without issue link",
			body:     "Just refactoring some code",
			expected: map[int]bool{},
		},
		{
			name:    "Branch with timestamp and keyword issue link without #",
			headRef: "ada-coder-bot:issue-11414-1783386792",
			title:   "Fixes 11414",
			body:    "Resolves 11414 without hash",
			expected: map[int]bool{
				11414: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pr := &githubv39.PullRequest{
				Head: &githubv39.PullRequestBranch{
					Ref: &tc.headRef,
				},
				Title: &tc.title,
				Body:  &tc.body,
			}
			got := getReferencedIssues(pr)
			if len(got) != len(tc.expected) {
				t.Fatalf("getReferencedIssues() returned %v; want %v", got, tc.expected)
			}
			for num := range tc.expected {
				if !got[num] {
					t.Errorf("getReferencedIssues() missed expected issue %d in %v", num, got)
				}
			}
		})
	}
}

func TestGetMissingLabelsForPR(t *testing.T) {
	tests := []struct {
		name      string
		prLabels  []string
		refIssues [][]string
		expected  []string
	}{
		{
			name:      "All issue labels are missing from PR",
			prLabels:  []string{},
			refIssues: [][]string{{"greenfield", "step/controller"}},
			expected:  []string{"greenfield", "step/controller"},
		},
		{
			name:      "Some labels already exist on PR",
			prLabels:  []string{"greenfield"},
			refIssues: [][]string{{"greenfield", "step/controller", "area/direct"}},
			expected:  []string{"greenfield", "step/controller", "area/direct"},
		},
		{
			name:     "Duplicate labels across multiple issues are deduplicated",
			prLabels: []string{"priority/medium"},
			refIssues: [][]string{
				{"greenfield", "step/controller"},
				{"step/controller", "area/direct"},
			},
			expected: []string{"priority/medium", "greenfield", "step/controller", "area/direct"},
		},
		{
			name:      "No missing labels",
			prLabels:  []string{"greenfield", "step/controller"},
			refIssues: [][]string{{"greenfield"}},
			expected:  []string{"greenfield", "step/controller"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var prLabels []*githubv39.Label
			for _, name := range tc.prLabels {
				prLabels = append(prLabels, &githubv39.Label{Name: stringPtr(name)})
			}

			var refIssues []*githubv39.Issue
			for _, issueLabels := range tc.refIssues {
				var labels []*githubv39.Label
				for _, name := range issueLabels {
					labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
				}
				refIssues = append(refIssues, &githubv39.Issue{Labels: labels})
			}

			got := getMissingLabelsForPR(prLabels, refIssues)

			// Build the final set of labels on the PR (original labels + added labels)
			finalLabelsMap := make(map[string]bool)
			var finalLabels []string
			for _, name := range tc.prLabels {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}
			for _, name := range got {
				if !finalLabelsMap[name] {
					finalLabelsMap[name] = true
					finalLabels = append(finalLabels, name)
				}
			}

			if len(finalLabels) != len(tc.expected) {
				t.Fatalf("Final labels list length is %d (%v); want %d (%v)", len(finalLabels), finalLabels, len(tc.expected), tc.expected)
			}
			for i, val := range tc.expected {
				if finalLabels[i] != val {
					t.Errorf("Final label at index %d = %q; want %q", i, finalLabels[i], val)
				}
			}
		})
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
		var q QueueTask
		if err := yaml.Unmarshal(data, &q); err == nil && !q.CompletedAt.IsZero() {
			lastRunTime = q.CompletedAt
		}
	}

	if !lastRunTime.Equal(completedAt) {
		t.Fatalf("lastRunTime = %v, want %v", lastRunTime, completedAt)
	}
}

func TestGetInvestigationCount(t *testing.T) {
	tests := []struct {
		name          string
		comments      []*githubv39.IssueComment
		allBotUsers   []string
		githubLogin   string
		allowlist     []string
		expectedCount int
	}{
		{
			name:          "No comments, should be 0",
			comments:      []*githubv39.IssueComment{},
			expectedCount: 0,
		},
		{
			name: "Only bot investigate comments, should be counted",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 2,
		},
		{
			name: "Prow comments should not reset the circuit breaker",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-3 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("google-oss-prow"), Type: stringPtr("Bot")},
					Body:      stringPtr("Some prow CI failure"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 2,
		},
		{
			name: "Human comments should reset the circuit breaker",
			comments: []*githubv39.IssueComment{
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-3 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("real-human"), Type: stringPtr("User")},
					Body:      stringPtr("Can you look into this?"),
					CreatedAt: timePtr(time.Now().Add(-2 * time.Hour)),
				},
				{
					User:      &githubv39.User{Login: stringPtr("pool-bot")},
					Body:      stringPtr("🤖 AI Factory started investigating CI check failures"),
					CreatedAt: timePtr(time.Now().Add(-1 * time.Hour)),
				},
			},
			allBotUsers:   []string{"pool-bot"},
			expectedCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lastCommitTime := time.Now().Add(-24 * time.Hour)
			count := getInvestigationCount(tc.comments, lastCommitTime, tc.allBotUsers, tc.githubLogin, tc.allowlist)
			if count != tc.expectedCount {
				t.Errorf("expected count %d, got %d", tc.expectedCount, count)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestShouldUnassignStaleBot(t *testing.T) {
	tests := []struct {
		name          string
		lastSHA       string
		unassignedSHA string
		headSHA       string
		assignedBot   string
		expected      bool
	}{
		{
			name:          "Should unassign when new commit and not yet unassigned",
			lastSHA:       "old-sha",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      true,
		},
		{
			name:          "Should not unassign when lastSHA is empty",
			lastSHA:       "",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should not unassign when lastSHA matches headSHA",
			lastSHA:       "same-sha",
			unassignedSHA: "",
			headSHA:       "same-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should not unassign when assignedBot is empty",
			lastSHA:       "old-sha",
			unassignedSHA: "",
			headSHA:       "new-sha",
			assignedBot:   "",
			expected:      false,
		},
		{
			name:          "Should not unassign when already unassigned for this headSHA",
			lastSHA:       "old-sha",
			unassignedSHA: "new-sha",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      false,
		},
		{
			name:          "Should unassign if previously unassigned for a different SHA",
			lastSHA:       "old-sha",
			unassignedSHA: "old-sha2",
			headSHA:       "new-sha",
			assignedBot:   "bot1",
			expected:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldUnassignStaleBot(tc.lastSHA, tc.unassignedSHA, tc.headSHA, tc.assignedBot)
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

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
}

func TestGetLastPRActivityTime(t *testing.T) {
	baseTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pr := &githubv39.PullRequest{
		CreatedAt: &baseTime,
	}

	githubLogin := "factory-bot"
	bots := []string{"allowlisted-bot"}

	// Case 1: No comments/reviews
	got := getLastPRActivityTime(pr, nil, nil, nil, githubLogin, bots)
	if !got.Equal(baseTime) {
		t.Errorf("Case 1 failed: expected %v, got %v", baseTime, got)
	}

	// Case 2: Human comment on issue
	humanTime := baseTime.Add(1 * time.Hour)
	comments := []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("human-user")},
			CreatedAt: &humanTime,
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
	if !got.Equal(humanTime) {
		t.Errorf("Case 2 failed: expected %v, got %v", humanTime, got)
	}

	// Case 3: Bot comment (ignored)
	botTime := baseTime.Add(2 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("allowlisted-bot")},
			CreatedAt: &botTime,
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
	if !got.Equal(baseTime) {
		t.Errorf("Case 3 failed: expected %v, got %v", baseTime, got)
	}

	// Case 4: Bot pause comment (resets timer)
	pauseTime := baseTime.Add(3 * time.Hour)
	comments = []*githubv39.IssueComment{
		{
			User:      &githubv39.User{Login: stringPtr("factory-bot")},
			CreatedAt: &pauseTime,
			Body:      stringPtr("🤖 AI Factory has paused automated processing on this pull request due to a period of inactivity"),
		},
	}
	got = getLastPRActivityTime(pr, comments, nil, nil, githubLogin, bots)
	if !got.Equal(pauseTime) {
		t.Errorf("Case 4 failed: expected %v, got %v", pauseTime, got)
	}

	// Case 5: Human review
	reviewTime := baseTime.Add(4 * time.Hour)
	reviews := []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(1),
			User:        &githubv39.User{Login: stringPtr("human-user2")},
			SubmittedAt: &reviewTime,
		},
	}
	got = getLastPRActivityTime(pr, nil, reviews, nil, githubLogin, bots)
	if !got.Equal(reviewTime) {
		t.Errorf("Case 5 failed: expected %v, got %v", reviewTime, got)
	}

	// Case 6: Review comment by human under a bot review
	botReviewTime := baseTime.Add(5 * time.Hour)
	humanReviewCommentTime := baseTime.Add(6 * time.Hour)
	reviews = []*githubv39.PullRequestReview{
		{
			ID:          int64Ptr(2),
			User:        &githubv39.User{Login: stringPtr("factory-bot")},
			SubmittedAt: &botReviewTime,
		},
	}
	revComments := map[int64][]*githubv39.PullRequestComment{
		2: {
			{
				User:      &githubv39.User{Login: stringPtr("human-user3")},
				CreatedAt: &humanReviewCommentTime,
			},
		},
	}
	got = getLastPRActivityTime(pr, nil, reviews, revComments, githubLogin, bots)
	if !got.Equal(humanReviewCommentTime) {
		t.Errorf("Case 6 failed: expected %v, got %v", humanReviewCommentTime, got)
	}
}

func int64Ptr(i int64) *int64 {
	return &i
}
func TestIsReviewerBot(t *testing.T) {
	loginReviewBot := "reviewbot-robot"
	userReviewBot := &githubv39.User{Login: &loginReviewBot}
	loginCoderBot := "neumann-coder-bot"
	userCoderBot := &githubv39.User{Login: &loginCoderBot}

	cfg := &config.FactoryConfig{
		Roles: map[string]config.RoleConfig{
			"reviewer": {Users: []string{"reviewbot-robot"}},
		},
	}

	if !isReviewerBot(userReviewBot, cfg) {
		t.Errorf("expected reviewbot-robot to be identified as reviewer bot")
	}
	if isReviewerBot(userCoderBot, cfg) {
		t.Errorf("expected neumann-coder-bot to not be identified as reviewer bot")
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

func TestIsSandboxTaskCompleted(t *testing.T) {
	scheme := runtime.NewScheme()
	ctx := context.Background()
	ns := "test-ns"

	tests := []struct {
		taskType          string
		annotatedTaskType string
		state             string
		expectedCompleted bool
	}{
		{"pr-comments", "address-comments", "Completed", true},
		{"pr-comments", "address-comments", "Running", false},
		{"pr-investigate", "investigate", "Completed", true},
		{"pr-iterate", "iterate", "Completed", true},
		{"pr-review", "review", "Completed", true},
		{"issue-fix", "fix-issue", "Completed", true},
		{"agent-chore", "agent", "Completed", true},
		{"pr-comments", "wrong-type", "Completed", false},
	}

	for _, tc := range tests {
		sbName := fmt.Sprintf("sb-%s-%s", tc.taskType, tc.state)
		fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
			k8s.SandboxGVR: "SandboxList",
		})
		kubeClient := &clients.KubernetesClient{
			DynamicClient: fakeDynamic,
		}

		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sbName,
					"namespace": ns,
					"annotations": map[string]interface{}{
						"sandbox.gemini.google.com/last-task-state": tc.state,
						"sandbox.gemini.google.com/last-task-type":  tc.annotatedTaskType,
					},
				},
			},
		}

		_, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, sb, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create mock sandbox: %v", err)
		}

		completed, err := isSandboxTaskCompleted(ctx, kubeClient, ns, sbName, tc.taskType)
		if err != nil {
			t.Errorf("Unexpected error for %s: %v", tc.taskType, err)
		}
		if completed != tc.expectedCompleted {
			t.Errorf("For taskType=%s, annotatedTaskType=%s, state=%s: expected completed=%v, got %v",
				tc.taskType, tc.annotatedTaskType, tc.state, tc.expectedCompleted, completed)
		}
	}
}

