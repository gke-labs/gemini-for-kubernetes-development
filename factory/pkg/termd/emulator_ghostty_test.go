//go:build libghostty

package termd

import (
	"os"
	"strings"
	"testing"
	"time"
)

// What a program set up besides the screen survives a snapshot: here
// bracketed paste and application cursor keys, which a client that missed
// them would otherwise get wrong (a paste typed out line by line, arrow
// keys the program does not read as arrows).
func TestSnapshotRestoresModes(t *testing.T) {
	defer func(k int) { keep = k }(keep)
	keep = 16 << 10
	script := `printf '\033[?2004h\033[?1h'; i=0; while [ $i -lt 3000 ]; do echo line-$i-padding-padding; i=$((i+1)); done; sleep 30`
	s, err := start("m", Spec{Argv: []string{"/bin/sh", "-c", script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Kill(time.Second); s.release() }()
	waitOffset(t, s, 60000)
	a := s.attach(-1)
	defer s.detach()
	if !a.Reset || a.Snapshot == "" {
		t.Fatalf("attach %+v, want a snapshot", a)
	}
	for _, mode := range []string{"\x1b[?2004h", "\x1b[?1h"} {
		if !strings.Contains(a.Snapshot, mode) {
			t.Errorf("snapshot lacks %q", mode)
		}
	}
}

// waitOffset waits until s has written at least n bytes.
func waitOffset(t *testing.T, s *Session, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.Info().Offset < n {
		if time.Now().After(deadline) {
			t.Fatalf("session wrote %d bytes, want %d", s.Info().Offset, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
