package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A rule factory does not handle by name (a repository's) is carried out
// on an issue: one that takes the issue ahead of fix queues its recipe in
// task-issue-7-rule-triage-2.yaml, stamped with its decision, and no fix;
// one that only writes, and does not take it, writes and leaves the fix.
func TestQueueTasks_RepositoryRules(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file, rule string
		task             string
		fixed            bool
	}{
		{"a run that takes the issue", "triage-2.yaml", "name: triage-2\non: issue\norder: 1\nwhen: {labels: {all: [factory]}}\nrun: {recipe: triage}\n", "task-issue-7-rule-triage-2.yaml", false},
		{"writes that do not take it", "tag.yaml", "name: tag\non: issue\nwhen: {labels: {all: [factory]}}\nrun: {label: [seen]}\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := rules.LoadFiles(map[string][]byte{tc.file: []byte(tc.rule)})
			if err != nil {
				t.Fatal(err)
			}
			set, err := rules.Overlay(base, repo)
			if err != nil {
				t.Fatal(err)
			}
			control, err := rules.NewControl(nil, set)
			if err != nil {
				t.Fatal(err)
			}
			var labelled []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/timeline") {
					_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/labels") {
					var names []string
					_ = json.NewDecoder(r.Body).Decode(&names)
					labelled = append(labelled, names...)
					_, _ = w.Write([]byte("[]"))
					return
				}
				_, _ = w.Write([]byte("{}"))
			}))
			t.Cleanup(server.Close)
			gh := githubv39.NewClient(nil)
			gh.BaseURL, _ = url.Parse(server.URL + "/")
			s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}, TriggerLabel: "factory"}, Deps{
				GitHub:   github.ForRepo(gh, "test-owner", "test-repo"),
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    control,
			})
			updated := time.Now().Add(-time.Hour)
			s.queueTasks(context.Background(), []*githubv39.Issue{{
				Number: githubv39.Int(7), UpdatedAt: &updated,
				Labels: []*githubv39.Label{{Name: githubv39.String("factory")}},
				User:   &githubv39.User{Login: githubv39.String("someone")},
			}}, map[int]bool{})
			if _, got := queue.enqueued["task-issue-7.yaml"]; got != tc.fixed {
				t.Errorf("fix queued %v, want %v", got, tc.fixed)
			}
			if tc.task != "" {
				task, ok := queue.enqueued[tc.task]
				if !ok {
					t.Fatalf("the rule's task was not queued: %v", queue.enqueued)
				}
				if task.Type != "issue-rule" || task.Rule != "triage-2" || task.Recipe != "triage" {
					t.Errorf("task = %+v", task)
				}
			} else if len(labelled) != 1 || labelled[0] != "seen" {
				t.Errorf("labelled %v, want [seen]", labelled)
			}
		})
	}
}
