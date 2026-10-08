package spool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineRetries(t *testing.T) {
	taskDir := t.TempDir()
	if r := engineRetries(taskDir); r != nil {
		t.Fatalf("no log: %+v", r)
	}
	log := filepath.Join(taskDir, EngineLogFile)
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}

	write("Loaded cached credentials.\n")
	if r := engineRetries(taskDir); r != nil {
		t.Fatalf("a log without retries: %+v", r)
	}

	write("Attempt 1 failed with status 503. Retrying with backoff... _ApiError: {\n" +
		"Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\n" +
		"Attempt 2 failed with status 429. Retrying with backoff... _ApiError: {\n")
	r := engineRetries(taskDir)
	if r == nil || r.Statuses["429"] != 2 || r.Statuses["503"] != 1 || r.Total() != 3 || r.QuotaExceeded {
		t.Fatalf("got %+v", r)
	}

	// The log grows as the task runs; the counts follow it.
	write(`  "message": "You exceeded your current quota, please check your plan and billing details."` + "\n")
	if r := engineRetries(taskDir); r == nil || !r.QuotaExceeded || r.Total() != 3 {
		t.Fatalf("got %+v", r)
	}

	e := Entry{}
	Overlay(&e, taskDir)
	if e.EngineRetries == nil || e.EngineRetries.Total() != 3 {
		t.Fatalf("Overlay: %+v", e.EngineRetries)
	}
}
