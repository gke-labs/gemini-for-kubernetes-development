package concurrency

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func TestTaskPriorityQueue_PriorityOrder(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	tasks := []struct {
		filename string
		priority api.TaskPriority
		phase    api.TaskPhase
	}{
		{"low.yaml", api.PriorityLow, api.PhaseInvestigate},
		{"med.yaml", api.PriorityMedium, api.PhaseInvestigate},
		{"crit.yaml", api.PriorityCritical, api.PhaseInvestigate},
		{"high.yaml", api.PriorityHigh, api.PhaseInvestigate},
		{"urg.yaml", api.PriorityUrgent, api.PhaseInvestigate},
	}

	for _, tc := range tasks {
		pq.Enqueue(tc.filename, &api.QueueTask{
			Priority:   tc.priority,
			Phase:      tc.phase,
			EnqueuedAt: baseTime,
			CreatedAt:  baseTime,
		})
	}

	expectedOrder := []string{"crit.yaml", "urg.yaml", "high.yaml", "med.yaml", "low.yaml"}
	for _, exp := range expectedOrder {
		fn, task := pq.ClaimCandidate()
		if task == nil {
			t.Fatalf("expected task %s, got nil", exp)
		}
		if fn != exp {
			t.Errorf("expected %s, got %s", exp, fn)
		}
	}

	if fn, task := pq.ClaimCandidate(); task != nil || fn != "" {
		t.Errorf("expected empty queue, got %s", fn)
	}
}

func TestTaskPriorityQueue_FIFO(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("second.yaml", &api.QueueTask{
		Priority:   api.PriorityMedium,
		Phase:      api.PhaseInvestigate,
		EnqueuedAt: baseTime.Add(2 * time.Minute),
	})
	pq.Enqueue("first.yaml", &api.QueueTask{
		Priority:   api.PriorityMedium,
		Phase:      api.PhaseInvestigate,
		EnqueuedAt: baseTime.Add(1 * time.Minute),
	})
	pq.Enqueue("third.yaml", &api.QueueTask{
		Priority:   api.PriorityMedium,
		Phase:      api.PhaseInvestigate,
		EnqueuedAt: baseTime.Add(3 * time.Minute),
	})

	expected := []string{"first.yaml", "second.yaml", "third.yaml"}
	for _, exp := range expected {
		fn, task := pq.ClaimCandidate()
		if task == nil || fn != exp {
			t.Errorf("expected %s, got %s", exp, fn)
		}
	}
}

func TestTaskPriorityQueue_UpdatePriority(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("task-a.yaml", &api.QueueTask{
		Priority:   api.PriorityLow,
		EnqueuedAt: baseTime,
	})
	pq.Enqueue("task-b.yaml", &api.QueueTask{
		Priority:   api.PriorityMedium,
		EnqueuedAt: baseTime,
	})

	// Elevate task-a to Critical
	if !pq.UpdatePriority("task-a.yaml", api.PriorityCritical) {
		t.Fatal("failed to update priority")
	}

	// Now task-a should be first
	fn, task := pq.ClaimCandidate()
	if fn != "task-a.yaml" || task.Priority != api.PriorityCritical {
		t.Fatalf("expected task-a.yaml with Critical priority, got %s (%s)", fn, task.Priority)
	}
	fn2, task2 := pq.ClaimCandidate()
	if fn2 != "task-b.yaml" || task2.Priority != api.PriorityMedium {
		t.Fatalf("expected task-b.yaml with Medium priority, got %s", fn2)
	}
}

func TestTaskPriorityQueue_Remove(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("task-1.yaml", &api.QueueTask{Priority: api.PriorityHigh, EnqueuedAt: baseTime})
	pq.Enqueue("task-2.yaml", &api.QueueTask{Priority: api.PriorityCritical, EnqueuedAt: baseTime})

	removed, ok := pq.Remove("task-2.yaml")
	if !ok || removed == nil {
		t.Fatal("expected successful remove of task-2.yaml")
	}

	fn, task := pq.ClaimCandidate()
	if fn != "task-1.yaml" || task == nil {
		t.Fatalf("expected task-1.yaml, got %s", fn)
	}
	pq.Remove(fn)

	if pq.Len() != 0 {
		t.Errorf("expected empty queue, got %d", pq.Len())
	}
}

