package issues

import (
	"os"
	"path/filepath"
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

func TestLoadProcessedIssues(t *testing.T) {
	dir := t.TempDir()

	completedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	write("task-issue-100.yaml", "type: issue-fix\ncompletedAt: \"2026-08-01T10:00:00Z\"\n")
	// A failed task leaves the issue still needing work, so it must not be
	// recorded: doing so would park the issue until someone touched it again.
	write("task-issue-101.yaml", "type: issue-fix\nstatus: Failed\ncompletedAt: \"2026-08-01T10:00:00Z\"\n")
	// Pull request state belongs to the pull request scanner.
	write("task-pr-200-comments.yaml", "type: pr-comments\ncommitSHA: sha200\n")

	processed := loadProcessedIssues(dir)

	if got, ok := processed[100]; !ok {
		t.Error("issue 100 missing from the loaded state")
	} else if !got.Equal(completedAt) {
		t.Errorf("issue 100 recorded at %v, want the recorded completion time %v", got, completedAt)
	}
	if _, ok := processed[101]; ok {
		t.Error("issue 101 was recorded despite its task having failed")
	}
	if len(processed) != 1 {
		t.Errorf("loaded %d issues, want 1: %v", len(processed), processed)
	}
}

// TestLoadProcessedIssues_FallsBackToModTime pins the behaviour a workflow
// cooldown depends on: the recorded completion time wins when the file carries
// one, and the file's own timestamp stands in when it does not.
func TestLoadProcessedIssues_FallsBackToModTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-issue-1.yaml")
	if err := os.WriteFile(path, []byte("type: issue-fix\n"), 0644); err != nil {
		t.Fatalf("writing task: %v", err)
	}
	modTime := time.Now().Add(-5 * time.Hour)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("setting mtime: %v", err)
	}

	processed := loadProcessedIssues(dir)
	got, ok := processed[1]
	if !ok {
		t.Fatal("issue 1 missing from the loaded state")
	}
	if !got.Equal(modTime.Truncate(time.Second)) && got.Sub(modTime).Abs() > time.Second {
		t.Errorf("issue 1 recorded at %v, want the file timestamp %v", got, modTime)
	}
}
