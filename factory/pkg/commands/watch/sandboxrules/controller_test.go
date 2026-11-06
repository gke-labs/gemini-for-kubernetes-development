package sandboxrules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/diskcare"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

type fakeQueue struct {
	queued    map[string]*api.QueueTask
	processed map[string]*api.QueueTask
}

func (q *fakeQueue) TaskExists(f string) bool { _, ok := q.queued[f]; return ok }
func (q *fakeQueue) Enqueue(f string, t *api.QueueTask) error {
	q.queued[f] = t
	return nil
}
func (q *fakeQueue) GetProcessedTask(f string) *api.QueueTask { return q.processed[f] }
func (q *fakeQueue) EndedRuns(string) []api.EndedRun          { return nil }

const gb = 1 << 30

// An awake sandbox whose disk is low and in which no task runs gets the
// built-in workspace-cleanup rule's task: the recipe, in that sandbox, as
// its account; its disk is recorded on it either way. A busy sandbox, one
// with room, or one whose task is queued already gets none.
func TestSyncQueuesTheCleanupOfALowSandbox(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	setup := func(free uint64, busy bool) (*Controller, *fakeQueue, map[string]string) {
		q := &fakeQueue{queued: map[string]*api.QueueTask{}, processed: map[string]*api.QueueTask{}}
		annotated := map[string]string{}
		c := New(time.Hour, Deps{
			Awake: func(context.Context) ([]Sandbox, error) {
				return []Sandbox{{Name: "fix-repo-1", RepoURL: "https://github.com/o/repo", User: "ada-coder-bot"}}, nil
			},
			Busy: func(context.Context, string) bool { return busy },
			Disk: func(context.Context, string) (diskcare.Usage, error) {
				return diskcare.Usage{Used: 60*gb - free, Size: 60 * gb, Free: free}, nil
			},
			Annotate: func(_ context.Context, name string, a map[string]string) error {
				annotated[name] = a[DiskAnnotation]
				return nil
			},
			Control: func() *rules.Control { return control },
			Env:     func() rules.Env { return rules.Env{Trigger: "overseer", Now: time.Now()} },
			Queue:   q,
		})
		return c, q, annotated
	}
	file := api.SandboxRuleTaskFile("fix-repo-1", "workspace-cleanup")

	c, q, annotated := setup(5*gb, false)
	c.SyncOnce(context.Background())
	task := q.queued[file]
	if task == nil {
		t.Fatalf("low and idle: nothing queued (%v)", q.queued)
	}
	if task.Type != api.TypeSandboxRule || task.Sandbox != "fix-repo-1" || task.Recipe != "workspace-cleanup" ||
		task.Inputs["target_bytes"] != "21474836480" || task.Assignee != "ada-coder-bot" || task.URL != "https://github.com/o/repo" {
		t.Errorf("task = %+v", task)
	}
	if !strings.Contains(annotated["fix-repo-1"], `"free":5368709120`) {
		t.Errorf("disk not recorded: %q", annotated)
	}
	// Queued already: not again.
	c.SyncOnce(context.Background())
	if len(q.queued) != 1 {
		t.Errorf("queued twice: %v", q.queued)
	}

	for _, tc := range []struct {
		name string
		free uint64
		busy bool
	}{{"room enough", 30 * gb, false}, {"a task running", 5 * gb, true}} {
		c, q, annotated := setup(tc.free, tc.busy)
		c.SyncOnce(context.Background())
		if len(q.queued) != 0 || annotated["fix-repo-1"] == "" {
			t.Errorf("%s: queued %v, recorded %q", tc.name, q.queued, annotated["fix-repo-1"])
		}
	}
}

// Wake checks a sandbox at once, never blocking: with the buffer full the
// next sweep checks it.
func TestWakeNeverBlocks(t *testing.T) {
	c := New(time.Hour, Deps{})
	for i := 0; i < 100; i++ {
		c.Wake("fix-repo-1")
	}
	var nilC *Controller
	nilC.Wake("x") // no controller: nothing
}
