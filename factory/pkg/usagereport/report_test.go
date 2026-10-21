/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package usagereport

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

func TestPublishAndMarkSummarized(t *testing.T) {
	var received UsageRecord
	markCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/usage":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mark-summarized"):
			markCalls++
			_ = json.NewEncoder(w).Encode(map[string]bool{"alreadyPosted": markCalls > 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("COLLECTOR_URL", ts.URL)

	if !Enabled() {
		t.Fatal("expected Enabled() with COLLECTOR_URL set")
	}

	rec := UsageRecord{
		Key: "sb:/workspaces/tasks/fix-1", Repo: "org/repo", Issue: 5,
		Stats: Stats{Models: map[string]ModelUsage{"m": {Tokens: TokenUsage{Total: 10}}}},
	}
	if err := Publish(context.Background(), rec); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if received.Key != rec.Key || received.Issue != 5 {
		t.Errorf("collector received %+v", received)
	}

	already, err := markSummarized(context.Background(), "issue-5")
	if err != nil || already {
		t.Fatalf("first markSummarized: (%v, %v)", already, err)
	}
	already, err = markSummarized(context.Background(), "issue-5")
	if err != nil || !already {
		t.Fatalf("second markSummarized: (%v, %v)", already, err)
	}
}

func TestPublishDisabled(t *testing.T) {
	t.Setenv("COLLECTOR_URL", "")
	if Enabled() {
		t.Fatal("expected disabled without COLLECTOR_URL")
	}
	// No-op, no error, no network.
	if err := Publish(context.Background(), UsageRecord{Key: "x"}); err != nil {
		t.Fatalf("Publish while disabled: %v", err)
	}
}

func TestTaskTypeFromDir(t *testing.T) {
	cases := map[string]string{
		"/workspaces/tasks/fix-20260713-101500":         "fix",
		"/workspaces/tasks/agent-chore-20260713-101500": "agent",
		"/workspaces/tasks/investigate-20260713-101500": "investigate",
		"/workspaces/tasks/weird":                       "weird",
	}
	for dir, want := range cases {
		if got := taskTypeFromDir(dir); got != want {
			t.Errorf("taskTypeFromDir(%s) = %s, want %s", dir, got, want)
		}
	}
}

func TestFormatWorkflowSummary(t *testing.T) {
	r := &Rollup{
		Key: "issue-42", WorkflowName: "greenfield", TaskCount: 3, PRs: []int{101, 102},
		Stats: Stats{Models: map[string]ModelUsage{
			"gemini-2.5-pro": {
				API:    APIUsage{TotalRequests: 4, TotalErrors: 1},
				Tokens: TokenUsage{Input: 100, Output: 50, Cached: 20, Thoughts: 10, Total: 180},
			},
			"gemini-2.5-flash": {
				API:    APIUsage{TotalRequests: 2},
				Tokens: TokenUsage{Input: 10, Output: 5, Total: 15},
			},
		}},
	}
	body := FormatWorkflowSummary(r)
	for _, want := range []string{
		"Gemini token usage summary",
		"**greenfield**",
		"Tasks: 3",
		"#101, #102",
		"| gemini-2.5-pro | 4 | 1 | 100 | 50 | 20 | 10 | 180 |",
		"| gemini-2.5-flash | 2 | 0 | 10 | 5 | 0 | 0 | 15 |",
		"| **Total** | 6 | 1 | 110 | 55 | 20 | 10 | 195 |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary missing %q in:\n%s", want, body)
		}
	}
}

// fakeTasks is a sandbox's task server: its task dirs and their files.
type fakeTasks struct {
	entries []spool.Entry
	files   map[string]string // "<id>/<name>"
	closed  bool
}

