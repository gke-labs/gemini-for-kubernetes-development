package dispatcher

import (
	"testing"
	"time"
)

// After a run its engine could not serve, no task starts for a while: 5
// minutes, doubling with each such failure up to 30; a run that succeeds
// ends the wait.
func TestEngineBackoff(t *testing.T) {
	d := &Dispatcher{}
	now := time.Date(2026, 11, 4, 4, 0, 0, 0, time.UTC)
	if _, waiting := d.engineBackedOff(now); waiting {
		t.Fatal("waiting before any failure")
	}
	for i, want := range []time.Duration{5, 10, 20, 30, 30} {
		until := d.engineFailed(now)
		if got := until.Sub(now); got != want*time.Minute {
			t.Errorf("failure %d: no task until +%s, want +%s", i+1, got, want*time.Minute)
		}
	}
	if until, waiting := d.engineBackedOff(now.Add(29 * time.Minute)); !waiting || !until.Equal(now.Add(30*time.Minute)) {
		t.Errorf("at +29m: waiting %v until %s", waiting, until)
	}
	if _, waiting := d.engineBackedOff(now.Add(31 * time.Minute)); waiting {
		t.Error("still waiting after the backoff ran out")
	}
	d.engineServed()
	if _, waiting := d.engineBackedOff(now); waiting {
		t.Error("a run that succeeded did not end the wait")
	}
	if until := d.engineFailed(now); until.Sub(now) != 5*time.Minute {
		t.Errorf("after a success the wait starts over at 5m, got %s", until.Sub(now))
	}
}
