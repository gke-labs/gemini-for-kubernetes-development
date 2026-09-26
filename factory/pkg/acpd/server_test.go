package acpd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent is a minimal ACP agent: enough of the protocol to complete a
// handshake and answer one prompt. It echoes the credential it was started
// with back as agent output, which is how the test proves the key reached
// the process environment rather than merely being accepted by the API.
//
// With --modes it also implements session modes: session/new advertises
// two, session/set_mode enforces the list, and the prompt echo names the
// mode in force — so a test can prove the switch reached the engine and
// not merely acpd's own bookkeeping. Without the flag it behaves like an
// agent that has never heard of modes, which is the other case that has
// to keep working.
const fakeAgent = `
import json, os, sys

modes = "--modes" in sys.argv[1:]
current = "default"

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    method, mid = msg.get("method"), msg.get("id")

    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": 1,
            "agentInfo": {"name": "fake"},
            "authMethods": [{"id": "fake-auth", "name": "Fake"}]}})
    elif method == "authenticate":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/new":
        result = {"sessionId": "agent-side-id"}
        if modes:
            result["modes"] = {
                "currentModeId": current,
                "availableModes": [
                    {"id": "default", "name": "Default", "description": "Prompts for approval"},
                    {"id": "yolo", "name": "YOLO", "description": "Auto-approves all tools"}]}
        send({"jsonrpc": "2.0", "id": mid, "result": result})
    elif method == "session/set_mode":
        wanted = msg["params"]["modeId"]
        if not modes or wanted not in ("default", "yolo"):
            send({"jsonrpc": "2.0", "id": mid,
                  "error": {"code": -32602, "message": "no such mode: %s" % wanted}})
        else:
            current = wanted
            send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/prompt":
        prompt = msg["params"]["prompt"][0]["text"]
        if prompt.startswith("!mode "):
            current = prompt.split(" ", 1)[1]
            send({"jsonrpc": "2.0", "method": "session/update", "params": {
                "sessionId": "agent-side-id",
                "update": {"sessionUpdate": "current_mode_update", "currentModeId": current}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {
            "sessionId": "agent-side-id",
            "update": {"sessionUpdate": "agent_message_chunk",
                       "content": {"type": "text",
                                   "text": "saw:%s key:%s mode:%s" % (
                                       prompt, os.environ.get("FAKE_KEY", "<unset>"), current)}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": method}})
`

// registerFakeEngine installs the python-backed agent under two names for
// the duration of the test: "fake", which knows nothing about modes, and
// "fake-modes", which implements them.
func registerFakeEngine(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; skipping end-to-end ACP test")
	}

	script := filepath.Join(t.TempDir(), "fake_agent.py")
	if err := os.WriteFile(script, []byte(fakeAgent), 0o600); err != nil {
		t.Fatalf("writing fake agent: %v", err)
	}

	Engines["fake"] = Engine{
		Command:      "python3",
		Args:         []string{script},
		APIKeyEnv:    "FAKE_KEY",
		AuthMethodID: "fake-auth",
	}
	Engines["fake-modes"] = Engine{
		Command:      "python3",
		Args:         []string{script, "--modes"},
		APIKeyEnv:    "FAKE_KEY",
		AuthMethodID: "fake-auth",
	}
	t.Cleanup(func() {
		delete(Engines, "fake")
		delete(Engines, "fake-modes")
	})
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := NewServer(t.TempDir(), t.TempDir())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return srv, ts
}

