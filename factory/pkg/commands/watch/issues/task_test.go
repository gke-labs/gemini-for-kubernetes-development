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
