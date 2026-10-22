package acpd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func sessionBusy(t *testing.T, ts *httptest.Server, id string) bool {
	t.Helper()
	resp, err := ts.Client().Get(fmt.Sprintf("%s/sessions/%s", ts.URL, id))
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer resp.Body.Close()
	var s struct{ Busy bool }
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s.Busy
}

// An engine that never ends its turn, and ignores the cancel, is stopped
// from acpd's side: the turn ends in the transcript as forced, the session
// closes, and created again it loads the conversation, idle, into an
// engine that answers.
func TestForceStopEndsATurnTheEngineNeverEnds(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	decodeSession(t, create(t, ts, "s1", "fake-load", GeminiModeYolo), http.StatusCreated)
	promptAndReadEcho(t, ts, "s1", "hello")

	if resp := post(t, ts, "/sessions/s1/prompt", `{"text":"!hang"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !transcriptHas(t, ts, "s1", "agent_message_chunk", "a reply, and then nothing") {
		if time.Now().After(deadline) {
			t.Fatal("the hanging turn never replied")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Stop asks; this engine does not listen.
	if resp := post(t, ts, "/sessions/s1/cancel", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	if !sessionBusy(t, ts, "s1") {
		t.Fatal("the turn ended on its own; this test needs an engine that ignores the cancel")
	}

	if resp := post(t, ts, "/sessions/s1/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("force stop: %d", resp.StatusCode)
	}
	if resp, _ := ts.Client().Get(ts.URL + "/sessions/s1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the force-stopped session is still open: %d", resp.StatusCode)
	}

	again := decodeSession(t, create(t, ts, "s1", "fake-load", GeminiModeYolo), http.StatusCreated)
	if !again.Loaded || sessionBusy(t, ts, "s1") {
		t.Fatalf("recreated: loaded=%v busy=%v, want a loaded, idle session", again.Loaded, sessionBusy(t, ts, "s1"))
	}
	var forced, errorsAfter int
	for _, ev := range readEvents(t, ts, "s1") {
		switch ev.Kind {
		case KindTurnEnd:
			var te TurnEnd
			_ = json.Unmarshal(ev.Data, &te)
			if te.Forced {
				forced++
				if te.StopReason != "cancelled" {
					t.Errorf("forced turn_end stopReason = %q", te.StopReason)
				}
			}
		case KindError:
			if forced > 0 {
				errorsAfter++
			}
		}
	}
	if forced != 1 || errorsAfter != 0 {
		t.Errorf("transcript: %d forced turn_ends, %d errors after it; want 1 and 0", forced, errorsAfter)
	}
	if echo := promptAndReadEcho(t, ts, "s1", "again"); !strings.Contains(echo, "history:hello") {
		t.Errorf("the reloaded agent does not remember the conversation: %s", echo)
	}

	// No turn in flight: nothing to stop.
	if resp := post(t, ts, "/sessions/s1/stop", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("force stop with no turn: %d, want 409", resp.StatusCode)
	}
}
