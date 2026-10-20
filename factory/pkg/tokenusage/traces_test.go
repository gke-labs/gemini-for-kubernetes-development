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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const token = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"

func traceRec() TraceRecord {
	return TraceRecord{Repo: "org/repo", Sandbox: "fix-repo-7", Task: "fix-20261020-120000", TaskType: "fix", PR: 7}
}

func archived(t *testing.T, a *memArchive, name string) string {
	t.Helper()
	data, err := a.Read(context.Background(), name)
	if err != nil {
		t.Fatalf("archive has no %s: %v (has %v)", name, err, a.names())
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

func spooled(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := os.ReadDir(s.traceSpoolDir())
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// A trace is redacted, spooled, uploaded by the flusher, indexed, and
// gone from the PVC; the index survives a new PVC.
func TestTraceSpoolUploadIndex(t *testing.T) {
	ctx := context.Background()
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	content := `{"seq":1,"kind":"user_prompt"}` + "\n" + `{"seq":2,"data":"token ` + token + `"}` + "\n"
	if err := s.PutTraceContent(traceRec(), strings.NewReader(content)); err != nil {
		t.Fatalf("PutTraceContent: %v", err)
	}
	if n := spooled(t, s); n != 1 {
		t.Fatalf("spooled %d traces, want 1", n)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := spooled(t, s); n != 0 {
		t.Errorf("%d traces left on the PVC after the flush", n)
	}
	got := archived(t, a, "traces/org/repo/pr-7/fix-repo-7/fix-20261020-120000/stream.ndjson.gz")
	if strings.Contains(got, token) || !strings.Contains(got, "[REDACTED]") || !strings.HasPrefix(got, `{"seq":1`) {
		t.Errorf("archived trace:\n%s", got)
	}
	if _, err := a.Read(ctx, "trace-index/00000000000000000001.seg.jsonl.gz"); err != nil {
		t.Errorf("trace index not archived: %v (%v)", err, a.names())
	}

	recs := openArchived(t, t.TempDir(), a).ListTraces(TraceFilter{PR: 7})
	if len(recs) != 1 || recs[0].Key != "fix-repo-7/fix-20261020-120000" || recs[0].Object == "" || recs[0].SHA256 == "" {
		t.Fatalf("restored index = %+v", recs)
	}

	// The same content again is not spooled; changed content is.
	if err := s.PutTraceContent(traceRec(), strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if n := spooled(t, s); n != 0 {
		t.Errorf("unchanged trace spooled again")
	}
	if err := s.PutTraceContent(traceRec(), strings.NewReader(content+`{"seq":3}`+"\n")); err != nil {
		t.Fatal(err)
	}
	if n := spooled(t, s); n != 1 {
		t.Errorf("changed trace not spooled")
	}
}

func TestTraceRefusals(t *testing.T) {
	if err := openArchived(t, t.TempDir(), nil).PutTraceContent(traceRec(), strings.NewReader("{}\n")); !errors.Is(err, ErrNoArchive) {
		t.Errorf("no archive: %v", err)
	}
	s := openArchived(t, t.TempDir(), newMemArchive())
	bad := traceRec()
	bad.Task = "../escape"
	if err := s.PutTraceContent(bad, strings.NewReader("{}\n")); err == nil {
		t.Errorf("task ../escape accepted")
	}
	bad = traceRec()
	bad.Repo = "no-owner"
	if err := s.PutTraceContent(bad, strings.NewReader("{}\n")); err == nil {
		t.Errorf("repo without owner accepted")
	}

	defer func(n int64) { maxTraceBytes = n }(maxTraceBytes)
	maxTraceBytes = 8
	if err := s.PutTraceContent(traceRec(), strings.NewReader(`{"seq":1,"long":true}`+"\n")); !errors.Is(err, ErrTraceTooLarge) {
		t.Errorf("too large: %v", err)
	}
	maxTraceBytes = 1 << 20

	defer func(n int64) { maxSpoolBytes = n }(maxSpoolBytes)
	maxSpoolBytes = 1
	if err := s.PutTraceContent(traceRec(), strings.NewReader("{}\n")); err != nil {
		t.Fatalf("first trace: %v", err)
	}
	other := traceRec()
	other.Task = "fix-20261020-130000"
	if err := s.PutTraceContent(other, strings.NewReader("{}\n")); !errors.Is(err, ErrSpoolFull) {
		t.Errorf("spool full: %v", err)
	}
}

func TestTraceHTTP(t *testing.T) {
	s := openArchived(t, t.TempDir(), newMemArchive())
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()
	put := func(path string) int {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader("{}\n"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("/v1/traces/fix-repo-7/fix-1?repo=org/repo&pr=7"); code != http.StatusOK {
		t.Errorf("PUT = %d", code)
	}
	if code := put("/v1/traces/fix-repo-7/fix-1?repo=org/repo&pr=x"); code != http.StatusBadRequest {
		t.Errorf("PUT bad pr = %d", code)
	}
	if code := put("/v1/traces/fix-repo-7/fix-1?repo=bad"); code != http.StatusBadRequest {
		t.Errorf("PUT bad repo = %d", code)
	}
	resp, err := http.Get(srv.URL + "/v1/traces?pr=7")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET traces: %v %v", resp, err)
	}
	resp.Body.Close()
}
