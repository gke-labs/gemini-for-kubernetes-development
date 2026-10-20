package conventions

import (
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestHasIgnorePrefix(t *testing.T) {
	tests := []struct {
		body         string
		triggerLabel string
		expected     bool
	}{
		{
			body:         "/overseer-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /OVERSEER-IGNORE: some message  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/factory-ignore",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "  /FACTORY-IGNORE: custom prefix  ",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "/other-ignore",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "overseer",
			expected:     true,
		},
		{
			body:         "/overseer-ignore",
			triggerLabel: "",
			expected:     true,
		},
		{
			body:         "just a regular comment",
			triggerLabel: "factory",
			expected:     false,
		},
		{
			body:         "line 1\n/overseer-ignore\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  /FACTORY-IGNORE: some message\nline 3",
			triggerLabel: "factory",
			expected:     true,
		},
		{
			body:         "line 1\n  some comment containing /overseer-ignore but not at start",
			triggerLabel: "factory",
			expected:     false,
		},
	}

	for _, tc := range tests {
		got := HasIgnorePrefix(tc.body, tc.triggerLabel)
		if got != tc.expected {
			t.Errorf("HasIgnorePrefix(%q, %q) = %v; expected %v", tc.body, tc.triggerLabel, got, tc.expected)
		}
	}
}

// The rules' human and pause are apart: a trusted human's comment is the
// one, the watcher's comment attaching the stop label the other, whatever
// the give-up says.
func TestHumanSpokeAtAndPausedAt(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	member := "MEMBER"
	comments := []*githubv39.IssueComment{
		{User: &githubv39.User{Login: githubv39.String("alice")}, AuthorAssociation: &member, Body: githubv39.String("try the other flag"), CreatedAt: at(3 * time.Hour)},
		{User: &githubv39.User{Login: githubv39.String("watcher")}, Body: githubv39.String("Pausing after 3 attempts to fix the failing checks. I have applied the `overseer/stop` label."), CreatedAt: at(2 * time.Hour)},
		{User: &githubv39.User{Login: githubv39.String("pool-bot")}, Body: githubv39.String("🤖 AI Factory started investigating CI check failures"), CreatedAt: at(time.Hour)},
		{User: &githubv39.User{Login: githubv39.String("mallory")}, Body: githubv39.String("adding `overseer/stop` myself"), CreatedAt: at(30 * time.Minute)},
	}
	if got := HumanSpokeAt(comments, []string{"pool-bot"}, "watcher", nil, nil, "overseer"); !got.Equal(*at(3 * time.Hour)) {
		t.Errorf("humanSpokeAt = %v, want alice's comment", got)
	}
	if got := PausedAt(comments, []string{"pool-bot"}, "watcher", "overseer"); !got.Equal(*at(2 * time.Hour)) {
		t.Errorf("pausedAt = %v, want the watcher's hold", got)
	}
	if got := PausedAt(comments[:1], []string{"pool-bot"}, "watcher", "overseer"); !got.IsZero() {
		t.Errorf("pausedAt with no hold = %v, want zero", got)
	}
}
