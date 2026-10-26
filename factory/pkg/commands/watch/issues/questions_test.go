package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// An issue whose description still asks (a question in its ## Questions
// with no box ticked) is not fixed: issue-asking labels it needs-human.
// Once every question is answered, fix queues it and issue-answered takes
// needs-human off, in the same pass.
func TestQueueTasks_DeferredQuestions(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	body := func(tick string) string {
		return "Add the archive reader.\n\n## Questions\n\n**Read through the collector or the bucket?**\n- [" + tick + "] The collector\n- [ ] The bucket\n"
	}
	for _, tc := range []struct {
		name      string
		body      string
		labels    []string
		fixed     bool
		labelled  []string
		unlabeled bool
	}{
		{"still asking", body(" "), []string{"factory"}, false, []string{"factory/needs-human"}, false},
		{"answered", body("x"), []string{"factory", "factory/needs-human"}, true, nil, true},
		{"no questions", "Add the archive reader.", []string{"factory"}, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var labelled []string
			unlabeled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/timeline"):
					_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/labels"):
					var names []string
					_ = json.NewDecoder(r.Body).Decode(&names)
					labelled = append(labelled, names...)
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/issues/7/labels/factory"):
					unlabeled = true
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
					_, _ = w.Write([]byte("[]"))
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			t.Cleanup(server.Close)
			gh := githubv39.NewClient(nil)
			gh.BaseURL, _ = url.Parse(server.URL + "/")
			s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}, TriggerLabel: "factory"}, Deps{
				GitHub:   github.ForRepo(gh, "test-owner", "test-repo"),
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    control,
			})
			var labels []*githubv39.Label
			for _, l := range tc.labels {
				labels = append(labels, &githubv39.Label{Name: githubv39.String(l)})
			}
			updated := time.Now().Add(-time.Hour)
			s.queueTasks(context.Background(), []*githubv39.Issue{{
				Number: githubv39.Int(7), UpdatedAt: &updated, Body: githubv39.String(tc.body), Labels: labels,
				User: &githubv39.User{Login: githubv39.String("alice")}, AuthorAssociation: githubv39.String("MEMBER"),
			}}, map[int]bool{})
			mu.Lock()
			defer mu.Unlock()
			if _, fixed := queue.enqueued["task-issue-7-rule-fix.yaml"]; fixed != tc.fixed {
				t.Errorf("fix queued %v, want %v: %v", fixed, tc.fixed, queue.enqueued)
			}
			if !slices.Equal(labelled, tc.labelled) || unlabeled != tc.unlabeled {
				t.Errorf("labelled %v, unlabeled %v; want %v, %v", labelled, unlabeled, tc.labelled, tc.unlabeled)
			}
		})
	}
}
