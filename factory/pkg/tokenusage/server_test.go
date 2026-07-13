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
	"encoding/json"
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
		Stats: statsFor("gemini-2.5-pro", 10, 20, 30),
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
