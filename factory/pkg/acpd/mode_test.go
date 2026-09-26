package acpd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// create posts a session with an explicit engine and mode. Separate from
// createSession, which is fixed to the mode-less engine that the rest of
// the suite uses.
func create(t *testing.T, ts *httptest.Server, id, engine, mode string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"engine":%q,"mode":%q}`, id, engine, mode)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set(APIKeyHeader, "k")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	return resp
}

// decodeSession reads a session body, failing the test on an unexpected
// status so that the caller does not have to unpick an error object.
func decodeSession(t *testing.T, resp *http.Response, wantStatus int) sessionResponse {
	t.Helper()
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, wantStatus, buf.String())
	}
	var out sessionResponse
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decoding session: %v (%s)", err, buf.String())
	}
	return out
}

// promptAndReadEcho sends one turn and returns the agent's reply. The
// fake agent names the mode it is actually in, so this is how a test
// distinguishes a mode acpd merely recorded from one the engine took.
func promptAndReadEcho(t *testing.T, ts *httptest.Server, id, text string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Attach before prompting, or the reply can land before the stream is
	// open and the read below waits for a turn that is already over.
	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/sessions/%s/events?offset=0", ts.URL, id), nil)
	if err != nil {
		t.Fatalf("building stream request: %v", err)
	}
	stream, err := ts.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer stream.Body.Close()

	promptResp, err := ts.Client().Post(fmt.Sprintf("%s/sessions/%s/prompt", ts.URL, id),
		"application/json", strings.NewReader(fmt.Sprintf(`{"text":%q}`, text)))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	promptResp.Body.Close()
	if promptResp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt returned %d", promptResp.StatusCode)
	}

	echo := ""
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("stream line is not an Event: %v (%s)", err, scanner.Text())
		}
		if ev.Kind == "agent_message_chunk" {
			echo = string(ev.Data)
		}
		if ev.Kind == KindTurnEnd {
			break
		}
		if ev.Kind == KindError {
			t.Fatalf("session reported an error: %s", ev.Data)
		}
	}
	return echo
}

func TestSessionStartsInTheModeTheCallerAsked(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	got := decodeSession(t, create(t, ts, "s1", "fake-modes", GeminiModeYolo), http.StatusCreated)
	if got.Mode != GeminiModeYolo {
		t.Errorf("session mode = %q, want %q", got.Mode, GeminiModeYolo)
	}
	// The set a client needs to offer a switcher. Without it the browser
	// would have to hardcode one engine's vocabulary.
	if len(got.AvailableModes) != 2 {
		t.Errorf("availableModes = %+v, want the two the engine advertised", got.AvailableModes)
	}

	// The engine's own account of which mode it is in. acpd recording the
	// mode it asked for proves nothing on its own.
	if echo := promptAndReadEcho(t, ts, "s1", "hello"); !strings.Contains(echo, "mode:yolo") {
		t.Errorf("engine is not in the requested mode: %s", echo)
	}
}

func TestCreateFailsWhenTheEngineDoesNotOfferTheMode(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	// Loudly, rather than falling back to prompting. Nobody is watching a
	// session the controller started, so a silent fallback is a turn that
	// blocks for the permission timeout and then dies — the failure this
	// whole mechanism exists to prevent.
	resp := create(t, ts, "s1", "fake-modes", "banana")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("create with an unknown mode returned %d, want 500", resp.StatusCode)
	}

	// And the half-started session must not be left in the registry.
	missing, err := ts.Client().Get(ts.URL + "/sessions/s1")
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("failed create left session registered: GET returned %d", missing.StatusCode)
	}
}

func TestAModelessEngineIgnoresTheRequestedMode(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	// An engine that advertises no modes has not refused one, it has never
	// heard of them. Failing here would make acpd refuse every engine but
	// the one that happens to implement the optional half of ACP.
	got := decodeSession(t, create(t, ts, "s1", "fake", GeminiModeYolo), http.StatusCreated)
	if got.Mode != "" {
		t.Errorf("mode = %q, want empty from an engine with no modes", got.Mode)
	}
	if len(got.AvailableModes) != 0 {
		t.Errorf("availableModes = %+v, want none", got.AvailableModes)
	}
}

func TestSetModeSwitchesTheEngineMidSession(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	started := decodeSession(t, create(t, ts, "s1", "fake-modes", ""), http.StatusCreated)
	if started.Mode != GeminiModeDefault {
		t.Fatalf("session started in %q, want the engine's own default", started.Mode)
	}

	resp, err := ts.Client().Post(ts.URL+"/sessions/s1/mode", "application/json",
		strings.NewReader(`{"mode":"yolo"}`))
	if err != nil {
		t.Fatalf("POST mode: %v", err)
	}
	if got := decodeSession(t, resp, http.StatusOK); got.Mode != GeminiModeYolo {
		t.Errorf("mode after the switch = %q, want yolo", got.Mode)
	}

	echo := promptAndReadEcho(t, ts, "s1", "hello")
	if !strings.Contains(echo, "mode:yolo") {
		t.Errorf("the switch did not reach the engine: %s", echo)
	}
	// A session that stopped asking has to say so in the record it keeps.
	if !transcriptHas(t, ts, "s1", KindModeChanged, `"currentModeId":"yolo"`) {
		t.Error("the mode change was not recorded in the transcript")
	}
}

func TestSetModeRejectsAModeTheEngineDoesNotOffer(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	decodeSession(t, create(t, ts, "s1", "fake-modes", ""), http.StatusCreated)

	resp, err := ts.Client().Post(ts.URL+"/sessions/s1/mode", "application/json",
		strings.NewReader(`{"mode":"banana"}`))
	if err != nil {
		t.Fatalf("POST mode: %v", err)
	}
	defer resp.Body.Close()
	// 400, not 502: the caller named something that was never on offer,
	// and the engine is not at fault for a list it published correctly.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown mode returned %d, want 400", resp.StatusCode)
	}

	still, err := ts.Client().Get(ts.URL + "/sessions/s1")
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	if got := decodeSession(t, still, http.StatusOK); got.Mode != GeminiModeDefault {
		t.Errorf("mode after a rejected switch = %q, want it unchanged", got.Mode)
	}
}

func TestModeChangedByTheEngineIsFollowed(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	decodeSession(t, create(t, ts, "s1", "fake-modes", GeminiModeDefault), http.StatusCreated)

	// An agent may change mode on its own — plan mode ends when the plan
	// does. If acpd only trusted its own set_mode calls it would keep
	// telling the UI the session is in a mode it has left.
	promptAndReadEcho(t, ts, "s1", "!mode yolo")

	resp, err := ts.Client().Get(ts.URL + "/sessions/s1")
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	if got := decodeSession(t, resp, http.StatusOK); got.Mode != GeminiModeYolo {
		t.Errorf("mode = %q, want the one the engine reported it switched to", got.Mode)
	}
}

// transcriptHas reports whether a one-shot read of the transcript holds an
// event of the given kind whose payload contains want.
func transcriptHas(t *testing.T, ts *httptest.Server, id, kind, want string) bool {
	t.Helper()
	resp, err := ts.Client().Get(fmt.Sprintf("%s/sessions/%s/events?follow=false", ts.URL, id))
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Kind == kind && strings.Contains(string(ev.Data), want) {
			return true
		}
	}
	return false
}