func TestTaskPriorityQueue_ReleaseCandidate(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("crit.yaml", &api.QueueTask{
		Priority:   api.PriorityCritical,
		EnqueuedAt: baseTime,
	})
	pq.Enqueue("med.yaml", &api.QueueTask{
		Priority:   api.PriorityMedium,
		EnqueuedAt: baseTime,
	})

	// Claim crit as candidate
	fn, task := pq.ClaimCandidate()
	if fn != "crit.yaml" || task == nil {
		t.Fatalf("expected crit.yaml candidate, got %s", fn)
	}

	// While crit is claimed as candidate, next claim returns med
	fnMed, taskMed := pq.ClaimCandidate()
	if fnMed != "med.yaml" || taskMed == nil {
		t.Fatalf("expected med.yaml candidate, got %s", fnMed)
	}

	// While both are claimed, next claim returns empty
	fnEmpty, taskEmpty := pq.ClaimCandidate()
	if fnEmpty != "" || taskEmpty != nil {
		t.Fatalf("expected empty claim while all claimed, got %s", fnEmpty)
	}

	// Release crit
	if ok := pq.Release("crit.yaml"); !ok {
		t.Fatalf("expected Release(crit.yaml) to succeed")
	}

	// Now crit should be available to claim again
	fnPromoted, taskPromoted := pq.ClaimCandidate()
	if fnPromoted != "crit.yaml" || taskPromoted == nil {
		t.Fatalf("expected crit.yaml after release, got %s", fnPromoted)
	}
}

func TestTaskPriorityQueue_Release(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("task-1.yaml", &api.QueueTask{Priority: api.PriorityHigh, EnqueuedAt: baseTime})

	fn, _ := pq.ClaimCandidate()
	if fn != "task-1.yaml" {
		t.Fatalf("expected task-1.yaml, got %s", fn)
	}

	// Candidate claimed: ready queue is empty
	if fn, task := pq.ClaimCandidate(); fn != "" || task != nil {
		t.Fatalf("expected empty claim for claimed candidate, got %s", fn)
	}

	// Releasing puts it back in ready queue
	if ok := pq.Release("task-1.yaml"); !ok {
		t.Fatalf("expected Release to succeed")
	}

	fn, _ = pq.ClaimCandidate()
	if fn != "task-1.yaml" {
		t.Errorf("expected task-1.yaml, got %s", fn)
	}
}

func TestTaskPriorityQueue_ToSortedSlice(t *testing.T) {
	pq := NewTaskPriorityQueue()
	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	pq.Enqueue("task-med.yaml", &api.QueueTask{Priority: api.PriorityMedium, EnqueuedAt: baseTime.Add(1 * time.Minute)})
	pq.Enqueue("task-crit.yaml", &api.QueueTask{Priority: api.PriorityCritical, EnqueuedAt: baseTime})
	pq.Enqueue("task-low.yaml", &api.QueueTask{Priority: api.PriorityLow, EnqueuedAt: baseTime})

	// Claim candidate for critical task
	fn, _ := pq.ClaimCandidate()
	if fn != "task-crit.yaml" {
		t.Fatalf("expected task-crit.yaml, got %s", fn)
	}

	// ToSortedSlice includes all tracked tasks in true priority order
	slice := pq.ToSortedSlice()
	if len(slice) != 3 {
		t.Fatalf("expected 3 items, got %d", len(slice))
	}
	expected := []string{"task-crit.yaml", "task-med.yaml", "task-low.yaml"}
	for i, exp := range expected {
		if slice[i].Filename != exp {
			t.Errorf("at index %d: expected %s, got %s", i, exp, slice[i].Filename)
		}
	}
}

func TestTaskPriorityQueue_HasActivePRTask(t *testing.T) {
	pq := NewTaskPriorityQueue()

	pq.Enqueue("task-issue-10.yaml", &api.QueueTask{Type: api.TypeIssueFix, Number: 10})
	pq.Enqueue("task-pr-42-review.yaml", &api.QueueTask{Type: api.TypePRReview, Number: 42})

	if !pq.HasActivePRTask(42) {
		t.Error("expected HasActivePRTask(42) to be true")
	}
	if pq.HasActivePRTask(10) {
		t.Error("expected HasActivePRTask(10) to be false for non-PR task")
	}
	if pq.HasActivePRTask(99) {
		t.Error("expected HasActivePRTask(99) to be false for absent PR")
	}
}

