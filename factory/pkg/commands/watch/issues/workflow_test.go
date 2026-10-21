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

// workflowGitHub fakes the GitHub calls made while an issue naming a workflow
// is evaluated, and records the labels and comments the scanner posts.
type workflowGitHub struct {
	// contents maps a repository path to the file served for it; any other
	// path is a 404.
	contents map[string]string
	// contentsStatus, if set, is returned for every contents request instead.
	contentsStatus int
	// labelsStatus, if set, fails every label request with that status.
	labelsStatus int

	mu       sync.Mutex
	labels   []string
	comments []string
	// contentsRequests counts requests for a file's contents.
	contentsRequests int
}

func (g *workflowGitHub) client(t *testing.T) *github.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/test-owner/test-repo/contents/"):
			g.contentsRequests++
			if g.contentsStatus != 0 {
				w.WriteHeader(g.contentsStatus)
				_, _ = w.Write([]byte(`{"message":"error"}`))
				return
			}
			content, ok := g.contents[strings.TrimPrefix(r.URL.Path, "/repos/test-owner/test-repo/contents/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "file", "content": content})
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_ = json.NewEncoder(w).Encode([]*githubv39.Timeline{})
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			if g.labelsStatus != 0 {
				w.WriteHeader(g.labelsStatus)
				_, _ = w.Write([]byte(`{"message":"error"}`))
				return
			}
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			g.labels = append(g.labels, labels...)
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			g.comments = append(g.comments, c.GetBody())
			_ = json.NewEncoder(w).Encode(c)
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	return github.ForRepo(gh, "test-owner", "test-repo")
}

func workflowIssue(num int, body string) *githubv39.Issue {
	updated := time.Now()
	return &githubv39.Issue{
		Number:    githubv39.Int(num),
		Body:      githubv39.String(body),
		UpdatedAt: &updated,
		Labels:    []*githubv39.Label{{Name: githubv39.String("factory")}},
	}
}

func TestQueueTask_MissingWorkflowStopsTheIssue(t *testing.T) {
	fake := &workflowGitHub{}
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		workflowIssue(12, "Run this: .agents/workflows/does-not-exist.md"),
	}, map[int]bool{})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v for an issue whose workflow does not exist, want nothing", keys(queue.enqueued))
	}
	if len(fake.labels) != 1 || fake.labels[0] != "factory/stop" {
		t.Errorf("added labels %v, want ['factory/stop']", fake.labels)
	}
	if len(fake.comments) != 1 {
		t.Fatalf("posted %d comments, want 1: %v", len(fake.comments), fake.comments)
	}
	if !strings.Contains(fake.comments[0], ".agents/workflows/does-not-exist.md") || !strings.Contains(fake.comments[0], "factory/stop") {
		t.Errorf("comment does not name the missing workflow and the stop label:\n%s", fake.comments[0])
	}
	if len(queue.removed) != 1 || queue.removed[0] != 12 {
		t.Errorf("withdrew pending tasks for %v, want [12]", queue.removed)
	}
}

// The stop label is what keeps the next cycle from reporting the missing
// workflow again, so without it the comment is held back too.
func TestQueueTask_MissingWorkflowNoCommentWhenStopLabelFails(t *testing.T) {
	fake := &workflowGitHub{labelsStatus: http.StatusInternalServerError}
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		workflowIssue(12, "Run this: .agents/workflows/does-not-exist.md"),
	}, map[int]bool{})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v, want nothing", keys(queue.enqueued))
	}
	if len(fake.comments) != 0 {
		t.Errorf("posted %v although the stop label could not be applied, want no comment", fake.comments)
	}
}

// A failure to fetch that is not a 404 says nothing about whether the
// definition exists: the issue is neither stopped nor given a standard fix.
func TestQueueTask_UnresolvedWorkflowIsRetriedLater(t *testing.T) {
	fake := &workflowGitHub{contentsStatus: http.StatusInternalServerError}
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		workflowIssue(12, "Run this: .agents/workflows/flaky.md"),
	}, map[int]bool{})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v for an issue whose workflow could not be fetched, want nothing", keys(queue.enqueued))
	}
	if len(fake.labels) != 0 || len(fake.comments) != 0 {
		t.Errorf("labelled %v and commented %v on a transient failure, want neither", fake.labels, fake.comments)
	}
}

