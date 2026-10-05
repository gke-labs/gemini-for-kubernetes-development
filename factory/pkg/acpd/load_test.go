package acpd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func deleteSession(t *testing.T, ts *httptest.Server, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/"+id, nil)
	if err != nil {
		t.Fatalf("building delete: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE returned %d", resp.StatusCode)
	}
}

var sidRE = regexp.MustCompile(`sid:(\S+)`)

func echoedSID(t *testing.T, echo string) string {
	t.Helper()
	m := sidRE.FindStringSubmatch(echo)
	if m == nil {
		t.Fatalf("the agent did not name its session: %s", echo)
	}
	return m[1]
}

// The engine going away — acpd restarted, the pod came back — used to
// cost the agent its memory: the same id, the same transcript, and an
// agent that had never seen any of it.
func TestARecreatedSessionContinuesTheConversation(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)

	first := decodeSession(t, create(t, ts, "s1", "fake-load", GeminiModeYolo), http.StatusCreated)
	if first.Loaded {
		t.Error("a session with nothing recorded reports it was loaded")
	}
	sid := echoedSID(t, promptAndReadEcho(t, ts, "s1", "hello"))
	if rec, ok := ReadRecord(filepath.Join(srv.stateDir, "s1")); !ok || rec.ACPSessionID != sid || rec.Engine != "fake-load" {
		t.Fatalf("record = %+v, %v; want the agent's id %s", rec, ok, sid)
	}

	deleteSession(t, ts, "s1")
	again := decodeSession(t, create(t, ts, "s1", "fake-load", GeminiModeYolo), http.StatusCreated)
	if !again.Loaded {
		t.Fatal("the recreated session started fresh")
	}
	if again.Mode != GeminiModeYolo {
		t.Errorf("mode = %q after load, want %q", again.Mode, GeminiModeYolo)
	}

	echo := promptAndReadEcho(t, ts, "s1", "again")
	if got := echoedSID(t, echo); got != sid {
		t.Errorf("the agent is on session %s, want the recorded %s", got, sid)
	}
	if !strings.Contains(echo, "history:hello") {
		t.Errorf("the agent does not remember the first turn: %s", echo)
	}

	// The transcript already had the first turn; the replay is not
	// written over it a second time.
	if transcriptHas(t, ts, "s1", "user_message_chunk", "replay:") {
		t.Error("the replayed conversation was appended to the transcript")
	}
	if !transcriptHas(t, ts, "s1", KindUserPrompt, "hello") {
		t.Error("the transcript lost the turn before the restart")
	}
	if !transcriptHas(t, ts, "s1", KindSessionLoaded, sid) {
		t.Error("the transcript does not mark where the conversation was picked up")
	}
}

func TestAnEngineThatCannotLoadStartsFresh(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)

	decodeSession(t, create(t, ts, "s1", "fake", ""), http.StatusCreated)
	if _, ok := ReadRecord(filepath.Join(srv.stateDir, "s1")); !ok {
		t.Fatal("no record written")
	}
	deleteSession(t, ts, "s1")

	again := decodeSession(t, create(t, ts, "s1", "fake", ""), http.StatusCreated)
	if again.Loaded {
		t.Error("an engine without loadSession reports a loaded session")
	}
	if transcriptHas(t, ts, "s1", KindError, "") {
		t.Error("an engine that never offered to load is reported as having failed to")
	}
}

func TestAFailedLoadStartsFreshAndSaysSo(t *testing.T) {
	registerFakeEngine(t)
	srv, ts := newTestServer(t)
	dir := filepath.Join(srv.stateDir, "s1")

	decodeSession(t, create(t, ts, "s1", "fake-load", ""), http.StatusCreated)
	promptAndReadEcho(t, ts, "s1", "hello")
	deleteSession(t, ts, "s1")
	// A conversation the engine no longer has: its home was wiped, say.
	if err := writeRecord(dir, Record{ACPSessionID: "gone", Engine: "fake-load", CWD: srv.cwd}); err != nil {
		t.Fatal(err)
	}

	again := decodeSession(t, create(t, ts, "s1", "fake-load", ""), http.StatusCreated)
	if again.Loaded {
		t.Fatal("a session the engine could not load reports it was loaded")
	}
	if !transcriptHas(t, ts, "s1", KindError, "could not continue") {
		t.Error("the transcript does not say the agent forgot")
	}
	if rec, _ := ReadRecord(dir); rec.ACPSessionID == "gone" {
		t.Error("the record still names the conversation that could not be loaded")
	}
}

// A record is the engine's conversation in one directory; another engine
// or another checkout has never heard of it.
func TestARecordForAnotherEngineOrDirectoryIsNotLoaded(t *testing.T) {
	registerFakeEngine(t)
	for name, rec := range map[string]Record{
		"engine":    {ACPSessionID: "x", Engine: "fake"},
		"directory": {ACPSessionID: "x", Engine: "fake-load", CWD: "/elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, ts := newTestServer(t)
			if rec.CWD == "" {
				rec.CWD = srv.cwd
			}
			dir := filepath.Join(srv.stateDir, "s1")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := writeRecord(dir, rec); err != nil {
				t.Fatal(err)
			}
			got := decodeSession(t, create(t, ts, "s1", "fake-load", ""), http.StatusCreated)
			if got.Loaded {
				t.Error("loaded a record that is not this session's")
			}
			if transcriptHas(t, ts, "s1", KindError, "") {
				t.Error("a record that did not apply was reported as a failed load")
			}
		})
	}
}
