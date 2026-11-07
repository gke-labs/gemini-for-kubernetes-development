package acpd

import (
	"context"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"
)

// An idle session - no turn, nobody following it, nothing in its
// transcript for longer than the limit - is closed and forgotten; one
// someone follows, or one active recently, stays.
func TestCloseIdle(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)
	for _, id := range []string{"idle", "watched", "recent"} {
		resp := createSession(t, ts, id, "k")
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: %d", id, resp.StatusCode)
		}
	}
	// Someone follows "watched".
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sessions/watched/events", nil)
		if resp, err := ts.Client().Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	srv.mu.Lock()
	watched := srv.sessions["watched"]
	srv.mu.Unlock()
	for i := 0; i < 100 && watched.Watchers() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if watched.Watchers() == 0 {
		t.Fatal("the follower never attached")
	}

	// Three hours on, with a two-hour limit: "recent" was active an hour
	// ago (judged from now), the others long before.
	now := time.Now().Add(3 * time.Hour)
	srv.mu.Lock()
	recent := srv.sessions["recent"]
	srv.mu.Unlock()
	recent.transcript.mu.Lock()
	recent.transcript.lastAppend = now.Add(-time.Hour)
	recent.transcript.mu.Unlock()

	closed := srv.CloseIdle(2*time.Hour, now)
	if !slices.Equal(closed, []string{"idle"}) {
		t.Errorf("closed %v, want [idle]", closed)
	}
	srv.mu.Lock()
	_, idleLeft := srv.sessions["idle"]
	_, watchedLeft := srv.sessions["watched"]
	_, recentLeft := srv.sessions["recent"]
	srv.mu.Unlock()
	if idleLeft || !watchedLeft || !recentLeft {
		t.Errorf("left: idle %v watched %v recent %v", idleLeft, watchedLeft, recentLeft)
	}
}
