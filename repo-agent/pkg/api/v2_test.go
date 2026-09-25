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

// A verb the platform cannot execute must be offered greyed out with a
// reason, not offered plainly and refused after the user fills in a
// form. The availability field exists for exactly this judgement.
func TestUnportedRecipesAreOfferedUnavailable(t *testing.T) {
	var ported, unported int
	for _, r := range v2Recipes {
		a := offer(r)
		if a.Available {
			ported++
			continue
		}
		unported++
		if a.Reason == "" {
			t.Errorf("%s is unavailable with no reason — a disabled button must say why", r.Name)
		}
	}
	if ported == 0 || unported == 0 {
		t.Fatalf("ported=%d unported=%d; expected a mix while the migration is partway", ported, unported)
	}
}

// Inputs must survive the trip to the row. Dropping them here is what
// cost investigate its topic box.
func TestOfferCarriesDeclaredInputs(t *testing.T) {
	for _, r := range v2Recipes {
		if len(r.Inputs) == 0 {
			continue
		}
		if got := len(offer(r).Inputs); got != len(r.Inputs) {
			t.Errorf("%s: offer carries %d inputs, recipe declares %d", r.Name, got, len(r.Inputs))
		}
	}
}
