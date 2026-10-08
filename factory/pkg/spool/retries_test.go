package spool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineRetries(t *testing.T) {
	taskDir := t.TempDir()
	if r := engineRetries(taskDir); r != nil {
		t.Fatalf("no session: %+v", r)
	}
	file := filepath.Join(taskDir, EngineRetriesFile)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"statuses":{}}`)
	if r := engineRetries(taskDir); r != nil {
		t.Fatalf("nothing retried: %+v", r)
	}

	write(`{"statuses":{"429":2,"503":1},"quota_exceeded":true}`)
	r := engineRetries(taskDir)
	if r == nil || r.Statuses["429"] != 2 || r.Statuses["503"] != 1 || r.Total() != 3 || !r.QuotaExceeded {
		t.Fatalf("got %+v", r)
	}

	e := Entry{}
	Overlay(&e, taskDir)
	if e.EngineRetries == nil || e.EngineRetries.Total() != 3 {
		t.Fatalf("Overlay: %+v", e.EngineRetries)
	}
}
