package watch

import (
	"os"
	"path/filepath"
	"testing"
)

// The shared scan's snapshot persists where ScanState says, and one left
// in the queue directory (pushed to the repository by run.sh) is moved
// there, or dropped when the new place already has one.
func TestScanStatePath(t *testing.T) {
	queue := t.TempDir()
	inQueue := filepath.Join(queue, "scan_state.json")

	w := &Watcher{Flags: Flags{QueueDir: queue}}
	if got := w.scanStatePath(); got != inQueue {
		t.Errorf("without ScanState = %s, want %s", got, inQueue)
	}

	moved := filepath.Join(t.TempDir(), "cache", "scan_state.json")
	if err := os.WriteFile(inQueue, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.ScanState = moved
	if got := w.scanStatePath(); got != moved {
		t.Errorf("with ScanState = %s, want %s", got, moved)
	}
	if b, err := os.ReadFile(moved); err != nil || string(b) != "old" {
		t.Errorf("the queue directory's snapshot was not moved: %q, %v", b, err)
	}
	if _, err := os.Stat(inQueue); !os.IsNotExist(err) {
		t.Error("the snapshot is still in the queue directory")
	}

	if err := os.WriteFile(inQueue, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.scanStatePath()
	if b, _ := os.ReadFile(moved); string(b) != "old" {
		t.Errorf("a stale queue-directory snapshot replaced the newer one: %q", b)
	}
	if _, err := os.Stat(inQueue); !os.IsNotExist(err) {
		t.Error("the stale snapshot was left in the queue directory")
	}
}
