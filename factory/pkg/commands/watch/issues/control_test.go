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

// A labelled issue is fixed, unless fix is disabled, or the engine does
// not decide it.
func TestQueueTasks_RulesControl(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var fix rules.Rule
	for _, r := range base {
		if r.Name == "fix" {
			fix = *r
		}
	}
	fix.When.Labels = &rules.Labels{All: []string{"never"}}
	neverFix, err := rules.Overlay(base, []*rules.Rule{&fix})
	if err != nil {
		t.Fatal(err)
	}
	control := func(disabled []string, set rules.Set) *rules.Control {
		c, err := rules.NewControl(disabled, set)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, tc := range []struct {
		name    string
		control *rules.Control
		fixed   bool
	}{
		{"no control", nil, true},
		{"fix disabled", control([]string{"fix"}, base), false},
		{"the engine does not decide fix", control(nil, neverFix), false},
		{"the engine decides fix", control(nil, base), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}}, Deps{
				GitHub:   github.ForRepo(gh, "test-owner", "test-repo"),
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    tc.control,
			})
			updated := time.Now().Add(-time.Hour)
			s.queueTasks(context.Background(), []*githubv39.Issue{{
				Number: githubv39.Int(7), UpdatedAt: &updated,
				Labels: []*githubv39.Label{{Name: githubv39.String("factory")}},
				User:   &githubv39.User{Login: githubv39.String("someone")},
			}}, map[int]bool{})
			if _, got := queue.enqueued["task-issue-7-rule-fix.yaml"]; got != tc.fixed {
				t.Errorf("fix queued %v, want %v", got, tc.fixed)
			}
		})
	}
}
