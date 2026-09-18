package api

import (
	"reflect"
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

func TestQueueTask_DeepCopy(t *testing.T) {
	t.Run("nil task", func(t *testing.T) {
		var task *QueueTask
		if got := task.DeepCopy(); got != nil {
			t.Errorf("DeepCopy of a nil task = %v, want nil", got)
		}
	})

	t.Run("copies every field", func(t *testing.T) {
		original := &QueueTask{
			Type:         TypeIssueFix,
			Number:       7,
			Status:       StatusCompleted,
			CompletedAt:  time.Now(),
			Instructions: []string{"first", "second"},
		}

		copied := original.DeepCopy()
		if !reflect.DeepEqual(copied, original) {
			t.Errorf("DeepCopy = %+v, want %+v", copied, original)
		}
	})

	t.Run("instructions do not share a backing array", func(t *testing.T) {
		original := &QueueTask{Instructions: []string{"first"}}

		copied := original.DeepCopy()
		copied.Instructions[0] = "rewritten"

		if original.Instructions[0] != "first" {
			t.Errorf("writing to the copy changed the original: %q", original.Instructions[0])
		}
	})

	t.Run("a nil instructions list stays nil", func(t *testing.T) {
		if got := (&QueueTask{}).DeepCopy(); got.Instructions != nil {
			t.Errorf("DeepCopy gave an empty task instructions %v, want nil", got.Instructions)
		}
	})
}
