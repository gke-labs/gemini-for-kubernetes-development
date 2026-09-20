package prs

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func TestFoldProcessedPRTask(t *testing.T) {
	initialState := prState{}

	// A review records only the commit it covered - there is no "reviewed at"
	// to gate on - so it must survive a task that carries no completion time.
	state := foldProcessedPRTask(&api.QueueTask{
		Type:      api.TypePRReview,
		CommitSHA: "abcd123",
	}, "task-pr-123-review", initialState)
	if state.lastReviewedSHA != "abcd123" {
		t.Errorf("expected lastReviewedSHA to be 'abcd123', got '%s'", state.lastReviewedSHA)
	}

	expectedCommentTime, _ := time.Parse(time.RFC3339, "2026-07-23T12:00:00Z")
	state = foldProcessedPRTask(&api.QueueTask{
		Type:        api.TypePRComments,
		CommitSHA:   "csha789",
		CompletedAt: expectedCommentTime,
	}, "task-pr-123-comments", state)
	if !state.lastCommentAddressedTime.Equal(expectedCommentTime) {
		t.Errorf("expected lastCommentAddressedTime to be %v, got %v", expectedCommentTime, state.lastCommentAddressedTime)
	}
	if state.lastCommentAddressedSHA != "csha789" {
		t.Errorf("expected lastCommentAddressedSHA to be 'csha789', got '%s'", state.lastCommentAddressedSHA)
	}

	expectedInvestigateTime, _ := time.Parse(time.RFC3339, "2026-07-23T13:00:00Z")
	state = foldProcessedPRTask(&api.QueueTask{
		Type:        api.TypePRInvestigate,
		CommitSHA:   "invsha123",
		CompletedAt: expectedInvestigateTime,
	}, "task-pr-123-investigate", state)
	if !state.lastInvestigatedTime.Equal(expectedInvestigateTime) {
		t.Errorf("expected lastInvestigatedTime to be %v, got %v", expectedInvestigateTime, state.lastInvestigatedTime)
	}
	if state.lastInvestigatedSHA != "invsha123" {
		t.Errorf("expected lastInvestigatedSHA to be 'invsha123', got '%s'", state.lastInvestigatedSHA)
	}

	expectedIterateTime, _ := time.Parse(time.RFC3339, "2026-07-23T14:00:00Z")
	state = foldProcessedPRTask(&api.QueueTask{
		Type:        api.TypePRIterate,
		CommitSHA:   "efgh456",
		CompletedAt: expectedIterateTime,
	}, "task-pr-123-iterate", state)
	if !state.lastIteratedTime.Equal(expectedIterateTime) {
		t.Errorf("expected lastIteratedTime to be %v, got %v", expectedIterateTime, state.lastIteratedTime)
	}
	if state.lastIteratedSHA != "efgh456" {
		t.Errorf("expected lastIteratedSHA to be 'efgh456', got '%s'", state.lastIteratedSHA)
	}

	// A failed task did not actually do the work, so folding it in would
	// suppress the retry.
	failedAt, _ := time.Parse(time.RFC3339, "2026-07-23T20:00:00Z")
	state = foldProcessedPRTask(&api.QueueTask{
		Type:        api.TypePRComments,
		Status:      api.StatusFailed,
		CompletedAt: failedAt,
	}, "task-pr-123-comments", state)
	if !state.lastCommentAddressedTime.Equal(expectedCommentTime) {
		t.Errorf("expected lastCommentAddressedTime to remain unchanged when task is Failed, got %v", state.lastCommentAddressedTime)
	}

	// For iterate (rebase) tasks, even if it failed, we want to update the lastIteratedSHA
	// and lastIteratedTime so we can skip rescheduling it on the same commit SHA.
	failedIterateAt, _ := time.Parse(time.RFC3339, "2026-07-23T21:00:00Z")
	state = foldProcessedPRTask(&api.QueueTask{
		Type:        api.TypePRIterate,
		Status:      api.StatusFailed,
		CommitSHA:   "failed-sha-123",
		CompletedAt: failedIterateAt,
	}, "task-pr-123-iterate", state)
	if !state.lastIteratedTime.Equal(failedIterateAt) {
		t.Errorf("expected lastIteratedTime to be updated to %v on failed iterate task, got %v", failedIterateAt, state.lastIteratedTime)
	}
	if state.lastIteratedSHA != "failed-sha-123" {
		t.Errorf("expected lastIteratedSHA to be 'failed-sha-123' on failed iterate task, got '%s'", state.lastIteratedSHA)
	}
}

func TestProcessedPRStates(t *testing.T) {
	at := func(s string) time.Time {
		parsed, _ := time.Parse(time.RFC3339, s)
		return parsed
	}

	prs := processedPRStates(map[string]*api.QueueTask{
		// Issue state belongs to the issue scanner, which recovers it from the
		// same set of finished tasks. This loader must ignore it rather than
		// silently claim it.
		"task-issue-100.yaml":               {Type: api.TypeIssueFix, CompletedAt: at("2026-08-01T10:00:00Z")},
		"task-pr-200-comments.yaml":         {Type: api.TypePRComments, CommitSHA: "sha200", CompletedAt: at("2026-08-01T11:00:00Z")},
		"task-pr-200-investigate.yaml":      {Type: api.TypePRInvestigate, CommitSHA: "sha-inv", CompletedAt: at("2026-08-01T12:00:00Z")},
		"task-pr-200-review.yaml":           {Type: api.TypePRReview, CommitSHA: "sha-rev", CompletedAt: at("2026-08-01T13:00:00Z")},
		"task-pr-200-iterate.yaml":          {Type: api.TypePRIterate, CommitSHA: "sha-iter", CompletedAt: at("2026-08-01T14:00:00Z")},
		"task-workflow-triage-issue-9.yaml": {Type: api.TypeAgentChore, CompletedAt: at("2026-08-01T15:00:00Z")},
	})

	if len(prs) != 1 {
		t.Errorf("recovered %d pull requests, want 1 (non-PR tasks must be ignored): %v", len(prs), prs)
	}
	state, ok := prs[200]
	if !ok {
		t.Fatalf("expected pr 200 in the recovered state")
	}
	if state.lastCommentAddressedSHA != "sha200" {
		t.Errorf("expected lastCommentAddressedSHA 'sha200', got %q", state.lastCommentAddressedSHA)
	}
	if state.lastInvestigatedSHA != "sha-inv" {
		t.Errorf("expected lastInvestigatedSHA 'sha-inv', got %q", state.lastInvestigatedSHA)
	}
	if state.lastReviewedSHA != "sha-rev" {
		t.Errorf("expected lastReviewedSHA 'sha-rev', got %q", state.lastReviewedSHA)
	}
	if state.lastIteratedSHA != "sha-iter" {
		t.Errorf("expected lastIteratedSHA 'sha-iter', got %q", state.lastIteratedSHA)
	}
}