func (f *fakeTasks) List(context.Context) ([]spool.Entry, error) { return f.entries, nil }
func (f *fakeTasks) ReadFile(_ context.Context, id, name string) ([]byte, error) {
	data, ok := f.files[id+"/"+name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(data), nil
}
func (f *fakeTasks) Close() { f.closed = true }
func (f *fakeTasks) AllFiles(_ context.Context, id string) ([]string, error) {
	var names []string
	for key := range f.files {
		if name, ok := strings.CutPrefix(key, id+"/"); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// The sweep before a delete harvests every task dir through the sandbox's
// task server, a classic command's and a recipe's alike, keyed as before
// (<sandbox>:/workspaces/tasks/<id>).
func TestHarvestSandboxThroughTheTaskServer(t *testing.T) {
	var keys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/usage" {
			var rec UsageRecord
			_ = json.NewDecoder(r.Body).Decode(&rec)
			keys = append(keys, rec.Key+" "+rec.TaskType)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	t.Setenv("COLLECTOR_URL", ts.URL)

	usage := `{"models":{"gemini-3-pro":{"api":{"totalRequests":1},"tokens":{"input":10,"output":2,"total":12}}}}`
	fake := &fakeTasks{
		entries: []spool.Entry{
			{Task: spool.Task{ID: "recipe-care-ci-20261012-100000-ab12"}},
			{Task: spool.Task{ID: "agent-deps-20261012-090000"}},
			{Task: spool.Task{ID: "fix-20261011-080000"}}, // wrote no usage
		},
		files: map[string]string{
			"recipe-care-ci-20261012-100000-ab12/token-usage.json": usage,
			"agent-deps-20261012-090000/token-usage.json":          usage,
		},
	}
	prev := connectTasks
	connectTasks = func(_ context.Context, ns, name string) (taskLister, error) {
		if ns != "overseer-kcc" || name != "fix-kcc-1" {
			t.Errorf("connected to %s/%s", ns, name)
		}
		return fake, nil
	}
	t.Cleanup(func() { connectTasks = prev })

	HarvestSandbox(context.Background(), "overseer-kcc", "fix-kcc-1", Meta{Repo: "o/r"})
	got := strings.Join(keys, "\n")
	for _, want := range []string{
		"fix-kcc-1:/workspaces/tasks/recipe-care-ci-20261012-100000-ab12 ",
		"fix-kcc-1:/workspaces/tasks/agent-deps-20261012-090000 ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("posted %q, want a record %q", got, want)
		}
	}
	if len(keys) != 2 || !fake.closed {
		t.Errorf("posted %d records, closed %v", len(keys), fake.closed)
	}
}

// The tests never reach a cluster: by default no sandbox can be read, and
// marking one harvested is recorded.
var marked []string

func TestMain(m *testing.M) {
	getSandbox = func(context.Context, string, string) (*unstructured.Unstructured, error) {
		return nil, errors.New("no cluster in tests")
	}
	markHarvested = func(_ context.Context, ns, name string, _ time.Time) error {
		marked = append(marked, ns+"/"+name)
		return nil
	}
	os.Exit(m.Run())
}

// collector records the usage keys and task PUTs it is sent (the path and
// query, and the files in the tar), and answers tasks with taskStatus.
func collector(t *testing.T, taskStatus int) (keys, tasks *[]string) {
	t.Helper()
	keys, tasks = &[]string{}, &[]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/usage":
			var rec UsageRecord
			_ = json.NewDecoder(r.Body).Decode(&rec)
			*keys = append(*keys, rec.Key)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
			var names []string
			tr := tar.NewReader(r.Body)
			for {
				hdr, err := tr.Next()
				if err != nil {
					break
				}
				body, _ := io.ReadAll(tr)
				names = append(names, hdr.Name+"="+strings.TrimSpace(string(body)))
			}
			*tasks = append(*tasks, r.URL.Path+"?"+r.URL.RawQuery+" "+strings.Join(names, ","))
			w.WriteHeader(taskStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("COLLECTOR_URL", ts.URL)
	return keys, tasks
}

func withTasks(t *testing.T, fake *fakeTasks) *bool {
	t.Helper()
	connected := false
	prev := connectTasks
	connectTasks = func(context.Context, string, string) (taskLister, error) {
		connected = true
		return fake, nil
	}
	t.Cleanup(func() { connectTasks = prev })
	return &connected
}

func sessionTasks() *fakeTasks {
	return &fakeTasks{
		entries: []spool.Entry{
			{Task: spool.Task{ID: "recipe-fix-20261020-100000-ab12"}},
			{Task: spool.Task{ID: "fix-20261011-080000"}}, // a classic task: no session
		},
		files: map[string]string{
			"recipe-fix-20261020-100000-ab12/session/stream.ndjson": `{"seq":1}` + "\n",
			"recipe-fix-20261020-100000-ab12/push.json":             `{"head":"abc"}`,
			"recipe-fix-20261020-100000-ab12/recipe.yaml":           "generated",
			"recipe-fix-20261020-100000-ab12/steps/uses.sh":         "generated",
			"recipe-fix-20261020-100000-ab12/pid":                   "123",
			"recipe-fix-20261020-100000-ab12/exit_code":             "",
		},
	}
}

// One pass sends a task's usage and its files (not the generated or the
// bookkeeping ones, not the empty ones), and marks the sandbox harvested
// once everything is in.
func TestHarvestSandboxArchivesTasks(t *testing.T) {
	_, tasks := collector(t, http.StatusOK)
	withTasks(t, sessionTasks())
	marked = nil
	if !HarvestSandbox(context.Background(), "overseer-kcc", "fix-kcc-7", Meta{Repo: "o/r", PR: 7}) {
		t.Fatal("HarvestSandbox reported a failure")
	}
	want := `/v1/tasks/fix-kcc-7/recipe-fix-20261020-100000-ab12?pr=7&repo=o%2Fr&taskType=recipe push.json={"head":"abc"},session/stream.ndjson={"seq":1}`
	if len(*tasks) != 1 || (*tasks)[0] != want {
		t.Errorf("tasks sent = %q, want [%q]", *tasks, want)
	}
	if len(marked) != 1 || marked[0] != "overseer-kcc/fix-kcc-7" {
		t.Errorf("marked = %v", marked)
	}
}

// A task the collector refuses leaves the sandbox unmarked, so the next
// pass tries again; a collector without an archive (501) wants none.
func TestHarvestSandboxArchiveFailures(t *testing.T) {
	collector(t, http.StatusInsufficientStorage)
	withTasks(t, sessionTasks())
	marked = nil
	if HarvestSandbox(context.Background(), "ns", "sb", Meta{Repo: "o/r"}) || len(marked) != 0 {
		t.Errorf("a refused task counted as harvested (marked %v)", marked)
	}

	collector(t, http.StatusNotImplemented)
	if !HarvestSandbox(context.Background(), "ns", "sb", Meta{Repo: "o/r"}) || len(marked) != 1 {
		t.Errorf("no archive: want harvested (marked %v)", marked)
	}
}

// A suspended sandbox harvested since its last activity is not woken
// before a delete; one active since is.
func TestHarvestSandboxSkipsHarvestedSuspended(t *testing.T) {
	collector(t, http.StatusOK)
	connected := withTasks(t, sessionTasks())
	sandbox := func(lastTask, harvested string) *unstructured.Unstructured {
		sb := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicas": int64(0)}}}
		sb.SetAnnotations(map[string]string{
			"sandbox.gemini.google.com/last-task-time": lastTask,
			"sandbox.gemini.google.com/harvested-at":   harvested,
		})
		return sb
	}
	prev := getSandbox
	t.Cleanup(func() { getSandbox = prev })

	getSandbox = func(context.Context, string, string) (*unstructured.Unstructured, error) {
		return sandbox("2026-10-20T10:00:00Z", "2026-10-20T11:00:00Z"), nil
	}
	if !HarvestSandbox(context.Background(), "ns", "sb", Meta{Repo: "o/r"}) || *connected {
		t.Errorf("woke a sandbox harvested since its last activity (connected %v)", *connected)
	}

	getSandbox = func(context.Context, string, string) (*unstructured.Unstructured, error) {
		return sandbox("2026-10-20T12:00:00Z", "2026-10-20T11:00:00Z"), nil
	}
	if !HarvestSandbox(context.Background(), "ns", "sb", Meta{Repo: "o/r"}) || !*connected {
		t.Errorf("skipped a sandbox active since its harvest")
	}
}

// A task server that lists the top level only still has its transcript
// read by name.
func TestArchiveTaskReadsTranscriptUnlisted(t *testing.T) {
	_, tasks := collector(t, http.StatusOK)
	fake := &topLevelOnly{fakeTasks: sessionTasks()}
	withTasks(t, fake.fakeTasks)
	if err := archiveTask(context.Background(), fake, "recipe-fix-20261020-100000-ab12", Meta{Repo: "o/r", Sandbox: "sb"}); err != nil {
		t.Fatal(err)
	}
	if len(*tasks) != 1 || !strings.Contains((*tasks)[0], `session/stream.ndjson={"seq":1}`) {
		t.Errorf("tasks sent = %q", *tasks)
	}
}

// topLevelOnly is a task server from before AllFiles listed subdirectories.
type topLevelOnly struct{ *fakeTasks }

func (f *topLevelOnly) AllFiles(ctx context.Context, id string) ([]string, error) {
	all, _ := f.fakeTasks.AllFiles(ctx, id)
	var top []string
	for _, n := range all {
		if !strings.Contains(n, "/") {
			top = append(top, n)
		}
	}
	return top, nil
}