func createSession(t *testing.T, ts *httptest.Server, id, key string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"engine":"fake"}`, id)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if key != "" {
		req.Header.Set(APIKeyHeader, key)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	return resp
}

func TestSessionLifecycleEndToEnd(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	resp := createSession(t, ts, "s1", "secret-key")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(resp.Body); err != nil {
			t.Fatalf("create returned %d (body unreadable: %v)", resp.StatusCode, err)
		}
		t.Fatalf("create returned %d: %s", resp.StatusCode, buf.String())
	}

	// Follow the stream before prompting, the way a browser does.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sessions/s1/events?offset=0", nil)
	if err != nil {
		t.Fatalf("building stream request: %v", err)
	}
	stream, err := ts.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer stream.Body.Close()
	if ct := stream.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("stream Content-Type = %q, want application/x-ndjson", ct)
	}

	promptResp, err := ts.Client().Post(ts.URL+"/sessions/s1/prompt", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer promptResp.Body.Close()
	// Accepted, not OK: the turn runs in the background and its output is
	// on the stream we are already reading.
	if promptResp.StatusCode != http.StatusAccepted {
		t.Errorf("prompt returned %d, want 202", promptResp.StatusCode)
	}

	var sawPrompt, sawAgentText, sawTurnEnd bool
	// Break inside the body, not in the loop condition: the condition
	// would call Scan() once more after the last event and block until the
	// context expired.
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("stream line is not an Event: %v (%s)", err, scanner.Text())
		}
		switch ev.Kind {
		case KindUserPrompt:
			sawPrompt = true
		case "agent_message_chunk":
			text := string(ev.Data)
			if !strings.Contains(text, "saw:hello") {
				t.Errorf("agent did not receive the prompt text: %s", text)
			}
			// The credential must have reached the child process env.
			// ACP carries no credential, so if this fails the whole auth
			// design is wrong, not just this test.
			if !strings.Contains(text, "key:secret-key") {
				t.Errorf("API key did not reach the engine environment: %s", text)
			}
			sawAgentText = true
		case KindTurnEnd:
			if !strings.Contains(string(ev.Data), "end_turn") {
				t.Errorf("turn ended with unexpected reason: %s", ev.Data)
			}
			sawTurnEnd = true
		case KindError:
			t.Fatalf("session reported an error: %s", ev.Data)
		}
		if sawTurnEnd {
			break
		}
	}

	if !sawPrompt {
		t.Error("transcript did not record the user's own prompt")
	}
	if !sawAgentText {
		t.Error("never saw the agent's reply on the stream")
	}
	if !sawTurnEnd {
		t.Error("never saw the turn end")
	}
}

func TestEventsResumeFromOffset(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)

	resp := createSession(t, ts, "s1", "k")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d", resp.StatusCode)
	}

	srv.mu.Lock()
	sess := srv.sessions["s1"]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatal("session missing from registry after create")
	}
	if err := sess.Transcript().AppendValue("agent_message_chunk", map[string]string{"text": "first"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	offset := sess.Transcript().Size()
	if err := sess.Transcript().AppendValue("agent_message_chunk", map[string]string{"text": "second"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// follow=false makes this a one-shot read, which is what a client
	// catching up on history wants before it starts streaming.
	url := fmt.Sprintf("%s/sessions/s1/events?follow=false&offset=%d", ts.URL, offset)
	got, err := ts.Client().Get(url)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer got.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(got.Body); err != nil {
		t.Fatalf("reading events: %v", err)
	}
	if strings.Contains(buf.String(), "first") {
		t.Errorf("resuming at offset %d replayed already-seen events: %s", offset, buf.String())
	}
	if !strings.Contains(buf.String(), "second") {
		t.Errorf("resuming at offset %d missed the new event: %s", offset, buf.String())
	}
}

func TestCreateSessionRequiresAPIKeyHeader(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	resp := createSession(t, ts, "s1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("create without %s returned %d, want 400", APIKeyHeader, resp.StatusCode)
	}
}

func TestCreateSessionRejectsDuplicateID(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	first := createSession(t, ts, "s1", "k")
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create returned %d", first.StatusCode)
	}

	second := createSession(t, ts, "s1", "k")
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Errorf("duplicate create returned %d, want 409", second.StatusCode)
	}
}

func TestUnknownSessionIsNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	for _, path := range []string{
		"/sessions/nope",
		"/sessions/nope/events",
	} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s returned %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestDeleteSessionEndsIt(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)

	resp := createSession(t, ts, "s1", "k")
	resp.Body.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/s1", nil)
	if err != nil {
		t.Fatalf("building delete: %v", err)
	}
	del, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE returned %d, want 204", del.StatusCode)
	}

	srv.mu.Lock()
	_, stillThere := srv.sessions["s1"]
	srv.mu.Unlock()
	if stillThere {
		t.Error("session remained in the registry after delete")
	}
}

func TestStaleAndMalformedPermissionAnswers(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	resp := createSession(t, ts, "s1", "k")
	resp.Body.Close()

	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing requestId", `{"optionId":"allow"}`, http.StatusBadRequest},
		{"no option and not cancelled", `{"requestId":"perm-1"}`, http.StatusBadRequest},
		// Answering a request that already timed out changes nothing; the
		// UI should re-read the transcript rather than retry.
		{"stale request", `{"requestId":"perm-99","optionId":"allow"}`, http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ts.Client().Post(ts.URL+"/sessions/s1/permission", "application/json",
				strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST permission: %v", err)
			}
			got.Body.Close()
			if got.StatusCode != tc.want {
				t.Errorf("got %d, want %d", got.StatusCode, tc.want)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz returned %d, want 200", resp.StatusCode)
	}
}
