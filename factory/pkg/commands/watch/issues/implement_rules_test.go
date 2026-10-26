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

// The plan flow's implement rules (built-in), on an issue labelled
// implement, by the shape of its plan (the plan fact, read from its
// comments only because a rule asks): a one-PR plan queues fix-with-plan;
// a plan with a Breakdown gets the graph label, the implement label off
// and a comment, and no task; no plan (or only an outsider's), the
// implement label off and a comment, and no task.
func TestQueueTasks_ImplementByPlanShape(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}

	const marker = "\n<!-- factory:comment key=plan v=2 -->"
	onePR := "**Implementation plan** · v2\n\n## Steps\n1. Do it." + marker
	breakdown := "**Implementation plan** · v2\n\n## Breakdown\n```yaml graph\nsteps:\n  - {id: a, title: A, body: a}\n```" + marker
	answered := "**Implementation plan** · v1\n\n## Summary\nWhat I understood.\n\n## Questions\n\n**Keep task folders how long?**\n- [x] 180 days\n- [ ] Forever\n" + marker
	asking := "**Implementation plan** · v1\n\n## Summary\nWhat I understood.\n\n## Questions\n\n**Keep task folders how long?**\n- [ ] 180 days\n- [ ] Forever\n" + marker
	type comment struct{ login, assoc, body string }
	for _, tc := range []struct {
		name      string
		comments  []comment
		task      string // the task queued, "" for none
		labelled  []string
		unlabeled bool
		commented string
	}{
		{"a one-PR plan", []comment{{"bot1", "NONE", onePR}}, "task-issue-7-rule-implement.yaml", nil, false, ""},
		{"a plan with a Breakdown", []comment{{"bot1", "NONE", breakdown}}, "", []string{"factory/graph"}, true, "as an issue graph"},
		{"no plan", nil, "", nil, true, "no plan to implement"},
		// planned and still asking: implement-not-ready takes the label
		// off, and plan-asking labels it needs-human.
		{"a plan still asking", []comment{{"bot1", "NONE", asking}}, "", []string{"factory/needs-human"}, true, "still asking questions"},
		// planned and every question ticked: plan-answered asks for the
		// plan again (and implement-not-ready still refuses implement).
		{"a plan answered, not yet rewritten", []comment{{"bot1", "NONE", answered}}, "task-issue-7-rule-plan.yaml", []string{"factory/plan"}, true, "still asking questions"},
		{"only an outsider's plan", []comment{{"mallory", "NONE", breakdown}}, "", nil, true, "no plan to implement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var labelled, posted []string
			unlabeled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/timeline"):
					_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
					var out []*githubv39.IssueComment
					for i, c := range tc.comments {
						at := time.Now().Add(time.Duration(i-10) * time.Minute)
						out = append(out, &githubv39.IssueComment{ID: githubv39.Int64(int64(i + 1)), Body: githubv39.String(c.body),
							User: &githubv39.User{Login: githubv39.String(c.login)}, AuthorAssociation: githubv39.String(c.assoc), CreatedAt: &at})
					}
					_ = json.NewEncoder(w).Encode(out)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
					var c githubv39.IssueComment
					_ = json.NewDecoder(r.Body).Decode(&c)
					posted = append(posted, c.GetBody())
					_, _ = w.Write([]byte("{}"))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/labels"):
					var names []string
					_ = json.NewDecoder(r.Body).Decode(&names)
					labelled = append(labelled, names...)
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/issues/7/labels/factory"):
					unlabeled = true
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
			updated := time.Now().Add(-time.Hour)
			s.queueTasks(context.Background(), []*githubv39.Issue{{
				Number: githubv39.Int(7), UpdatedAt: &updated,
				Labels:            []*githubv39.Label{{Name: githubv39.String("factory/implement")}, {Name: githubv39.String("factory/planned")}},
				User:              &githubv39.User{Login: githubv39.String("alice")},
				AuthorAssociation: githubv39.String("MEMBER"),
			}}, map[int]bool{})

			mu.Lock()
			defer mu.Unlock()
			var queued []string
			for name := range queue.enqueued {
				queued = append(queued, name)
			}
			if tc.task == "" && len(queued) > 0 || tc.task != "" && !slices.Equal(queued, []string{tc.task}) {
				t.Fatalf("queued %v, want %q", queued, tc.task)
			}
			if tc.task != "" {
				want := map[string]string{"task-issue-7-rule-implement.yaml": "fix-with-plan", "task-issue-7-rule-plan.yaml": "plan"}[tc.task]
				if task := queue.enqueued[tc.task]; task.Recipe != want {
					t.Errorf("task = %+v", task)
				}
			}
			if !slices.Equal(labelled, tc.labelled) || unlabeled != tc.unlabeled {
				t.Errorf("labelled %v, unlabeled %v; want %v, %v", labelled, unlabeled, tc.labelled, tc.unlabeled)
			}
			if tc.commented == "" && len(posted) > 0 || tc.commented != "" && (len(posted) != 1 || !strings.Contains(posted[0], tc.commented)) {
				t.Errorf("posted %q, want one with %q", posted, tc.commented)
			}
		})
	}
}
