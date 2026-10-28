package retros

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The first pass only records the schedule; the retro runs at its next
// point, once, for a week at least, and not again until the point after.
// A paused watcher starts none.
func TestSyncOnce(t *testing.T) {
	sched, err := ParseSchedule("@weekly")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) // a Wednesday
	var runs atomic.Int32
	var since time.Time
	paused := false
	c := New(0, sched, t.TempDir(), Deps{
		Paused: func() bool { return paused },
		Now:    func() time.Time { return now },
		Run: func(_ context.Context, s time.Time) error {
			runs.Add(1)
			since = s
			return nil
		},
	})
	ctx := context.Background()
	c.SyncOnce(ctx)
	c.Wait()
	if runs.Load() != 0 {
		t.Fatal("ran on the first pass")
	}
	now = now.Add(3 * 24 * time.Hour) // Saturday: not yet
	c.SyncOnce(ctx)
	c.Wait()
	if runs.Load() != 0 {
		t.Fatal("ran before the schedule's point")
	}
	now = time.Date(2026, 11, 1, 0, 5, 0, 0, time.UTC) // just past Sunday 00:00
	paused = true
	c.SyncOnce(ctx)
	c.Wait()
	if runs.Load() != 0 {
		t.Fatal("ran while paused")
	}
	paused = false
	c.SyncOnce(ctx)
	c.Wait()
	if runs.Load() != 1 || !since.Equal(now.AddDate(0, 0, -7)) {
		t.Fatalf("runs %d, since %s; want 1, a week back", runs.Load(), since)
	}
	now = now.Add(time.Hour)
	c.SyncOnce(ctx)
	c.Wait()
	if runs.Load() != 1 {
		t.Fatal("ran twice in one week")
	}
	// A new controller (a restart) reads the state: not again this week.
	c2 := New(0, sched, filepath.Dir(c.state), Deps{Now: func() time.Time { return now }, Run: func(context.Context, time.Time) error { runs.Add(1); return nil }})
	c2.SyncOnce(ctx)
	c2.Wait()
	if runs.Load() != 1 {
		t.Fatal("a restart ran it again")
	}
}

func TestParseSchedule(t *testing.T) {
	for _, ok := range []string{"@weekly", "0 6 * * 1", " @daily "} {
		if _, err := ParseSchedule(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "weekly", "* * *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
