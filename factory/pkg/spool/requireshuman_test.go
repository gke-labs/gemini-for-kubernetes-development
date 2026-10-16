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
