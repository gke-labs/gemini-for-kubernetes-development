package api

import (
	"testing"
	"time"
)

func TestSinceCutoff(t *testing.T) {
	cases := []struct {
		since  string
		window time.Duration // 0 means "no filter"
	}{
		{"", 0},
		{"24h", 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"7d", 7 * 24 * time.Hour}, // Go durations stop at hours; a log window should not
		// A window nobody can parse must not silently hide rows: an
		// over-long activity list is a nuisance, a truncated one is a lie.
		{"last tuesday", 0},
		{"xd", 0},
	}
	for _, tc := range cases {
		got := sinceCutoff(tc.since)
		if tc.window == 0 {
			if !got.IsZero() {
				t.Errorf("sinceCutoff(%q) = %v, want no filter", tc.since, got)
			}
			continue
		}
		want := time.Now().Add(-tc.window)
		if diff := got.Sub(want); diff > time.Second || diff < -time.Second {
			t.Errorf("sinceCutoff(%q) off by %v", tc.since, diff)
		}
	}
}