func TestQueueTask_ExistingWorkflowIsQueued(t *testing.T) {
	fake := &workflowGitHub{contents: map[string]string{
		".agents/workflows/greenfield.md": "---\nname: greenfield\nmode: workflow\n---\nprompt\n",
	}}
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		workflowIssue(12, "Run this: .agents/workflows/greenfield.md"),
	}, map[int]bool{})

	task := queue.enqueued[issueTaskFilename(12, "greenfield")]
	if task == nil {
		t.Fatalf("queued %v, want the greenfield workflow for #12", keys(queue.enqueued))
	}
	if task.AgentFile != ".agents/workflows/greenfield.md" {
		t.Errorf("queued agent file %q, want .agents/workflows/greenfield.md", task.AgentFile)
	}
	if len(fake.comments) != 0 {
		t.Errorf("commented %v on an issue whose workflow exists", fake.comments)
	}
}

// A definition that will never load - here a chained redirect - is stopped
// like a missing one rather than retried silently every cycle.
func TestQueueTask_UnusableWorkflowStopsTheIssue(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("---\nname: again\nmode: workflow\nredirect: " + upstream.URL + "/elsewhere.md\n---\nmoved again\n"))
	}))
	defer upstream.Close()

	fake := &workflowGitHub{contents: map[string]string{
		".agents/workflows/chained.md": "---\nname: chained\nmode: workflow\nredirect: " + upstream.URL + "/workflows/again.md\n---\nmoved\n",
	}}
	s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})

	s.queueTasks(context.Background(), []*githubv39.Issue{
		workflowIssue(12, "Run this: .agents/workflows/chained.md"),
	}, map[int]bool{})

	if len(queue.enqueued) != 0 {
		t.Errorf("queued %v for an issue whose workflow cannot be loaded, want nothing", keys(queue.enqueued))
	}
	if len(fake.labels) != 1 || fake.labels[0] != "factory/stop" {
		t.Errorf("added labels %v, want ['factory/stop']", fake.labels)
	}
	if len(fake.comments) != 1 || !strings.Contains(fake.comments[0], "could not load") || !strings.Contains(fake.comments[0], "chained workflow redirects") {
		t.Errorf("want one comment saying the workflow could not be loaded and why, got %v", fake.comments)
	}
}

// The definition is fetched only once the cheap gates have passed: an issue
// whose workflow is already queued, or is cooling down, must cost no fetch
// beyond the cooldown lookup itself.
func TestQueueTask_WorkflowFetchedOnlyAfterGates(t *testing.T) {
	const body = "Run this: .agents/workflows/greenfield.md"
	contents := map[string]string{
		".agents/workflows/greenfield.md": "---\nname: greenfield\nmode: workflow\ncooldown: 2h\n---\nprompt\n",
	}
	filename := issueTaskFilename(12, "greenfield")

	t.Run("already queued", func(t *testing.T) {
		fake := &workflowGitHub{contents: contents}
		s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})
		queue.existing[filename] = true

		s.queueTasks(context.Background(), []*githubv39.Issue{workflowIssue(12, body)}, map[int]bool{})

		if fake.contentsRequests != 0 {
			t.Errorf("fetched the definition %d times for a workflow already queued, want 0", fake.contentsRequests)
		}
	})

	t.Run("cooling down", func(t *testing.T) {
		fake := &workflowGitHub{contents: contents}
		s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t)})
		queue.finish(filename, &api.QueueTask{Status: api.StatusCompleted, CompletedAt: time.Now().Add(-time.Hour)})

		s.queueTasks(context.Background(), []*githubv39.Issue{workflowIssue(12, body)}, map[int]bool{})

		if len(queue.enqueued) != 0 {
			t.Errorf("queued %v inside the workflow's 2h cooldown", keys(queue.enqueued))
		}
		// One fetch reads the cooldown; the existence check must not add another.
		if fake.contentsRequests != 1 {
			t.Errorf("fetched the definition %d times for a workflow cooling down, want 1 (the cooldown lookup)", fake.contentsRequests)
		}
	})
}

