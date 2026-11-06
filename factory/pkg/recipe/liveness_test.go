package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastLiveness makes the turn's liveness checks run in milliseconds.
func fastLiveness(t *testing.T) {
	t.Helper()
	a, b, c, d := turnCheckAfter, turnBadLineGrace, turnStalledAfter, turnCheckPollEvery
	turnCheckAfter, turnBadLineGrace, turnStalledAfter, turnCheckPollEvery = 50*time.Millisecond, 20*time.Millisecond, 150*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { turnCheckAfter, turnBadLineGrace, turnStalledAfter, turnCheckPollEvery = a, b, c, d })
}

// sessionState is a daemon that answers GET /sessions/s with state (404
// when gone) and records cancels.
type sessionState struct {
	mu        sync.Mutex
	busy      bool
	gone      bool
	cancelled int
}

func (st *sessionState) session(t *testing.T) *DaemonSession {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/s", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.gone {
			http.Error(w, "no session s", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"id":"s","busy":%v}`, st.busy)
	})
	mux.HandleFunc("POST /sessions/s/cancel", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.cancelled++
		st.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return newDaemonSession(ts.URL, "s", "tok")
}

// A turn must end even when its end never reaches the task (KCC #15081:
// the disk full, the error event glued to a line cut short, the task
// waiting 7 hours): a line that is no whole event, or silence, has the
// task ask the session; one not busy, or gone, lost the turn's end, and
// one busy but silent with no tool running is stalled. A tool running is
// not silence.
func TestFollowEndsATurnWhoseEndIsLost(t *testing.T) {
	fastLiveness(t)
	ctx := context.Background()
	run := func(t *testing.T, st *sessionState, events string, keepOpen bool) (string, error) {
		t.Helper()
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, events)
			if !keepOpen {
				_ = pw.Close()
			}
		}()
		t.Cleanup(func() { _ = pw.Close() })
		done := make(chan struct{})
		var reply string
		var err error
		go func() { reply, err = st.session(t).follow(ctx, pr); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the turn never ended")
		}
		return reply, err
	}

	t.Run("a cut line, the session no longer busy: the turn's end was lost", func(t *testing.T) {
		st := &sessionState{busy: false}
		_, err := run(t, st, `{"seq":1,"kind":"usage_update","dat{"seq":2,"kind":"error","data":{"message":"prompt failed: connection to agent closed"}}`+"\n", true)
		if !errors.Is(err, errTurnLost) || !EngineTransient(err) {
			t.Errorf("err = %v, want the turn lost (asked again)", err)
		}
	})

	t.Run("the session gone", func(t *testing.T) {
		st := &sessionState{gone: true}
		if _, err := run(t, st, "", true); !errors.Is(err, errTurnLost) || !strings.Contains(err.Error(), "gone") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("busy and silent, no tool running: stalled, and the turn cancelled", func(t *testing.T) {
		st := &sessionState{busy: true}
		_, err := run(t, st, "", true)
		if err == nil || !strings.Contains(err.Error(), "the turn stalled") || st.cancelled != 1 {
			t.Errorf("err = %v, cancels %d", err, st.cancelled)
		}
	})

	t.Run("busy and silent with a tool running: not stalled", func(t *testing.T) {
		st := &sessionState{busy: true}
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, `{"seq":1,"kind":"tool_call","data":{"toolCallId":"t1","status":"in_progress","title":"scripts/validate-prereqs.sh"}}`+"\n")
			time.Sleep(400 * time.Millisecond) // past turnStalledAfter
			_, _ = io.WriteString(pw, `{"seq":2,"kind":"tool_call_update","data":{"toolCallId":"t1","status":"completed"}}`+"\n")
			_, _ = io.WriteString(pw, `{"seq":3,"kind":"turn_end","data":{"stopReason":"end_turn"}}`+"\n")
		}()
		t.Cleanup(func() { _ = pw.Close() })
		if _, err := st.session(t).follow(ctx, pr); err != nil || st.cancelled != 0 {
			t.Errorf("err = %v, cancels %d", err, st.cancelled)
		}
	})
}
