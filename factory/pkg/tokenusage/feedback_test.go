// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFeedbackSpoolUploadAndRedaction(t *testing.T) {
	ctx := context.Background()
	a := &countingArchive{memArchive: newMemArchive(), writes: map[string]int{}}
	s := openArchived(t, t.TempDir(), a)
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()

	payload := `{"author":"alice","kind":"review","state":"CHANGES_REQUESTED","body":"token ghp_0123456789abcdefghijklmnopqrstuvwxyzAB here","head":"c0ffee"}` + "\n" +
		`{"author":"bob","kind":"inline","path":"main.go","line":10,"body":"key AIza12345678901234567890123456789012345","head":"c0ffee"}` + "\n"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/v1/prs/test-org/test-repo/42/feedback", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /v1/prs/.../feedback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT feedback status %d, want 200", resp.StatusCode)
	}

	// Verify spooled on the PVC
	entries, err := os.ReadDir(s.feedbackSpoolDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 spooled feedback entry on PVC, got %d, err %v", len(entries), err)
	}

	// Flush to archive
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Verify spool is cleaned up
	entries, err = os.ReadDir(s.feedbackSpoolDir())
	if err == nil && len(entries) != 0 {
		t.Fatalf("expected 0 spooled feedback entries after flush, got %d", len(entries))
	}

	// Verify archived under prs/test-org/test-repo/42/feedback.jsonl.gz
	archivedName := "prs/test-org/test-repo/42/feedback.jsonl.gz"
	data := gunzipped(t, a, archivedName)

	if strings.Contains(data, "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB") {
		t.Errorf("archived data still contains raw GitHub personal token")
	}
	if strings.Contains(data, "AIza12345678901234567890123456789012345") {
		t.Errorf("archived data still contains raw Google API key")
	}
	if !strings.Contains(data, "token [REDACTED] here") {
		t.Errorf("expected redacted token, got:\n%s", data)
	}
	if !strings.Contains(data, "key [REDACTED]") {
		t.Errorf("expected redacted Google API key, got:\n%s", data)
	}
	if !strings.Contains(data, `"author":"alice"`) || !strings.Contains(data, `"author":"bob"`) {
		t.Errorf("archived data missing author fields:\n%s", data)
	}
}

func TestFeedbackRefusals(t *testing.T) {
	ctx := context.Background()
	noArchiveStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer noArchiveStore.Close()

	srvNoArchive := httptest.NewServer(NewServerForStore(noArchiveStore).Handler())
	defer srvNoArchive.Close()

	// 1. Without archive: 501 Not Implemented
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, srvNoArchive.URL+"/v1/prs/o/r/1/feedback", strings.NewReader("some feedback\n"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("no archive returned %d, want 501", resp.StatusCode)
	}

	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()

	// 2. Invalid PR number (not a number or <= 0): 400 Bad Request
	for _, badURL := range []string{
		srv.URL + "/v1/prs/o/r/0/feedback",
		srv.URL + "/v1/prs/o/r/-1/feedback",
		srv.URL + "/v1/prs/o/r/abc/feedback",
	} {
		req, _ = http.NewRequestWithContext(ctx, http.MethodPut, badURL, strings.NewReader("line\n"))
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("bad URL %s returned %d, want 400", badURL, resp.StatusCode)
		}
	}

	// 3. Spool full: 507 Insufficient Storage
	origSpoolBytes := maxSpoolBytes
	maxSpoolBytes = 1
	if err := s.PutFeedback("o", "r", 1, strings.NewReader("line\n")); err != nil {
		t.Fatalf("first feedback: %v", err)
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/v1/prs/o/r/10/feedback", strings.NewReader("line\n"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("full spool returned %d, want 507", resp.StatusCode)
	}
	maxSpoolBytes = origSpoolBytes

	// 4. Feedback too large: 413 Request Entity Too Large
	origTaskBytes := maxTaskBytes
	maxTaskBytes = 16
	defer func() { maxTaskBytes = origTaskBytes }()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/v1/prs/o/r/11/feedback", strings.NewReader("01234567890123456789\n"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("payload too large returned %d, want 413", resp.StatusCode)
	}
}

func TestFeedbackEmptyPayload(t *testing.T) {
	ctx := context.Background()
	a := &countingArchive{memArchive: newMemArchive(), writes: map[string]int{}}
	s := openArchived(t, t.TempDir(), a)
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/v1/prs/o/r/99/feedback", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty feedback returned %d, want 200", resp.StatusCode)
	}

	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	archivedName := "prs/o/r/99/feedback.jsonl.gz"
	data := gunzipped(t, a, archivedName)
	if data != "" {
		t.Errorf("expected empty string from gunzipped empty feedback, got %q", data)
	}
}

func TestFeedbackGetEndpoint(t *testing.T) {
	ctx := context.Background()
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	srv := httptest.NewServer(NewServerForStore(s).Handler())
	defer srv.Close()

	payload := `{"author":"alice","kind":"review","state":"CHANGES_REQUESTED","body":"needs improvement"}` + "\n" +
		`{"author":"bob","kind":"inline","path":"main.go","line":10,"body":"nit: naming"}` + "\n"

	// 1. Put feedback
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/v1/prs/test-org/test-repo/42/feedback", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil || putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT feedback failed: %v", err)
	}
	putResp.Body.Close()

	// 2. Read before flush (fallback to spool)
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/prs/test-org/test-repo/42/feedback", nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET feedback before flush status %d, want 200", getResp.StatusCode)
	}
	if string(body) != payload {
		t.Errorf("got %q, want %q", string(body), payload)
	}

	// 3. Flush to archive
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// 4. Read after flush (reads and decompresses from archive)
	getReq2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/prs/test-org/test-repo/42/feedback", nil)
	getResp2, err := http.DefaultClient.Do(getReq2)
	if err != nil {
		t.Fatal(err)
	}
	body2, err := io.ReadAll(getResp2.Body)
	getResp2.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if getResp2.StatusCode != http.StatusOK {
		t.Fatalf("GET feedback after flush status %d, want 200", getResp2.StatusCode)
	}
	if getResp2.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", getResp2.Header.Get("Content-Type"))
	}
	if string(body2) != payload {
		t.Errorf("got %q, want %q", string(body2), payload)
	}

	// 5. 404 Not Found for non-existent feedback
	getReq404, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/prs/test-org/test-repo/999/feedback", nil)
	getResp404, err := http.DefaultClient.Do(getReq404)
	if err != nil {
		t.Fatal(err)
	}
	getResp404.Body.Close()
	if getResp404.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing feedback status = %d, want 404", getResp404.StatusCode)
	}

	// 6. 400 Bad Request for invalid parameters
	for _, badURL := range []string{
		srv.URL + "/v1/prs/test-org/test-repo/0/feedback",
		srv.URL + "/v1/prs/test-org/test-repo/-1/feedback",
		srv.URL + "/v1/prs/test-org/test-repo/abc/feedback",
	} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, badURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", badURL, resp.StatusCode)
		}
	}

	// 7. 501 Not Implemented without archive
	noArchiveStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer noArchiveStore.Close()
	srvNoArchive := httptest.NewServer(NewServerForStore(noArchiveStore).Handler())
	defer srvNoArchive.Close()

	req501, _ := http.NewRequestWithContext(ctx, http.MethodGet, srvNoArchive.URL+"/v1/prs/test-org/test-repo/42/feedback", nil)
	resp501, err := http.DefaultClient.Do(req501)
	if err != nil {
		t.Fatal(err)
	}
	resp501.Body.Close()
	if resp501.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET without archive status = %d, want 501", resp501.StatusCode)
	}
}
