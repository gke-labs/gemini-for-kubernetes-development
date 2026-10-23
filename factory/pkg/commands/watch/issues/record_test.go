package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// With a recorder, the scanner records each issue it decides about: the
// facts, and what it did, in the rules' names; the rules, replayed on the
// records, decide the same. A gate outside the rules (a busy sandbox) is
// recorded as such.
func TestQueueTasks_RecordsDecisionsTheRulesReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/timeline") {
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}}, Deps{
		GitHub:    github.ForRepo(gh, "test-owner", "test-repo"),
		Recorder:  rules.NewRecorder(path, 0, nil),
		Sandboxes: fakeSandboxes{running: map[string]bool{"fix-test-repo-9": true}},
	})
	labelled := func(num int, updated time.Time) *githubv39.Issue {
		return &githubv39.Issue{
			Number: githubv39.Int(num), UpdatedAt: &updated,
			Labels: []*githubv39.Label{{Name: githubv39.String("factory")}},
			User:   &githubv39.User{Login: githubv39.String("someone")},
		}
	}
	// 8 finished a minute ago: in its cooldown.
	queue.finish("task-issue-8-rule-fix.yaml", &api.QueueTask{
		Type: api.TypeIssueRule, Number: 8, Status: api.StatusCompleted,
		StartedAt: time.Now().Add(-time.Hour), CompletedAt: time.Now().Add(-time.Minute),
	})
	s.queueTasks(context.Background(), []*githubv39.Issue{
		labelled(7, time.Now().Add(-3*time.Hour)),
		labelled(8, time.Now()),
		labelled(9, time.Now()),
		labelled(10, time.Now()),
	}, map[int]bool{10: true})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byNumber := map[int]rules.Record{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec rules.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		byNumber[rec.Item.Number] = rec
	}
	if r := byNumber[7]; strings.Join(r.Decided, ",") != "fix run" || r.Gate != "" || r.Scanner != "issues" {
		t.Errorf("7 = %+v, want fix run", r)
	}
	if r := byNumber[8]; len(r.Decided) != 0 || len(r.Item.Runs) != 1 || r.Item.Runs[0].Rule != "fix" {
		t.Errorf("8 = %+v, want nothing decided and its last fix run", r)
	}
	if r := byNumber[9]; r.Gate != "its sandbox is busy" {
		t.Errorf("9 = %+v, want gated by its busy sandbox", r)
	}
	if r := byNumber[10]; r.Item.LinkedPR != "open" || len(r.Decided) != 0 {
		t.Errorf("10 = %+v, want an open linked PR and nothing decided", r)
	}
	if _, ok := queue.enqueued["task-issue-7-rule-fix.yaml"]; !ok {
		t.Errorf("recording changed what was queued: %v", queue.enqueued)
	}

	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := set.Replay(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Compared != 3 || res.Gated != 1 || len(res.Diffs) != 0 {
		t.Errorf("replay = compared %d, gated %d, diffs %+v", res.Compared, res.Gated, res.Diffs)
	}
}