// A workflow that cannot be loaded is the workflow-missing rule's: the
// scanner records the fact and the decision, and the rules decide the
// same; one that cannot be fetched is a gate, not compared; with the rule
// disabled, the issue is neither stopped nor stepped.
func TestQueueTask_MissingWorkflowIsARule(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	record := func(fake *workflowGitHub, control *rules.Control) rules.Record {
		t.Helper()
		var got rules.Record
		s, _, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t), Rules: control,
			Recorder: rules.NewSinkRecorder(func(rec rules.Record) { got = rec })})
		s.queueTasks(context.Background(), []*githubv39.Issue{workflowIssue(12, "Run this: .agents/workflows/does-not-exist.md")}, map[int]bool{})
		return got
	}

	missing := &workflowGitHub{}
	rec := record(missing, nil)
	if !rec.Item.WorkflowMissing || strings.Join(rec.Decided, ",") != "workflow-missing run" {
		t.Errorf("record = missing %v, decided %v", rec.Item.WorkflowMissing, rec.Decided)
	}
	var engine []string
	for _, d := range set.Decide(rec.Item, rec.Env) {
		engine = append(engine, d.Rule+" "+d.Outcome)
	}
	if strings.Join(engine, ",") != "workflow-missing run" {
		t.Errorf("the rules decide %v, want workflow-missing run", engine)
	}

	if rec := record(&workflowGitHub{contentsStatus: http.StatusInternalServerError}, nil); rec.Gate == "" || len(rec.Decided) != 0 {
		t.Errorf("unresolved: gate %q, decided %v; want a gate and nothing", rec.Gate, rec.Decided)
	}

	off, err := rules.NewControl([]string{"workflow-missing"}, set)
	if err != nil {
		t.Fatal(err)
	}
	fake := &workflowGitHub{}
	if rec := record(fake, off); len(rec.Decided) != 0 || len(fake.labels) != 0 {
		t.Errorf("disabled: decided %v, labelled %v; want neither", rec.Decided, fake.labels)
	}
}

// One task per issue at a time, whatever the rule: a queued fix holds back
// a workflow its edited body now names, and a queued workflow step holds
// back a fix once the body no longer names it. The record says a task is
// active, and the rules then decide nothing either.
func TestQueueTask_OneTaskPerIssue(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, queued, body string
	}{
		{"a fix queued, the body now names a workflow", issueTaskFilename(12, ""), "Run this: .agents/workflows/greenfield.md"},
		{"a workflow step queued, the body no longer names it", issueTaskFilename(12, "greenfield"), "please fix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &workflowGitHub{contents: map[string]string{
				".agents/workflows/greenfield.md": "---\nname: greenfield\nmode: workflow\n---\nprompt\n",
			}}
			var rec rules.Record
			s, queue, _ := newScanner(t, Config{}, Deps{GitHub: fake.client(t),
				Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r })})
			queue.existing[tc.queued] = true

			s.queueTasks(context.Background(), []*githubv39.Issue{workflowIssue(12, tc.body)}, map[int]bool{})

			if len(queue.enqueued) != 0 {
				t.Errorf("queued %v beside %s, want nothing", keys(queue.enqueued), tc.queued)
			}
			if !rec.Item.TaskActive || len(rec.Decided) != 0 {
				t.Errorf("record: task active %v, decided %v; want active and nothing", rec.Item.TaskActive, rec.Decided)
			}
			if ds := set.Decide(rec.Item, rec.Env); len(ds) != 0 {
				t.Errorf("the rules decide %+v, want nothing while a task is active", ds)
			}
		})
	}
}
