package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A fix the engine decides, that failed 3 times since a trusted human last
// spoke on the issue, gives up: the stop label, the rule's comment naming
// it, the issue's queued tasks dropped and nothing queued. A human's
// comment after the failures starts the count again: the fix is queued.
func TestQueueTask_EngineGivesUpOnAFailingFix(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl("on", map[string]string{"fix": "engine"}, "", nil, set)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runAt := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }
	spoke := runAt(-2).Add(-30 * time.Minute)
	for _, tc := range []struct {
		name     string
		comments []*githubv39.IssueComment
		giveUp   bool
	}{
		{"no human since the failures", nil, true},
		{"a human spoke after two of them", []*githubv39.IssueComment{{
			User: &githubv39.User{Login: githubv39.String("alice")}, AuthorAssociation: githubv39.String("MEMBER"),
			Body: githubv39.String("try the other flag"), CreatedAt: &spoke,
		}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var labelled, commented []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/timeline"):
					_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
				case r.URL.Path == "/repos/test-owner/test-repo/issues/71/comments" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(tc.comments)
				case r.URL.Path == "/repos/test-owner/test-repo/issues/71/comments" && r.Method == http.MethodPost:
					var c githubv39.IssueComment
					_ = json.NewDecoder(r.Body).Decode(&c)
					mu.Lock()
					commented = append(commented, c.GetBody())
					mu.Unlock()
					_, _ = w.Write([]byte("{}"))
				case r.URL.Path == "/repos/test-owner/test-repo/issues/71/labels" && r.Method == http.MethodPost:
					var ls []string
					_ = json.NewDecoder(r.Body).Decode(&ls)
					mu.Lock()
					labelled = append(labelled, ls...)
					mu.Unlock()
					_, _ = w.Write([]byte("[]"))
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()
			gh := githubv39.NewClient(nil)
			gh.BaseURL, _ = url.Parse(server.URL + "/")
			var rec rules.Record
			s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}}, Deps{
				GitHub: github.ForRepo(gh, "test-owner", "test-repo"), Rules: control,
				Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r }),
			})
			// Three failed fixes, the last ended an hour ago (past the
			// failure cooldown).
			queue.ended = map[string][]api.EndedRun{"task-issue-71.yaml": {
				{StartedAt: runAt(-4), Failed: true}, {StartedAt: runAt(-3), Failed: true}, {StartedAt: runAt(-2), Failed: true},
			}}
			queue.processed["task-issue-71.yaml"] = &api.QueueTask{Status: api.StatusFailed, StartedAt: runAt(-2), CompletedAt: runAt(-1)}
			updated := runAt(-5)
			s.queueTasks(context.Background(), []*githubv39.Issue{{
				Number: githubv39.Int(71), UpdatedAt: &updated,
				Labels: []*githubv39.Label{{Name: githubv39.String("factory")}},
				User:   &githubv39.User{Login: githubv39.String("someone")},
			}}, map[int]bool{})

			_, queued := queue.enqueued["task-issue-71.yaml"]
			if tc.giveUp {
				if queued {
					t.Error("queued a fix the engine gave up on")
				}
				if len(labelled) != 1 || labelled[0] != "factory/stop" {
					t.Errorf("labels added = %v, want factory/stop", labelled)
				}
				if len(commented) != 1 || !strings.Contains(commented[0], "3 failed attempts") || !strings.Contains(commented[0], "`factory/stop`") {
					t.Errorf("comments = %q, want the rule's, naming the stop label", commented)
				}
				if len(queue.removed) != 1 || queue.removed[0] != 71 {
					t.Errorf("queued tasks dropped for %v, want 71", queue.removed)
				}
				if len(rec.Decided) != 1 || rec.Decided[0] != "fix give-up" {
					t.Errorf("decided %v, want fix give-up", rec.Decided)
				}
				return
			}
			if !queued || len(labelled) != 0 || len(commented) != 0 {
				t.Errorf("queued %v, labels %v, comments %q; want the fix queued and nothing written", queued, labelled, commented)
			}
		})
	}
}
