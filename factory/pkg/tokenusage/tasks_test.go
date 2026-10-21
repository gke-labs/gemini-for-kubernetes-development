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

package tokenusage

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const token = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"

const folder = "tasks/org/repo/fix-repo-7/recipe-care-ci-20261021-120000-ab12/"

func taskRec() TaskRecord {
	return TaskRecord{Repo: "org/repo", Sandbox: "fix-repo-7", Task: "recipe-care-ci-20261021-120000-ab12", TaskType: "recipe-care", PR: 14, Issue: 7}
}

func taskFiles() map[string][]byte {
	return map[string][]byte{
		"task.json":             []byte(`{"id":"recipe-care-ci-20261021-120000-ab12","run_name":"care-14","recipe":"care-ci","url":"https://github.com/org/repo/pull/14"}`),
		"status.json":           []byte(`{"state":"exited","exit_code":"0","started_at":"2026-10-21T12:00:00Z","ended_at":"2026-10-21T12:30:00Z"}`),
		"push.json":             []byte(`{"fork":"bot/repo","branch":"issue-7","base":"aaa","head":"552e7b4"}`),
		"task-output.applied":   []byte("2026-10-21T12:31:00Z\n"),
		"steps/02-setup.log":    []byte("cloned\n"),
		"session/stream.ndjson": []byte(`{"seq":1,"kind":"user_prompt"}` + "\n" + `{"seq":2,"data":"token ` + token + `"}` + "\n"),
	}
}

