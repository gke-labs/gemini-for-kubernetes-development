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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp
}

func TestServerEndToEnd(t *testing.T) {
	ts := newTestServer(t)

	rec := UsageRecord{
		Key: "wf-issue-3:/workspaces/tasks/agent-x", Repo: "org/repo",
		TaskType: "agent-chore", Sandbox: "wf-issue-3", Issue: 3,
		Workflow: "issue-3", WorkflowName: "chore",
		RecordedAt: "2026-07-14T09:00:00Z",
		Stats:      statsFor("gemini-2.5-pro", 10, 20, 30),
	}
	if resp := postJSON(t, ts.URL+"/v1/usage", rec); resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: status %d", resp.StatusCode)
	}

	// Missing key -> 400.
	if resp := postJSON(t, ts.URL+"/v1/usage", UsageRecord{Repo: "org/repo"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("ingest without key: expected 400, got %d", resp.StatusCode)
	}

	var records struct {
		Records []UsageRecord `json:"records"`
	}
	getJSON(t, ts.URL+"/v1/usage/records?repo=org/repo", &records)
	if len(records.Records) != 1 {
		t.Fatalf("records: expected 1, got %d", len(records.Records))
	}

	var rollups struct {
		Rollups []Rollup `json:"rollups"`
	}
	getJSON(t, ts.URL+"/v1/usage/rollups/workflows?repo=org/repo", &rollups)
	if len(rollups.Rollups) != 1 || rollups.Rollups[0].Key != "issue-3" {
		t.Fatalf("workflow rollups: got %+v", rollups.Rollups)
	}

	// Subject ingest joins onto rollups.
	if resp := postJSON(t, ts.URL+"/v1/subjects", Subject{
		Key: "issue-3", Repo: "org/repo", Kind: "issue", Number: 3,
		State: "open", CreatedAt: "2026-07-10T00:00:00Z",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("subject ingest: status %d", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/v1/subjects", Subject{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("subject ingest without key: expected 400, got %d", resp.StatusCode)
	}
	getJSON(t, ts.URL+"/v1/usage/rollups/workflows?repo=org/repo", &rollups)
	if rollups.Rollups[0].State != "open" || rollups.Rollups[0].CreatedAt != "2026-07-10T00:00:00Z" {
		t.Errorf("workflow rollup subject join: got %+v", rollups.Rollups[0])
	}

	var daily struct {
		Rollups []Rollup `json:"rollups"`
	}
	getJSON(t, ts.URL+"/v1/usage/rollups/daily?repo=org/repo", &daily)
	if len(daily.Rollups) != 1 || daily.Rollups[0].Key != "2026-07-14" || daily.Rollups[0].TaskCount != 1 {
		t.Errorf("daily rollup: got %+v", daily.Rollups)
	}

	var detail Rollup
	getJSON(t, ts.URL+"/v1/usage/rollups/workflows/issue-3", &detail)
	if detail.TaskCount != 1 || len(detail.Records) != 1 {
		t.Fatalf("workflow detail: got %+v", detail)
	}
	if resp := getJSON(t, ts.URL+"/v1/usage/rollups/workflows/issue-99", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown workflow: expected 404, got %d", resp.StatusCode)
	}

	var mark struct {
		AlreadyPosted bool `json:"alreadyPosted"`
	}
	resp := postJSON(t, ts.URL+"/v1/workflows/issue-3/mark-summarized", nil)
	if err := json.NewDecoder(resp.Body).Decode(&mark); err != nil {
		t.Fatalf("decode mark: %v", err)
	}
	if mark.AlreadyPosted {
		t.Error("first mark-summarized: expected alreadyPosted=false")
	}
	resp = postJSON(t, ts.URL+"/v1/workflows/issue-3/mark-summarized", nil)
	if err := json.NewDecoder(resp.Body).Decode(&mark); err != nil {
		t.Fatalf("decode mark 2: %v", err)
	}
	if !mark.AlreadyPosted {
		t.Error("second mark-summarized: expected alreadyPosted=true")
	}

	if resp := getJSON(t, ts.URL+"/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
}

func TestTaskFileArchiveStreaming(t *testing.T) {
	ctx := context.Background()
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	ts := httptest.NewServer(NewServerForStore(s).Handler())
	defer ts.Close()

	taskJSONContent := []byte(`{"id":"task-stream-1","run_name":"run-1"}`)
	streamContent := []byte(`{"seq":1,"kind":"user_prompt"}` + "\n")
	logContent := []byte("setup step completed\n")

	files := map[string][]byte{
		"task.json":             taskJSONContent,
		"session/stream.ndjson": streamContent,
		"steps/01-setup.log":    logContent,
	}

	rec := TaskRecord{
		Repo:     "org/repo",
		Sandbox:  "sb-stream",
		Task:     "task-stream-1",
		TaskType: "recipe-fix",
	}
	if err := s.PutTask(rec, tarOf(t, files)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// 1. Successful file streaming (gzipped files in archive are uncompressed on the fly)
	resp, err := http.Get(ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/task.json")
	if err != nil {
		t.Fatalf("GET task.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, taskJSONContent) {
		t.Errorf("got %q, want %q", body, taskJSONContent)
	}

	// Stream a subdirectory file (session/stream.ndjson)
	respStream, err := http.Get(ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/session/stream.ndjson")
	if err != nil {
		t.Fatalf("GET session/stream.ndjson: %v", err)
	}
	defer respStream.Body.Close()
	if respStream.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", respStream.StatusCode)
	}
	if ct := respStream.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	bodyStream, _ := io.ReadAll(respStream.Body)
	if !bytes.Equal(bodyStream, streamContent) {
		t.Errorf("got %q, want %q", bodyStream, streamContent)
	}

	// Stream manifest.json (not gzipped in bucket)
	respManifest, err := http.Get(ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/manifest.json")
	if err != nil {
		t.Fatalf("GET manifest.json: %v", err)
	}
	defer respManifest.Body.Close()
	if respManifest.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", respManifest.StatusCode)
	}
	var manifestRec TaskRecord
	if err := json.NewDecoder(respManifest.Body).Decode(&manifestRec); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifestRec.Key != "sb-stream/task-stream-1" {
		t.Errorf("manifest key = %q, want sb-stream/task-stream-1", manifestRec.Key)
	}

	// 2. Security validation: refuse "..", leading slash, and invalid characters (400)
	invalidURLs := []string{
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/%2e%2e",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/%2e%2e/escape",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/session/%2e%2e/%2e%2e/escape",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/%2ftask.json",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/task*.json",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/bad%20name.txt",
		ts.URL + "/v1/tasks/bad%20sb/task-stream-1/files/task.json",
		ts.URL + "/v1/tasks/sb-stream/bad*task/files/task.json",
		ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/session%2f%2fstream.ndjson",
	}
	for _, u := range invalidURLs {
		r, err := http.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d", u, r.StatusCode, http.StatusBadRequest)
		}
	}

	// 3. Not found checks (404)
	r404, err := http.Get(ts.URL + "/v1/tasks/sb-stream/task-stream-1/files/missing.json")
	if err != nil {
		t.Fatal(err)
	}
	r404.Body.Close()
	if r404.StatusCode != http.StatusNotFound {
		t.Errorf("missing file status = %d, want 404", r404.StatusCode)
	}

	r404Task, err := http.Get(ts.URL + "/v1/tasks/sb-stream/unknown-task/files/task.json")
	if err != nil {
		t.Fatal(err)
	}
	r404Task.Body.Close()
	if r404Task.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task status = %d, want 404", r404Task.StatusCode)
	}

	// 4. No archive configured -> 501
	storeNoArchive := openArchived(t, t.TempDir(), nil)
	tsNoArchive := httptest.NewServer(NewServerForStore(storeNoArchive).Handler())
	defer tsNoArchive.Close()

	r501, err := http.Get(tsNoArchive.URL + "/v1/tasks/sb-stream/task-stream-1/files/task.json")
	if err != nil {
		t.Fatal(err)
	}
	r501.Body.Close()
	if r501.StatusCode != http.StatusNotImplemented {
		t.Errorf("no archive status = %d, want 501", r501.StatusCode)
	}
}
