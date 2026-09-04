package concurrency

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// priorityQueueNode wraps a task and its filename with its index in the binary heap.
// If the task is currently not in the ready heap (e.g. claimed as candidate), index is -1.
type priorityQueueNode struct {
	filename string
	task     *api.QueueTask
	index    int // Index in taskHeap; -1 if not in ready heap
}

// taskHeap implements heap.Interface for priorityQueueNode pointers, ordered by IsLessTask.
type taskHeap []*priorityQueueNode

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	return IsLessTask(
		api.TaskItem{Filename: h[i].filename, Task: h[i].task},
		api.TaskItem{Filename: h[j].filename, Task: h[j].task},
	)
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x any) {
	node := x.(*priorityQueueNode)
	node.index = len(*h)
	*h = append(*h, node)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	node := old[n-1]
	old[n-1] = nil
	node.index = -1
	*h = old[0 : n-1]
	return node
}

// TaskPriorityQueue is a priority queue data structure for incoming queue tasks.
// It maintains ready tasks in a binary min-heap ordered by priority and arrival (via IsLessTask),
// and provides O(1) lookups by filename.
type TaskPriorityQueue struct {
	ready      taskHeap
	byFilename map[string]*priorityQueueNode
}

// NewTaskPriorityQueue creates an empty TaskPriorityQueue.
func NewTaskPriorityQueue() *TaskPriorityQueue {
	return &TaskPriorityQueue{
		ready:      make(taskHeap, 0),
		byFilename: make(map[string]*priorityQueueNode),
	}
}

// Len returns the total number of tasks in the priority queue.
func (pq *TaskPriorityQueue) Len() int {
	return len(pq.byFilename)
}

// Get retrieves a task by its filename, returning whether it was found.
func (pq *TaskPriorityQueue) Get(filename string) (*api.QueueTask, bool) {
	if node, ok := pq.byFilename[filename]; ok {
		return node.task, true
	}
	return nil, false
}

// Contains reports whether a task with the given filename exists in the queue.
func (pq *TaskPriorityQueue) Contains(filename string) bool {
	_, ok := pq.byFilename[filename]
	return ok
}

// Enqueue adds a task to the priority queue, or updates it if it already exists.
func (pq *TaskPriorityQueue) Enqueue(filename string, task *api.QueueTask) {
	if node, ok := pq.byFilename[filename]; ok {
		node.task = task
		if node.index >= 0 {
			heap.Fix(&pq.ready, node.index)
		} else {
			heap.Push(&pq.ready, node)
		}
		return
	}

	node := &priorityQueueNode{
		filename: filename,
		task:     task,
		index:    -1,
	}
	pq.byFilename[filename] = node
	heap.Push(&pq.ready, node)
}

// Release returns a task back to the ready heap if it is not currently in the heap.
// Returns false if the task is not found.
func (pq *TaskPriorityQueue) Release(filename string) bool {
	node, ok := pq.byFilename[filename]
	if !ok {
		return false
	}
	if node.index < 0 {
		heap.Push(&pq.ready, node)
	} else {
		heap.Fix(&pq.ready, node.index)
	}
	return true
}

// ClaimCandidate pops the highest priority eligible task from the ready heap
// without removing it from byFilename (its index is set to -1).
// This reserves the task so other calls will not claim it, while keeping it tracked
// in incoming until either StartTask moves it or Release/Remove cleans it up.
// Returns ("", nil) if no eligible tasks are available.
func (pq *TaskPriorityQueue) ClaimCandidate() (string, *api.QueueTask) {
	if len(pq.ready) == 0 {
		return "", nil
	}

	node := heap.Pop(&pq.ready).(*priorityQueueNode)
	node.index = -1
	return node.filename, node.task
}

// Remove removes a task by filename from the queue (whether ready or delayed).
// Returns the removed task and true, or (nil, false) if not found.
func (pq *TaskPriorityQueue) Remove(filename string) (*api.QueueTask, bool) {
	node, ok := pq.byFilename[filename]
	if !ok {
		return nil, false
	}

	if node.index >= 0 {
		heap.Remove(&pq.ready, node.index)
		node.index = -1
	}
	delete(pq.byFilename, filename)
	return node.task, true
}

// UpdatePriority updates the priority tier of a task and adjusts its position in the heap.
// Returns true if the task was found and updated, false otherwise.
func (pq *TaskPriorityQueue) UpdatePriority(filename string, priority api.TaskPriority) bool {
	node, ok := pq.byFilename[filename]
	if !ok {
		return false
	}
	node.task.Priority = priority
	if node.index >= 0 {
		heap.Fix(&pq.ready, node.index)
	}
	return true
}

// HasActivePRTask reports whether any task in the queue belongs to the specified PR number,
// either by task metadata or by filename prefix.
func (pq *TaskPriorityQueue) HasActivePRTask(prNumber int) bool {
	prefix := fmt.Sprintf("task-pr-%d-", prNumber)
	for fn, node := range pq.byFilename {
		if (node.task.Number == prNumber && api.IsPRTask(node.task.Type)) || strings.HasPrefix(fn, prefix) {
			return true
		}
	}
	return false
}

// RemoveMatching removes all tasks satisfying the predicate function and returns their filenames.
func (pq *TaskPriorityQueue) RemoveMatching(predicate func(filename string, task *api.QueueTask) bool) []string {
	var matched []string
	for fn, node := range pq.byFilename {
		if predicate(fn, node.task) {
			matched = append(matched, fn)
		}
	}
	for _, fn := range matched {
		pq.Remove(fn)
	}
	return matched
}

// ToSortedSlice returns all tasks in the queue sorted by IsLessTask.
func (pq *TaskPriorityQueue) ToSortedSlice() []api.TaskItem {
	items := make([]api.TaskItem, 0, len(pq.byFilename))
	for fn, node := range pq.byFilename {
		items = append(items, api.TaskItem{
			Filename: fn,
			Task:     node.task,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return IsLessTask(items[i], items[j])
	})
	return items
}

// Reset clears all tasks and state from the priority queue.
func (pq *TaskPriorityQueue) Reset() {
	pq.ready = make(taskHeap, 0)
	pq.byFilename = make(map[string]*priorityQueueNode)
}
