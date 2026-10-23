package prs

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// prState records what has already been done for a pull request, and is what
// stops the scanner from queueing the same work twice.
//
// Almost every field is a commit SHA rather than a timestamp, because the
// question being asked is "did we already do this for *this* revision?" - a new
// commit is exactly what makes a previous investigation, review or rebase worth
// repeating.
type prState struct {
	// lastCommentAddressedTime is when review feedback was last addressed by the bot.
	lastCommentAddressedTime time.Time

	// The three fields below record what the last *complete* evaluation saw,
	// and exist for the fast pass's skip gate (see Scanner.needsEvaluation).
	// They are set only when an evaluation runs all the way through, so a
	// pull request abandoned half way - a failed history fetch, a merge queue
	// bail-out - is never mistaken for one that has been fully accounted for.
	//
	// Unlike the field above they are not recovered from the queue on
	// startup, and that is deliberate: a fresh process has no idea what has
	// happened while it was down, so every pull request earns one full
	// evaluation before it can be skipped.

	// lastEvaluatedUpdatedAt is the pull request's updated_at at that
	// evaluation. A zero value means it has never been evaluated in this
	// process.
	lastEvaluatedUpdatedAt time.Time
	// lastEvaluationActive records whether CI was pending or failing, or
	// GitHub had not yet computed whether the pull request merges cleanly.
	// None of those transitions move updated_at, so a pull request in any of
	// those states cannot be skipped on that timestamp alone.
	lastEvaluationActive bool
	// lastEvaluationHadTask records whether a task for this pull request was
	// queued or running. The transition out of that state is what makes a
	// pull request ready for a human, and it is invisible in updated_at when
	// the task finished without pushing anything.
	lastEvaluationHadTask bool
}

// stateStore holds the per-pull-request gating state.
//
// It stays inside this package rather than moving to the shared
// concurrency.EntityStateCache because nothing outside the PR scanner reads it:
// the shared cache exists for state that crosses subcontroller boundaries,
// and putting single-owner bookkeeping there would make it look shared when it
// is not.
//
// The scanner evaluates pull requests one at a time (evaluateAll), so the
// mutex only guards against that changing; it costs nothing, since the
// critical sections are map lookups between GitHub round trips.
type stateStore struct {
	mu sync.Mutex
	// queue is where the finished tasks are recovered from. The scanner does
	// not read the task files itself: the queue owns them.
	queue Queue
	// byNumber is nil until the first access, at which point it is recovered
	// from the queue. Loading lazily keeps construction free of I/O, which is
	// what lets a test build a Scanner without a populated queue.
	byNumber map[int]prState
}

// newStateStore returns a store that recovers its contents from the queue's
// finished tasks on first use.
func newStateStore(queue Queue) *stateStore {
	return &stateStore{queue: queue}
}

// get returns the recorded state for a pull request, or the zero state when
// nothing has been done for it yet.
func (s *stateStore) get(num int) prState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverLocked()
	return s.byNumber[num]
}

// set records the state for a pull request.
func (s *stateStore) set(num int, state prState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverLocked()
	s.byNumber[num] = state
}

// clearEvaluation forgets the last recorded evaluation for a pull request, so
// an evaluation that returns before reaching recordEvaluation is never mistaken
// by the next fast pass for one that completed cleanly.
func (s *stateStore) clearEvaluation(num int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverLocked()
	state, ok := s.byNumber[num]
	if !ok {
		return
	}
	state.lastEvaluatedUpdatedAt = time.Time{}
	state.lastEvaluationActive = false
	state.lastEvaluationHadTask = false
	s.byNumber[num] = state
}

// recoverLocked populates the store from the queue on first access. The caller
// must hold s.mu.
func (s *stateStore) recoverLocked() {
	if s.byNumber != nil {
		return
	}
	var tasks map[string]*api.QueueTask
	if s.queue != nil {
		tasks = s.queue.ListProcessedTasks()
	}
	s.byNumber = processedPRStates(tasks)
}

// processedPRStates recovers the gating state from the queue's finished tasks,
// so that a restart does not re-run work that has already been done for a
// commit.
func processedPRStates(tasks map[string]*api.QueueTask) map[int]prState {
	processedPRs := make(map[int]prState)
	for filename, t := range tasks {
		if t == nil || !strings.HasPrefix(filename, "task-pr-") {
			continue
		}
		// task-pr-<n>-rule-<rule>.yaml (api.RuleTaskFile).
		numStr, _, ok := strings.Cut(strings.TrimPrefix(filename, "task-pr-"), "-rule-")
		if !ok {
			continue
		}
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		processedPRs[num] = foldProcessedPRTask(t, processedPRs[num])
	}
	return processedPRs
}

// foldProcessedPRTask folds one finished task into the state of its pull
// request.
//
// For a task that acknowledges the feedback it answers (the comments
// rule's, acknowledge: feedback), the enqueue timestamp is recorded even when the
// task failed so that a restart does not mistake pre-existing comments for new
// ones while still picking up comments posted while the task was running;
// retries of a failed address-comments task are the comments rule's
// (last-run: failed). The other tasks record nothing here: the rules read
// their runs from the queue.
func foldProcessedPRTask(t *api.QueueTask, state prState) prState {
	// The queue dates every finished task, falling back to the task file's own
	// timestamp for one that recorded no completion time.
	tTime := t.CompletedAt

	if t.Acknowledge == rules.AcknowledgeFeedback {
		cTime := t.EnqueuedAt
		if cTime.IsZero() {
			cTime = t.StartedAt
		}
		if cTime.IsZero() {
			cTime = tTime
		}
		if cTime.After(state.lastCommentAddressedTime) {
			state.lastCommentAddressedTime = cTime
		}
		return state
	}

	return state
}

// MarkCommentsAddressed records that a comments task enqueued at at has
// started on PR num: feedback until then is the task's to answer, and no
// longer new to the next cycle (NotifyTaskStarted). A restarted watcher
// recovers the same from the task once it has finished
// (processedPRStates).
func (s *Scanner) MarkCommentsAddressed(num int, at time.Time) {
	state := s.state.get(num)
	if at.After(state.lastCommentAddressedTime) {
		state.lastCommentAddressedTime = at
		s.state.set(num, state)
	}
}
