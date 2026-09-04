package concurrency

import (
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// GetEnqueueTime returns the timestamp when a task was enqueued, falling back to
// file modification time or task creation time if EnqueuedAt is unset.
func GetEnqueueTime(t *api.QueueTask, modTime time.Time) time.Time {
	if !t.EnqueuedAt.IsZero() {
		return t.EnqueuedAt
	}
	if !modTime.IsZero() {
		return modTime
	}
	return t.CreatedAt
}

// PriorityRankValue converts a priority into an integer rank where lower numbers indicate higher priority.
func PriorityRankValue(p api.TaskPriority) int {
	priorityRank := map[api.TaskPriority]int{
		api.PriorityCritical:  1,
		api.PriorityUrgent:    2,
		api.PriorityImportant: 3,
		api.PriorityHigh:      4,
		api.PriorityMedium:    5,
		api.PriorityLow:       6,
		api.PriorityUnknown:   5,
	}
	if r, ok := priorityRank[api.TaskPriority(strings.ToLower(string(p)))]; ok {
		return r
	}
	return 5
}

// IsLessTask reports whether task a should be ordered before task b based on priority rank,
// phase rank, enqueue timestamp (FIFO), creation timestamp, and filename tiebreaking.
func IsLessTask(a, b api.TaskItem) bool {
	rankA := PriorityRankValue(a.Task.Priority)
	rankB := PriorityRankValue(b.Task.Priority)
	if rankA != rankB {
		return rankA < rankB
	}

	phaseA := a.Task.Phase
	if phaseA == 0 {
		phaseA = api.PhaseInvestigate
	}
	phaseB := b.Task.Phase
	if phaseB == 0 {
		phaseB = api.PhaseInvestigate
	}
	if phaseA != phaseB {
		return phaseA < phaseB
	}

	if !a.Task.EnqueuedAt.Equal(b.Task.EnqueuedAt) {
		return a.Task.EnqueuedAt.Before(b.Task.EnqueuedAt)
	}

	if !a.Task.CreatedAt.Equal(b.Task.CreatedAt) {
		return a.Task.CreatedAt.Before(b.Task.CreatedAt)
	}

	return a.Filename < b.Filename
}
