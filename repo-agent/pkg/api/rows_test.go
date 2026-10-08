package api

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// Finished work is done for a month, at the top of the feed: a run
// done, a review submitted, a fix's PR open. What still needs the member,
// or an agent, comes first; older, it rests.
func TestAttentionDone(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	run := func(status string, ended time.Duration) models.RunSession {
		return models.RunSession{Recipe: "review", Status: status, EndedAt: ago(ended)}
	}
	old := ago(60 * 24 * time.Hour)
	for _, c := range []struct {
		name string
		item models.WorkItem
		want string
	}{
		{"a run done today", models.WorkItem{Type: "pr", UpdatedAt: old, Sessions: []models.RunSession{run("done", time.Hour)}}, attentionDone},
		{"a run done a week ago", models.WorkItem{Type: "pr", UpdatedAt: old, Sessions: []models.RunSession{run("done", 7*24*time.Hour)}}, attentionDone},
		{"a run done two months ago", models.WorkItem{Type: "pr", UpdatedAt: old, Sessions: []models.RunSession{run("done", 60*24*time.Hour)}}, ""},
		{"an old run applied today", models.WorkItem{Type: "pr", UpdatedAt: old, Sessions: []models.RunSession{{Recipe: "review", Status: "done", EndedAt: old, Applied: map[string]string{"submit": ago(time.Hour)}}}}, attentionDone},
		{"a done run beside a ready one", models.WorkItem{Type: "issue", UpdatedAt: old, Sessions: []models.RunSession{run("done", time.Hour), {Recipe: "plan", Status: "ready"}}}, attentionNeedsYou},
		{"a review submitted today", models.WorkItem{Type: "pr", Reviewed: true, UpdatedAt: ago(time.Hour)}, attentionDone},
		{"a review submitted long ago", models.WorkItem{Type: "pr", Reviewed: true, UpdatedAt: old}, ""},
		{"my PR, its run done", models.WorkItem{Type: "pr", Mine: true, UpdatedAt: old, Sessions: []models.RunSession{run("done", time.Hour)}}, attentionDone},
		{"my PR, nothing done", models.WorkItem{Type: "pr", Mine: true, UpdatedAt: old}, attentionWaiting},
		{"a fix's PR open", models.WorkItem{Type: "issue", PRURL: "https://github.com/o/r/pull/2", UpdatedAt: ago(time.Hour)}, attentionDone},
		{"a click queued after a done run", models.WorkItem{Type: "pr", UpdatedAt: old, Sessions: []models.RunSession{run("done", time.Hour)}, Launching: map[string]string{"care": "queued"}}, attentionWaiting},
	} {
		if got := attentionOf(&c.item, now); got != c.want {
			t.Errorf("%s: attention = %q, want %q", c.name, got, c.want)
		}
	}
}