func TestTaskPriorityQueue_RemoveMatching(t *testing.T) {
	pq := NewTaskPriorityQueue()

	pq.Enqueue("keep.yaml", &api.QueueTask{Priority: api.PriorityHigh})
	pq.Enqueue("drop-1.yaml", &api.QueueTask{Priority: api.PriorityLow})
	pq.Enqueue("drop-2.yaml", &api.QueueTask{Priority: api.PriorityLow})

	removed := pq.RemoveMatching(func(fn string, t *api.QueueTask) bool {
		return t.Priority == api.PriorityLow
	})

	if len(removed) != 2 {
		t.Fatalf("expected 2 removed items, got %d", len(removed))
	}
	if pq.Len() != 1 {
		t.Fatalf("expected 1 item left, got %d", pq.Len())
	}
	fn, _ := pq.ClaimCandidate()
	if fn != "keep.yaml" {
		t.Fatalf("expected keep.yaml, got %s", fn)
	}
}

func TestTaskPriorityQueue_Contains_Get_Reset(t *testing.T) {
	pq := NewTaskPriorityQueue()

	task := &api.QueueTask{Priority: api.PriorityMedium}
	pq.Enqueue("item.yaml", task)

	if !pq.Contains("item.yaml") {
		t.Error("expected Contains(item.yaml) to be true")
	}
	if pq.Contains("absent.yaml") {
		t.Error("expected Contains(absent.yaml) to be false")
	}

	gotTask, ok := pq.Get("item.yaml")
	if !ok || gotTask != task {
		t.Errorf("expected Get(item.yaml) to return task, got ok=%v", ok)
	}

	pq.Reset()
	if pq.Len() != 0 {
		t.Errorf("expected Len=0 after Reset, got %d", pq.Len())
	}
	if pq.Contains("item.yaml") {
		t.Error("expected Contains to be false after Reset")
	}
}

func TestTaskPriorityQueue_ClaimCandidate(t *testing.T) {
	pq := NewTaskPriorityQueue()

	task1 := &api.QueueTask{Priority: api.PriorityHigh, Number: 1}
	task2 := &api.QueueTask{Priority: api.PriorityMedium, Number: 2}

	pq.Enqueue("task1.yaml", task1)
	pq.Enqueue("task2.yaml", task2)

	// Claim candidate should pop task1 from ready heap, but keep it in byFilename
	fn, claimed := pq.ClaimCandidate()
	if fn != "task1.yaml" || claimed != task1 {
		t.Fatalf("expected task1.yaml, got %s", fn)
	}

	// It should still be contained in pq and accessible via Get
	if !pq.Contains("task1.yaml") {
		t.Errorf("expected pq.Contains(task1.yaml) to be true for claimed candidate")
	}
	if got, ok := pq.Get("task1.yaml"); !ok || got != task1 {
		t.Errorf("expected Get(task1.yaml) to succeed, got ok=%v", ok)
	}

	// Claiming next candidate should return task2 (task1 is reserved)
	fn2, claimed2 := pq.ClaimCandidate()
	if fn2 != "task2.yaml" || claimed2 != task2 {
		t.Fatalf("expected task2.yaml, got %s", fn2)
	}

	// No more candidates in ready heap
	fnEmpty, claimedEmpty := pq.ClaimCandidate()
	if fnEmpty != "" || claimedEmpty != nil {
		t.Fatalf("expected empty candidate, got %s", fnEmpty)
	}

	// Releasing candidate task1 should return it to the ready heap
	if ok := pq.Release("task1.yaml"); !ok {
		t.Fatalf("expected Release(task1.yaml) to succeed")
	}
	if fnAgain, _ := pq.ClaimCandidate(); fnAgain != "task1.yaml" {
		t.Errorf("expected task1 to be claimed after release, got %s", fnAgain)
	}

	// Removing task2 should remove it completely
	if _, ok := pq.Remove("task2.yaml"); !ok {
		t.Errorf("expected Remove(task2.yaml) to succeed")
	}
	if pq.Contains("task2.yaml") {
		t.Errorf("expected task2.yaml not to be contained after Remove")
	}
}
