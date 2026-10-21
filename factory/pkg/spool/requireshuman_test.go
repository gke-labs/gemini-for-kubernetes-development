package spool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayReadsWhyATaskNeedsAPerson(t *testing.T) {
	dir := t.TempDir()
	var e Entry
	Overlay(&e, dir)
	if e.RequiresHuman != "" {
		t.Errorf("no file: %q", e.RequiresHuman)
	}
	if err := os.WriteFile(filepath.Join(dir, RequiresHumanFile), []byte("step 8:ask: too big\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Overlay(&e, dir)
	if e.RequiresHuman != "step 8:ask: too big" {
		t.Errorf("RequiresHuman = %q", e.RequiresHuman)
	}
	if err := os.WriteFile(filepath.Join(dir, RequiresHumanFile), []byte(strings.Repeat("x", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	Overlay(&e, dir)
	if len(e.RequiresHuman) != maxRequiresHuman {
		t.Errorf("len = %d, want %d", len(e.RequiresHuman), maxRequiresHuman)
	}
}

// The stop reason of the turn a task failed on goes on its entry, and an
// agent that ran out of turns is told apart from other stops.
func TestOverlayReadsTheStopReason(t *testing.T) {
	dir := t.TempDir()
	var e Entry
	Overlay(&e, dir)
	if e.StopReason != "" || e.OutOfTurns() {
		t.Errorf("no file: %q", e.StopReason)
	}
	if err := os.WriteFile(filepath.Join(dir, StopReasonFile), []byte("step 8:ask: max_turn_requests\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Overlay(&e, dir)
	if e.StopReason != "step 8:ask: max_turn_requests" || !e.OutOfTurns() {
		t.Errorf("StopReason = %q, out of turns %v", e.StopReason, e.OutOfTurns())
	}
	if err := os.WriteFile(filepath.Join(dir, StopReasonFile), []byte("step 8:ask: refusal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Overlay(&e, dir)
	if e.OutOfTurns() {
		t.Errorf("a refusal taken for running out of turns: %q", e.StopReason)
	}
}