func tarOf(t *testing.T, files map[string][]byte) io.Reader {
	t.Helper()
	data, err := TarFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func gunzipped(t *testing.T, a Archive, name string) string {
	t.Helper()
	data, err := a.Read(context.Background(), name)
	if err != nil {
		t.Fatalf("archive has no %s: %v", name, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

func spooledTasks(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := os.ReadDir(s.taskSpoolDir())
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// countingArchive counts the writes of each object.
type countingArchive struct {
	*memArchive
	mu     sync.Mutex
	writes map[string]int
}

func (c *countingArchive) Write(ctx context.Context, name string, data []byte) error {
	c.mu.Lock()
	c.writes[name]++
	c.mu.Unlock()
	return c.memArchive.Write(ctx, name, data)
}

// A task's files are redacted, spooled, uploaded one object each under
// the task's folder with the manifest last, indexed, and gone from the
// PVC; the index survives a new PVC.
func TestTaskSpoolUploadIndex(t *testing.T) {
	ctx := context.Background()
	a := &countingArchive{memArchive: newMemArchive(), writes: map[string]int{}}
	s := openArchived(t, t.TempDir(), a)
	if err := s.PutTask(taskRec(), tarOf(t, taskFiles())); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if n := spooledTasks(t, s); n != 1 {
		t.Fatalf("spooled %d tasks, want 1", n)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := spooledTasks(t, s); n != 0 {
		t.Errorf("%d tasks left on the PVC after the flush", n)
	}

	stream := gunzipped(t, a, folder+"session/stream.ndjson.gz")
	if strings.Contains(stream, token) || !strings.Contains(stream, "[REDACTED]") || !strings.HasPrefix(stream, `{"seq":1`) {
		t.Errorf("archived transcript:\n%s", stream)
	}
	if got := gunzipped(t, a, folder+"steps/02-setup.log.gz"); got != "cloned\n" {
		t.Errorf("step log = %q", got)
	}
	data, err := a.Read(ctx, folder+"manifest.json")
	if err != nil {
		t.Fatalf("no manifest: %v", err)
	}
	var m TaskRecord
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Recipe != "care-ci" || m.RunName != "care-14" || m.ExitCode != "0" || m.PushedHead != "552e7b4" ||
		m.Applied != "2026-10-21T12:31:00Z" || m.Folder != folder || len(m.Files) != 6 || m.PR != 14 {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := a.Read(ctx, "task-index/00000000000000000001.seg.jsonl.gz"); err != nil {
		t.Errorf("task index not archived: %v (%v)", err, a.names())
	}

	recs := openArchived(t, t.TempDir(), a).ListTasks(TaskFilter{PR: 14})
	if len(recs) != 1 || recs[0].Key != "fix-repo-7/recipe-care-ci-20261021-120000-ab12" || recs[0].PushedHead != "552e7b4" {
		t.Fatalf("restored index = %+v", recs)
	}

	// The same files again are not spooled.
	if err := s.PutTask(taskRec(), tarOf(t, taskFiles())); err != nil {
		t.Fatal(err)
	}
	if n := spooledTasks(t, s); n != 0 {
		t.Errorf("unchanged task spooled again")
	}
	// A grown transcript is; only it and the manifest go up again.
	files := taskFiles()
	files["session/stream.ndjson"] = append(files["session/stream.ndjson"], []byte(`{"seq":3}`+"\n")...)
	if err := s.PutTask(taskRec(), tarOf(t, files)); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if a.writes[folder+"session/stream.ndjson.gz"] != 2 || a.writes[folder+"push.json.gz"] != 1 || a.writes[folder+"manifest.json"] != 2 {
		t.Errorf("writes = %v", a.writes)
	}
}

func TestTaskRefusals(t *testing.T) {
	if err := openArchived(t, t.TempDir(), nil).PutTask(taskRec(), tarOf(t, taskFiles())); !errors.Is(err, ErrNoArchive) {
		t.Errorf("no archive: %v", err)
	}
	s := openArchived(t, t.TempDir(), newMemArchive())
	bad := taskRec()
	bad.Task = "../escape"
	if err := s.PutTask(bad, tarOf(t, taskFiles())); err == nil {
		t.Errorf("task ../escape accepted")
	}
	bad = taskRec()
	bad.Repo = "no-owner"
	if err := s.PutTask(bad, tarOf(t, taskFiles())); err == nil {
		t.Errorf("repo without owner accepted")
	}
	for _, name := range []string{"../x", "a/b/c", "/abs", ".hidden", "manifest.json"} {
		if err := s.PutTask(taskRec(), tarOf(t, map[string][]byte{name: []byte("x")})); err == nil {
			t.Errorf("file %q accepted", name)
		}
	}

	defer func(n int64) { maxTaskBytes = n }(maxTaskBytes)
	maxTaskBytes = 16
	if err := s.PutTask(taskRec(), tarOf(t, map[string][]byte{"a": []byte("0123456789\n"), "b": []byte("0123456789\n")})); !errors.Is(err, ErrTaskTooLarge) {
		t.Errorf("too large: %v", err)
	}
	maxTaskBytes = 1 << 20

	defer func(n int64) { maxSpoolBytes = n }(maxSpoolBytes)
	maxSpoolBytes = 1
	if err := s.PutTask(taskRec(), tarOf(t, taskFiles())); err != nil {
		t.Fatalf("first task: %v", err)
	}
	other := taskRec()
	other.Task = "recipe-care-ci-20261021-130000-cd34"
	if err := s.PutTask(other, tarOf(t, taskFiles())); !errors.Is(err, ErrSpoolFull) {
		t.Errorf("spool full: %v", err)
	}
}

func TestTaskHTTP(t *testing.T) {
	s := openArchived(t, t.TempDir(), newMemArchive())
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()
	put := func(path string) int {
		body, _ := TarFiles(taskFiles())
		req, _ := http.NewRequest(http.MethodPut, srv.URL+path, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("/v1/tasks/fix-repo-7/recipe-fix-1?repo=org/repo&pr=7"); code != http.StatusOK {
		t.Errorf("PUT = %d", code)
	}
	if code := put("/v1/tasks/fix-repo-7/recipe-fix-1?repo=org/repo&pr=x"); code != http.StatusBadRequest {
		t.Errorf("PUT bad pr = %d", code)
	}
	if code := put("/v1/tasks/fix-repo-7/recipe-fix-1?repo=bad"); code != http.StatusBadRequest {
		t.Errorf("PUT bad repo = %d", code)
	}
	resp, err := http.Get(srv.URL + "/v1/tasks?pr=7")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tasks: %v %v", resp, err)
	}
	resp.Body.Close()
}
