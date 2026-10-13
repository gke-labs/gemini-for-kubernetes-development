package sandbox

import (
	"context"
	"errors"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// fakeTaskServer is a sandbox's task server: its tasks, newest first, and
// their files.
type fakeTaskServer struct {
	entries []spool.Entry
	files   map[string]string // "<id>/<name>"
	closed  bool
}

func (f *fakeTaskServer) List(context.Context) ([]spool.Entry, error) { return f.entries, nil }
func (f *fakeTaskServer) ReadFile(_ context.Context, id, name string) ([]byte, error) {
	if data, ok := f.files[id+"/"+name]; ok {
		return []byte(data), nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeTaskServer) Close() { f.closed = true }

func withTasks(t *testing.T, sb *fakeTaskServer, err error) {
	t.Helper()
	prev := connectTasks
	connectTasks = func(context.Context, string, string) (taskServer, error) {
		if err != nil {
			return nil, err
		}
		return sb, nil
	}
	t.Cleanup(func() { connectTasks = prev })
}

func taskState(t *testing.T, svc *Service, name string) string {
	t.Helper()
	got, err := svc.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got.GetAnnotations()[annotationLastTaskState]
}

// A sandbox marked Running is asked, through its task server, whether its
// latest task still runs; one that has ended corrects the mark.
func TestIsTaskRunningAsksTheTaskServer(t *testing.T) {
	for name, tc := range map[string]struct {
		entries    []spool.Entry
		connectErr error
		running    bool
		state      string
	}{
		"still running":        {entries: []spool.Entry{{Task: spool.Task{ID: "recipe-fix-2"}, State: spool.Running}}, running: true, state: "Running"},
		"claimed, not started": {entries: []spool.Entry{{Task: spool.Task{ID: "recipe-fix-2"}, State: spool.Claimed}}, running: true, state: "Running"},
		"ended well":           {entries: []spool.Entry{{Task: spool.Task{ID: "recipe-fix-2"}, State: spool.Exited, ExitCode: "0"}, {Task: spool.Task{ID: "old"}, State: spool.Exited, ExitCode: "1"}}, running: false, state: "Completed"},
		"process gone (137)":   {entries: []spool.Entry{{Task: spool.Task{ID: "recipe-fix-2"}, State: spool.Exited, ExitCode: "137"}}, running: false, state: "Failed"},
		"no tasks":             {running: false, state: "Running"},
		"pod terminated":       {connectErr: errors.New("cannot connect to terminated pod fix-x"), running: false, state: "Failed"},
		"unreachable":          {connectErr: errors.New("dial: i/o timeout"), running: true, state: "Running"},
	} {
		t.Run(name, func(t *testing.T) {
			ts := &fakeTaskServer{entries: tc.entries}
			withTasks(t, ts, tc.connectErr)
			svc := NewService(ServiceConfig{Namespace: testNamespace}, ServiceDeps{Kube: newFakeKubeClient(t, newSandbox("fix-x", map[string]interface{}{
				annotationLastTaskState: "Running", annotationLastTaskType: "fix",
			}))})
			running, err := svc.IsTaskRunning(context.Background(), "fix-x")
			if err != nil || running != tc.running {
				t.Errorf("IsTaskRunning = %v, %v; want %v", running, err, tc.running)
			}
			if got := taskState(t, svc, "fix-x"); got != tc.state {
				t.Errorf("last-task-state = %q, want %q", got, tc.state)
			}
			if tc.connectErr == nil && !ts.closed {
				t.Error("the task server connection was left open")
			}
		})
	}
}
