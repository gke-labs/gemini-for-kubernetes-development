package warmworkspace

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestSyncOnce(t *testing.T) {
	ctx := context.Background()
	var warms atomic.Int32
	due, paused := true, false
	var dueErr error
	release := make(chan struct{})
	c := New(0, Deps{
		Paused: func() bool { return paused },
		Due:    func(context.Context) (bool, error) { return due, dueErr },
		Warm: func(context.Context) error {
			warms.Add(1)
			<-release
			return nil
		},
	})

	c.SyncOnce(ctx)
	c.SyncOnce(ctx) // one warm at a time
	close(release)
	c.Wait()
	if n := warms.Load(); n != 1 {
		t.Fatalf("warms = %d, want 1", n)
	}

	for name, set := range map[string]func(){
		"not due":    func() { due = false },
		"paused":     func() { paused = true },
		"due failed": func() { dueErr = errors.New("no API") },
	} {
		due, paused, dueErr = true, false, nil
		set()
		c.SyncOnce(ctx)
		c.Wait()
		if n := warms.Load(); n != 1 {
			t.Fatalf("%s: warms = %d, want 1", name, n)
		}
	}
}
