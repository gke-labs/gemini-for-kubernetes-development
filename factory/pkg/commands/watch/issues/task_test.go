package issues

import (
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func stringPtr(s string) *string { return &s }

func TestTriggerInfo(t *testing.T) {
	num := 42
	created := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	labelled := time.Date(2026, 8, 1, 11, 30, 0, 0, time.UTC)

	t.Run("Issue created with label (no later timeline label event)", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &created,
			Labels: []*githubv39.Label{
				{Name: stringPtr("factory")},
			},
		}

		eventTime, reason, notes := triggerInfo(issue, nil, "factory", false)
		if !eventTime.Equal(created) {
			t.Errorf("expected eventTime %v, got %v", created, eventTime)
		}
		if reason != api.TriggerReasonIssueCreated {
			t.Errorf("expected reason %s, got %s", api.TriggerReasonIssueCreated, reason)
		}
		if !strings.Contains(notes, "Issue #42 created at 2026-08-01T10:00:00Z with trigger label 'factory'") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})

	t.Run("Issue labeled later by user", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &created,
			Labels: []*githubv39.Label{
				{Name: stringPtr("factory")},
			},
		}
		timeline := []*githubv39.Timeline{
			{
				Event:     stringPtr("labeled"),
				CreatedAt: &labelled,
				Label:     &githubv39.Label{Name: stringPtr("factory")},
				Actor:     &githubv39.User{Login: stringPtr("alice")},
			},
		}

		eventTime, reason, notes := triggerInfo(issue, timeline, "factory", false)
		if !eventTime.Equal(labelled) {
			t.Errorf("expected eventTime %v, got %v", labelled, eventTime)
		}
		if reason != api.TriggerReasonIssueLabeled {
			t.Errorf("expected reason %s, got %s", api.TriggerReasonIssueLabeled, reason)
		}
		if !strings.Contains(notes, "trigger label 'factory' added by alice at 2026-08-01T11:30:00Z") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})

	t.Run("Issue auto-labeled by watcher", func(t *testing.T) {
		issue := &githubv39.Issue{
			Number:    &num,
			CreatedAt: &created,
			User:      &githubv39.User{Login: stringPtr("bob")},
		}

		eventTime, reason, notes := triggerInfo(issue, nil, "factory", true)
		if !eventTime.Equal(created) {
			t.Errorf("expected eventTime %v, got %v", created, eventTime)
		}
		if reason != api.TriggerReasonIssueCreated {
			t.Errorf("expected reason %s, got %s", api.TriggerReasonIssueCreated, reason)
		}
		if !strings.Contains(notes, "auto-applied by watcher") {
			t.Errorf("unexpected notes: %s", notes)
		}
	})
}

func TestProcessedIssueTimes(t *testing.T) {
	completedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	tasks := map[string]*api.QueueTask{
		"task-issue-100.yaml": {Type: api.TypeIssueFix, CompletedAt: completedAt},
		// A failed task leaves the issue still needing work, so it must not be
		// recorded: doing so would park the issue until someone touched it again.
		"task-issue-101.yaml": {Type: api.TypeIssueFix, Status: api.StatusFailed, CompletedAt: completedAt},
		// Pull request state belongs to the pull request scanner.
		"task-pr-200-comments.yaml": {Type: api.TypePRComments, CommitSHA: "sha200", CompletedAt: completedAt},
	}

	processed := processedIssueTimes(tasks)

	if got, ok := processed[100]; !ok {
		t.Error("issue 100 missing from the recovered state")
	} else if !got.Equal(completedAt) {
		t.Errorf("issue 100 recorded at %v, want the recorded completion time %v", got, completedAt)
	}
	if _, ok := processed[101]; ok {
		t.Error("issue 101 was recorded despite its task having failed")
	}
	if len(processed) != 1 {
		t.Errorf("recovered %d issues, want 1: %v", len(processed), processed)
	}
}

// TestProcessedIssueTimes_SkipsUndatedTasks pins the other half of the
// cooldown contract. The queue dates every task it hands back - falling back to
// the task file's own timestamp for one that recorded no completion time - so a
// zero here means the time could not be established at all, and says nothing
// about when the issue was last worked on.
func TestProcessedIssueTimes_SkipsUndatedTasks(t *testing.T) {
	processed := processedIssueTimes(map[string]*api.QueueTask{
		"task-issue-1.yaml": {Type: api.TypeIssueFix},
	})
	if _, ok := processed[1]; ok {
		t.Errorf("issue 1 was recorded from a task with no completion time: %v", processed)
	}
}
