package api

import (
	"testing"
	"time"
)

func TestQueueTask_Duration(t *testing.T) {
	task := &QueueTask{}
	if d := task.Duration(); d != 0 {
		t.Errorf("expected 0 duration for unset timestamps, got %v", d)
	}

	start := time.Now()
	task.StartedAt = start
	task.CompletedAt = start.Add(1500 * time.Millisecond)

	if d := task.Duration(); d != 1500*time.Millisecond {
		t.Errorf("expected 1.5s duration, got %v", d)
	}

	// Completed before started
	task.CompletedAt = start.Add(-1 * time.Second)
	if d := task.Duration(); d != 0 {
		t.Errorf("expected 0 duration when completed before started, got %v", d)
	}
}

func TestIsPRTask(t *testing.T) {
	tests := []struct {
		taskType TaskType
		expected bool
	}{
		{TypePRInvestigate, true},
		{TypePRComments, true},
		{TypePRIterate, true},
		{TypePRReview, false},
		{TypeIssueFix, false},
		{TypeAgentChore, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range tests {
		t.Run(string(tc.taskType), func(t *testing.T) {
			got := IsPRTask(tc.taskType)
			if got != tc.expected {
				t.Errorf("IsPRTask(%q) = %v, want %v", tc.taskType, got, tc.expected)
			}
		})
	}
}
