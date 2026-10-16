package prs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
)

// The CI give-up's attempts: without the setting, the watcher's "started
// investigating" comments, as always; with it, the higher of those and the
// investigation runs that ended in the journal, both since the patience
// last reset (the last commit, a trusted human comment, a pause notice). A
// task dropped before it started is not an attempt.
func TestInvestigationAttempts(t *testing.T) {
	now := time.Now()
	queueDir := t.TempDir()
	runs := func(started ...time.Duration) {
		t.Helper()
		var lines []string
		for _, ago := range started {
			b, _ := json.Marshal(api.JournalEvent{TaskID: "task-pr-7-investigate", Event: "Completed", StartedAt: now.Add(-ago), Timestamp: now.Add(-ago + time.Minute)})
			lines = append(lines, string(b))
		}
		b, _ := json.Marshal(api.JournalEvent{TaskID: "task-pr-7-investigate", Event: "Dropped", Timestamp: now.Add(-time.Minute)})
		lines = append(lines, string(b))
		if err := os.WriteFile(filepath.Join(queueDir, "journal.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	comment := func(login, body string, ago time.Duration) *githubv39.IssueComment {
		at := now.Add(-ago)
		return &githubv39.IssueComment{User: &githubv39.User{Login: stringPtr(login)}, Body: stringPtr(body), CreatedAt: &at, AuthorAssociation: stringPtr("MEMBER")}
	}
	started := func(ago time.Duration) *githubv39.IssueComment {
		return comment("pool-bot", "🤖 AI Factory started investigating CI check failures", ago)
	}
	scanner := func(attempts bool) *Scanner {
		q := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{QueueDir: queueDir})
		return &Scanner{
			cfg:   Config{BotUsers: []string{"pool-bot"}, GitHubLogin: "watcher", TriggerLabel: "overseer", Attempts: attempts},
			queue: q,
		}
	}
	lastCommit := now.Add(-10 * time.Hour)

	// Four runs ended since the last commit, one comment posted (the others
	// failed to post): the comments say 1, the journal 4.
	runs(5*time.Hour, 4*time.Hour, 3*time.Hour, 2*time.Hour)
	comments := []*githubv39.IssueComment{started(2 * time.Hour)}
	if got := scanner(false).investigationAttempts(7, comments, lastCommit); got != 1 {
		t.Errorf("without the setting = %d, want 1 (the comments)", got)
	}
	if got := scanner(true).investigationAttempts(7, comments, lastCommit); got != 4 {
		t.Errorf("with the setting = %d, want 4 (the journal's runs; the drop is not one)", got)
	}

	// A trusted human comment 3.5h ago resets the count: only the runs after it.
	comments = []*githubv39.IssueComment{started(2 * time.Hour), comment("maintainer", "try bumping the timeout", 210*time.Minute)}
	if got := scanner(true).investigationAttempts(7, comments, lastCommit); got != 2 {
		t.Errorf("after a human comment = %d, want 2", got)
	}

	// A pause notice resets it too.
	comments = []*githubv39.IssueComment{comment("watcher", "... pausing automated investigation ...", 150*time.Minute)}
	if got := scanner(true).investigationAttempts(7, comments, lastCommit); got != 1 {
		t.Errorf("after a pause notice = %d, want 1", got)
	}

	// So does a commit.
	if got := scanner(true).investigationAttempts(7, nil, now.Add(-150*time.Minute)); got != 1 {
		t.Errorf("after a commit = %d, want 1", got)
	}

	// Mid-count today: more comments than runs in the journal (runs before
	// the journal was read for this) keep the higher count.
	runs(2 * time.Hour)
	comments = []*githubv39.IssueComment{started(5 * time.Hour), started(4 * time.Hour), started(3 * time.Hour)}
	if got := scanner(true).investigationAttempts(7, comments, lastCommit); got != 3 {
		t.Errorf("seeded from the comments = %d, want 3 (the higher count)", got)
	}
}
