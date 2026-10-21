package prs

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// A restart recovers when feedback was last addressed from the finished
// comments tasks, the latest enqueue, failed or not; other tasks record
// nothing.
func TestProcessedPRStatesRecoverTheAddressedTime(t *testing.T) {
	early := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	states := processedPRStates(map[string]*api.QueueTask{
		"task-pr-7-comments.yaml":    {EnqueuedAt: late, Status: api.StatusFailed},
		"task-pr-7-investigate.yaml": {EnqueuedAt: late, CompletedAt: late, CommitSHA: "abc"},
		"task-pr-8-review.yaml":      {EnqueuedAt: early, CompletedAt: early},
		"task-issue-9.yaml":          {EnqueuedAt: early},
	})
	if got := states[7].lastCommentAddressedTime; !got.Equal(late) {
		t.Errorf("#7 addressed at %v, want %v", got, late)
	}
	if got := states[8].lastCommentAddressedTime; !got.IsZero() {
		t.Errorf("#8 (a review) addressed at %v, want none", got)
	}
	if _, ok := states[9]; ok {
		t.Error("an issue's task recovered as a PR's")
	}
}
