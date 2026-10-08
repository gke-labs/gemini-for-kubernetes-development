package acpd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGeminiRetry(t *testing.T) {
	tests := []struct {
		line   string
		want   EngineRetry
		wantOK bool
	}{
		{line: "Attempt 3 failed with status 429. Retrying with backoff... _ApiError: {", want: EngineRetry{Status: "429", Attempt: 3}, wantOK: true},
		{line: "Attempt 1 failed with status 503. Retrying after explicit delay of 2000ms...", want: EngineRetry{Status: "503", Attempt: 1}, wantOK: true},
		{line: "Attempt 2 failed with 429 error (no Retry-After header). Retrying with backoff...", want: EngineRetry{Status: "429", Attempt: 2}, wantOK: true},
		{line: "Attempt 4 failed with 5xx error. Retrying with backoff...", want: EngineRetry{Status: "5xx", Attempt: 4}, wantOK: true},
		{line: "Attempt 5 failed. Retrying with backoff...", want: EngineRetry{Attempt: 5}, wantOK: true},
		{line: `      "message": "You exceeded your current quota, please check your plan and billing details."`, want: EngineRetry{QuotaExceeded: true}, wantOK: true},
		{line: "Loaded cached credentials."},
	}
	for _, tc := range tests {
		got, ok := geminiRetry(tc.line)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("geminiRetry(%q) = %+v, %v; want %+v, %v", tc.line, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestLineWriter(t *testing.T) {
	var file bytes.Buffer
	var lines []string
	w := &lineWriter{w: &file, onLine: func(l string) { lines = append(lines, l) }}
	long := strings.Repeat("x", maxStderrLine+100)
	for _, p := range []string{"Attempt 1 fa", "iled.\nsecond\nlo", long, "\n", "unterminated"} {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Attempt 1 failed.", "second", ("lo" + long)[:maxStderrLine]}
	if !slices.Equal(lines, want) {
		t.Errorf("lines %q, want %q", lines, want)
	}
	if file.String() != "Attempt 1 failed.\nsecond\nlo"+long+"\nunterminated" {
		t.Error("the log did not get every byte")
	}
}

// TestEngineRetriesEndToEnd: retries the engine logs while a turn waits on
// its model are markers in the transcript, before the answer, and the
// session reports the last one as retrying until the engine speaks again.
func TestEngineRetriesEndToEnd(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)
	gate := strings.TrimPrefix(Engines["fake-retry"].Env[0], "FAKE_GATE=")

	resp := create(t, ts, "s1", "fake-retry", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d", resp.StatusCode)
	}
	if r := getSession(t, ts, "s1").Retrying; r != nil {
		t.Fatalf("retrying before any turn: %+v", r)
	}
	promptResp, err := ts.Client().Post(ts.URL+"/sessions/s1/prompt", "application/json", strings.NewReader(`{"text":"!retry"}`))
	if err != nil {
		t.Fatal(err)
	}
	promptResp.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		s := getSession(t, ts, "s1")
		// The quota line follows the retry it is about.
		if s.Retrying != nil && s.Retrying.Attempt == 2 && s.Retrying.QuotaExceeded {
			if !s.Busy || s.Retrying.Status != "429" {
				t.Fatalf("session %+v, retrying %+v", s, s.Retrying)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never retrying: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for time.Now().Before(deadline) && !slices.Contains(kinds, KindTurnEnd) {
		kinds = transcriptKinds(t, ts, "s1")
		time.Sleep(20 * time.Millisecond)
	}
	want := []string{KindUserPrompt, KindEngineRetry, KindEngineRetry, "agent_message_chunk", KindTurnEnd}
	if !slices.Equal(kinds, want) {
		t.Fatalf("transcript %v, want %v", kinds, want)
	}
	if r := getSession(t, ts, "s1").Retrying; r != nil {
		t.Errorf("still retrying after the turn: %+v", r)
	}

	// The counts, for whoever reads the session directory.
	data, err := os.ReadFile(filepath.Join(srv.stateDir, "s1", RetriesFile))
	if err != nil {
		t.Fatal(err)
	}
	var counts EngineRetries
	if err := json.Unmarshal(data, &counts); err != nil {
		t.Fatal(err)
	}
	if want := (EngineRetries{Statuses: map[string]int{"503": 1, "429": 1}, QuotaExceeded: true}); !maps.Equal(counts.Statuses, want.Statuses) || !counts.QuotaExceeded {
		t.Errorf("%s = %+v, want %+v", RetriesFile, counts, want)
	}
}

func TestLoadRetries(t *testing.T) {
	dir := t.TempDir()
	if r := loadRetries(dir); r.Statuses == nil || len(r.Statuses) != 0 {
		t.Fatalf("no file: %+v", r)
	}
	if err := replaceFile(dir, RetriesFile, []byte(`{"statuses":{"429":3}}`)); err != nil {
		t.Fatal(err)
	}
	if r := loadRetries(dir); r.Statuses["429"] != 3 {
		t.Fatalf("got %+v", r)
	}
}

// transcriptKinds is the kinds of a session's transcript so far, in order.
func transcriptKinds(t *testing.T, ts *httptest.Server, id string) []string {
	t.Helper()
	resp, err := ts.Client().Get(fmt.Sprintf("%s/sessions/%s/events?follow=false&offset=0", ts.URL, id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var kinds []string
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}
