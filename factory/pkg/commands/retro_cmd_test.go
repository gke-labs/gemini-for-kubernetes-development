package commands

import (
	"testing"
	"time"
)

// --since is days, a duration or a date.
func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"7d":                   time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		"36h":                  time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		"2026-10-20":           time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		"2026-10-20T08:00:00Z": time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
	} {
		if got, err := parseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-3d", "soon"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
