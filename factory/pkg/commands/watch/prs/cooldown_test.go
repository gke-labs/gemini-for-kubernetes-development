package prs

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// ci's cooldown counts from when the last investigation ended, not from
// when this process queued it (KCC PR 13982: queued at 23:16, ended 23:43,
// queued again at 01:17). A restart already counted from the end.
func TestLastInvestigatedCountsFromTheEnd(t *testing.T) {
	s, queue := newTestScanner(t, t.TempDir(), testOpts{BotUsers: []string{"bot1"}, TriggerLabel: "factory"})
	const fn = "task-pr-13982-investigate.yaml"
	queuedAt := time.Now().Add(-2*time.Hour - time.Minute)
	endedAt := time.Now().Add(-94 * time.Minute)
	state := prState{lastInvestigatedSHA: "head", lastInvestigatedTime: queuedAt}

	if got := s.lastInvestigated(fn, state); !got.Equal(queuedAt) {
		t.Errorf("no processed task: %v, want the queued time", got)
	}
	task := &api.QueueTask{Type: api.TypePRInvestigate, Number: 13982, CommitSHA: "head"}
	if err := queue.Enqueue(fn, task); err != nil {
		t.Fatal(err)
	}
	if err := queue.StartTask(fn, task); err != nil {
		t.Fatal(err)
	}
	task.CompletedAt = endedAt
	if err := queue.CompleteTask(fn, task); err != nil {
		t.Fatal(err)
	}
	if got := s.lastInvestigated(fn, state); !got.Equal(endedAt) {
		t.Errorf("ended %v: got %v, want the end", endedAt, got)
	}
	if time.Since(s.lastInvestigated(fn, state)) > investigationRetryAfter {
		t.Error("1h34m after it ended, the cooldown is over")
	}
	// A run on another head is not this head's.
	other := prState{lastInvestigatedSHA: "new-head", lastInvestigatedTime: queuedAt}
	if got := s.lastInvestigated(fn, other); !got.Equal(queuedAt) {
		t.Errorf("another head's run: %v, want the queued time", got)
	}
}
