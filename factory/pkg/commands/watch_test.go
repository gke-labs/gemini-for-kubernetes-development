package commands

import (
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
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

func TestSortQueueTasks(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		input    []QueueTaskItem
		expected []string
	}{
		{
			name: "Sorting by priority level",
			input: []QueueTaskItem{
				{
					Filename: "task-medium.yaml",
					Task: &QueueTask{
						Type:     "issue-fix",
						Priority: "medium",
						Phase:    1,
					},
				},
				{
					Filename: "task-critical.yaml",
					Task: &QueueTask{
						Type:     "issue-fix",
						Priority: "critical",
						Phase:    1,
					},
				},
				{
					Filename: "task-high.yaml",
					Task: &QueueTask{
						Type:     "issue-fix",
						Priority: "high",
						Phase:    1,
					},
				},
			},
			expected: []string{"task-critical.yaml", "task-high.yaml", "task-medium.yaml"},
		},
		{
			name: "Sorting by phase when priority matches",
			input: []QueueTaskItem{
				{
					Filename: "task-phase-3.yaml",
					Task: &QueueTask{
						Type:     "issue-fix",
						Priority: "medium",
						Phase:    3,
					},
				},
				{
					Filename: "task-phase-1.yaml",
					Task: &QueueTask{
						Type:     "issue-fix",
						Priority: "medium",
						Phase:    1,
					},
				},
			},
			expected: []string{"task-phase-1.yaml", "task-phase-3.yaml"},
		},
		{
			name: "Sorting by age (newest first) when priority and phase match",
			input: []QueueTaskItem{
				{
					Filename: "task-old.yaml",
					Task: &QueueTask{
						Type:      "issue-fix",
						Priority:  "medium",
						Phase:     1,
						CreatedAt: now.Add(-10 * time.Minute),
					},
				},
				{
					Filename: "task-new.yaml",
					Task: &QueueTask{
						Type:      "issue-fix",
						Priority:  "medium",
						Phase:     1,
						CreatedAt: now,
					},
				},
			},
			expected: []string{"task-new.yaml", "task-old.yaml"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inputCopy := make([]QueueTaskItem, len(tc.input))
			copy(inputCopy, tc.input)

			sortQueueTasks(inputCopy)

			if len(inputCopy) != len(tc.expected) {
				t.Fatalf("sortQueueTasks() returned slice of length %d; want %d", len(inputCopy), len(tc.expected))
			}

			for i, expectedFilename := range tc.expected {
				if inputCopy[i].Filename != expectedFilename {
					t.Errorf("At index %d: expected filename %q, got %q", i, expectedFilename, inputCopy[i].Filename)
				}
			}
		})
	}
}
