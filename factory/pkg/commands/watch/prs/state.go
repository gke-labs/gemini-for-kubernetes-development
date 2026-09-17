package prs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// prState records what has already been done for a pull request, and is what
// stops the scanner from queueing the same work twice.
//
// Almost every field is a commit SHA rather than a timestamp, because the
// question being asked is "did we already do this for *this* revision?" - a new
// commit is exactly what makes a previous investigation, review or rebase worth
// repeating.
type prState struct {
	// lastInvestigatedTime is when a CI failure investigation was last queued or completed.
	lastInvestigatedTime time.Time
	// lastInvestigatedSHA is the head commit SHA when CI failures were last investigated.
	lastInvestigatedSHA string
	// lastCommentAddressedTime is when review feedback was last addressed by the bot.
	lastCommentAddressedTime time.Time
	// lastCommentAddressedSHA is the head commit SHA when review comments were
	// last addressed, which prevents processing the same feedback twice on a
	// commit the agent decided needed no change.
	lastCommentAddressedSHA string
	// lastReviewedSHA is the commit SHA for which an automated review was last queued or completed.
	lastReviewedSHA string
	// lastIteratedSHA is the commit SHA for which a rebase was last queued or completed.
	lastIteratedSHA string
	// lastIteratedTime is when a rebase was last queued or completed.
	lastIteratedTime time.Time
}

// stateStore holds the per-pull-request gating state.
//
// It stays inside this package rather than moving to the shared
// concurrency.EntityStateCache because nothing outside the PR scanner reads it:
// the shared cache exists for state that crosses subcontroller boundaries,
// and putting single-owner bookkeeping there would make it look shared when it
// is not.
//
// The mutex is needed because the scanner evaluates pull requests on a worker
// pool. Contention is not a concern - the critical sections are map lookups
// between GitHub round trips.
type stateStore struct {
	mu sync.Mutex
	// processedDir is the queue directory holding completed task files.
	processedDir string
	// byNumber is nil until the first access, at which point it is recovered
	// from disk. Loading lazily keeps construction free of I/O, which is what
	// lets a test build a Scanner without laying out a queue directory.
	byNumber map[int]prState
}

// newStateStore returns a store that recovers its contents from processedDir on
// first use.
func newStateStore(processedDir string) *stateStore {
	return &stateStore{processedDir: processedDir}
}

// get returns the recorded state for a pull request, or the zero state when
// nothing has been done for it yet.
func (s *stateStore) get(num int) prState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byNumber == nil {
		s.byNumber = loadProcessedPRs(s.processedDir)
	}
	return s.byNumber[num]
}

// set records the state for a pull request.
func (s *stateStore) set(num int, state prState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byNumber == nil {
		s.byNumber = loadProcessedPRs(s.processedDir)
	}
	s.byNumber[num] = state
}

// loadProcessedPRs recovers the gating state from the completed task files on
// disk, so that a restart does not re-run work that has already been done for a
// commit.
func loadProcessedPRs(processedDir string) map[int]prState {
	processedPRs := make(map[int]prState)
	files, err := os.ReadDir(processedDir)
	if err != nil {
		return processedPRs
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") || !strings.HasPrefix(f.Name(), "task-pr-") {
			continue
		}
		filePath := filepath.Join(processedDir, f.Name())
		name := strings.TrimPrefix(f.Name(), "task-pr-")
		name = strings.TrimSuffix(name, ".yaml")

		isComments := strings.HasSuffix(name, "-comments")
		isInvestigate := strings.HasSuffix(name, "-investigate")
		isReview := strings.HasSuffix(name, "-review")
		isIterate := strings.HasSuffix(name, "-iterate")

		var numStr string
		if isComments {
			numStr = strings.TrimSuffix(name, "-comments")
		} else if isInvestigate {
			numStr = strings.TrimSuffix(name, "-investigate")
		} else if isReview {
			numStr = strings.TrimSuffix(name, "-review")
		} else if isIterate {
			numStr = strings.TrimSuffix(name, "-iterate")
		}

		if numStr != "" {
			if num, err := strconv.Atoi(numStr); err == nil {
				state := processedPRs[num]
				info, _ := f.Info()
				processedPRs[num] = parseProcessedPRTask(filePath, name, info, state)
			}
		}
	}
	return processedPRs
}

// parseProcessedPRTask folds one completed task file into the state of its pull
// request.
//
// A failed task is folded in as nothing at all: the work it represents did not
// actually happen, so recording it would suppress the retry.
func parseProcessedPRTask(filePath string, name string, fInfo os.FileInfo, state prState) prState {
	isComments := strings.HasSuffix(name, "-comments")
	isInvestigate := strings.HasSuffix(name, "-investigate")
	isReview := strings.HasSuffix(name, "-review")
	isIterate := strings.HasSuffix(name, "-iterate")

	var t api.QueueTask
	hasTask := false
	if data, err := os.ReadFile(filePath); err == nil {
		if err := yaml.Unmarshal(data, &t); err == nil {
			hasTask = true
			if strings.EqualFold(string(t.Status), string(api.StatusFailed)) {
				return state
			}
		}
	}

	if fInfo != nil {
		// The file's modification time is only a stand-in for when the task
		// finished; the task's own timestamp is preferred whenever it recorded one.
		tTime := fInfo.ModTime()
		if hasTask && !t.CompletedAt.IsZero() {
			tTime = t.CompletedAt
		}
		if isComments {
			if tTime.After(state.lastCommentAddressedTime) {
				state.lastCommentAddressedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastCommentAddressedSHA = t.CommitSHA
			}
		} else if isInvestigate {
			if tTime.After(state.lastInvestigatedTime) {
				state.lastInvestigatedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastInvestigatedSHA = t.CommitSHA
			}
		} else if isReview {
			if hasTask && t.CommitSHA != "" {
				state.lastReviewedSHA = t.CommitSHA
			}
		} else if isIterate {
			if tTime.After(state.lastIteratedTime) {
				state.lastIteratedTime = tTime
			}
			if hasTask && t.CommitSHA != "" {
				state.lastIteratedSHA = t.CommitSHA
			}
		}
	}
	return state
}
